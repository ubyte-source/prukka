package ffmpeg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	neturl "net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// device:// URLs map local devices onto ffmpeg's platform device layers;
// <id> is the platform's index or name (docs/DEVICES.md).
const deviceScheme = "device://"

// kindAudio is the device:// kind that maps onto an audio capture or push.
const kindAudio = "audio"

// GOOS names used across the package's platform dispatch (goconst).
const (
	osDarwin  = "darwin"
	osLinux   = "linux"
	osWindows = "windows"
)

// ffmpeg device layers named once (goconst).
const (
	fmtPulse        = "pulse"
	fmtAVFoundation = "avfoundation"
)

// Stream selectors shared by the demux invocations (goconst).
const (
	mapFirstAudio  = "0:a:0"
	mapFirstVideo  = "0:v:0"
	mapSecondAudio = "1:a:0"
)

// playbackDrainTimeout bounds how long a sealed playback helper may take to
// drain its scheduled audio before it is killed.
const playbackDrainTimeout = 5 * time.Second

// IsDeviceURL reports whether a URL names a local device.
func IsDeviceURL(url string) bool {
	return strings.HasPrefix(url, deviceScheme)
}

// ListRaw runs one ffmpeg device-listing invocation and returns everything
// it printed on either stream. Listings exit non-zero by design (no real
// input follows the flag), so the exit status is deliberately ignored;
// only a binary that could not run at all is an error.
func ListRaw(ctx context.Context, bin string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if len(out) == 0 && err != nil {
		return "", fmt.Errorf("list devices: %w", err)
	}

	return string(out), nil
}

// deviceParts splits device://<kind>/<id>[?label=<name>]. The label is a
// display-name rebinding hint: positional ids reshuffle whenever a device
// appears or vanishes (OBS, AirPods, Continuity), so consumers rebind by
// label at start time whenever one is present.
func deviceParts(url string) (kind, id, label string, err error) {
	kind, id, found := strings.Cut(strings.TrimPrefix(url, deviceScheme), "/")
	if !found || kind == "" || id == "" {
		return "", "", "", fmt.Errorf("device URL %q: want device://audio/<id> or device://video/<id>", url)
	}

	if bare, query, tagged := strings.Cut(id, "?"); tagged {
		id = bare
		if values, parseErr := neturl.ParseQuery(query); parseErr == nil {
			label = values.Get("label")
		}
	}
	if id == "" {
		return "", "", "", fmt.Errorf("device URL %q: empty device id", url)
	}

	return kind, id, label, nil
}

// OutputIndexResolver maps an output device label to its current position in
// the system device array. The ffmpeg package cannot reach CoreAudio itself,
// so the composition root supplies the platform lookup and it is threaded
// through DeviceOutputArgs — no process-global state.
type OutputIndexResolver func(label string) (int, bool)

// outputIndex prefers the label's current index over the embedded one.
func outputIndex(id, label string, resolve OutputIndexResolver) string {
	if resolve != nil && label != "" {
		if fresh, ok := resolve(label); ok {
			return strconv.Itoa(fresh)
		}
	}

	return id
}

// avSource is a camera paired with a microphone: the capture input args,
// where each stream lives in the inputs, and — always, cameras deliver
// raw frames — a video leg that encodes instead of copying.
type avSource struct {
	audioMap string
	videoMap string
	input    []string
}

// IsAVDeviceURL reports a paired camera+microphone source.
func IsAVDeviceURL(url string) bool {
	return strings.HasPrefix(url, deviceScheme+"av/")
}

// deviceAVConfigured parses a device://av/<camera>|<microphone> source into
// its platform capture invocation.
func deviceAVConfigured(url string, config pcmConfig) (avSource, error) {
	return deviceAVConfiguredFor(runtime.GOOS, url, config)
}

func deviceAVConfiguredFor(goos, url string, config pcmConfig) (avSource, error) {
	_, id, label, err := deviceParts(url)
	if err != nil {
		return avSource{}, err
	}

	return avInputArgs(goos, url, id, label, config)
}

// avParts splits the id of device://av/<video>|<audio>.
func avParts(url, id string) (video, audio string, err error) {
	video, audio, found := strings.Cut(id, "|")
	if !found || video == "" || audio == "" {
		return "", "", fmt.Errorf("device URL %q: want device://av/<camera>|<microphone>", url)
	}

	return video, audio, nil
}

// avInputArgs builds the combined camera+microphone capture for one
// platform: one avfoundation/dshow input on macOS/Windows, a v4l2 plus a
// pulse input on Linux (v4l2 nodes carry no audio).
func avInputArgs(goos, url, id, label string, config pcmConfig) (avSource, error) {
	video, audio, err := avParts(url, id)
	if err != nil {
		return avSource{}, err
	}

	switch goos {
	case osDarwin:
		// Keep the camera's positional selector, but prefer the microphone's
		// display-name hint when AVFoundation can parse it unambiguously.
		if label != "" && !strings.Contains(label, ":") {
			audio = label
		}

		return avSource{
			input:    []string{flagFormat, fmtAVFoundation, "-framerate", "30", flagInput, video + ":" + audio},
			audioMap: mapFirstAudio, videoMap: mapFirstVideo,
		}, nil
	case osWindows:
		return avSource{
			input: append(
				[]string{flagFormat, "dshow"}, deviceCaptureArgs(goos, config.deviceBuffer)...,
			),
			audioMap: mapFirstAudio, videoMap: mapFirstVideo,
		}.withInput(flagInput, "video="+video+":audio="+audio), nil
	case osLinux:
		return avSource{
			input: append(
				[]string{flagFormat, "v4l2", flagInput, video, flagFormat, fmtPulse},
				deviceCaptureArgs(goos, config.deviceBuffer)...,
			),
			audioMap: mapSecondAudio, videoMap: mapFirstVideo,
		}.withInput(flagInput, audio), nil
	default:
		return avSource{}, fmt.Errorf("device source %q: camera capture is not supported on %s", url, goos)
	}
}

func (source avSource) withInput(tokens ...string) avSource {
	source.input = append(source.input, tokens...)

	return source
}

// deviceInputArgsConfigured builds the capture-side input for one device
// source.
func deviceInputArgsConfigured(url string, config pcmConfig) ([]string, error) {
	return deviceInputArgsFor(runtime.GOOS, url, config)
}

func deviceInputArgsFor(goos, url string, config pcmConfig) ([]string, error) {
	kind, id, label, err := deviceParts(url)
	if err != nil {
		return nil, err
	}

	if kind != kindAudio {
		return nil, fmt.Errorf("device source %q: only audio capture is supported as a session source", url)
	}

	switch goos {
	case osDarwin:
		// avfoundation resolves names itself; a colon would read as its
		// video:audio separator, so such labels keep the index.
		if label != "" && !strings.Contains(label, ":") {
			return []string{flagFormat, fmtAVFoundation, flagInput, ":" + label}, nil
		}

		return []string{flagFormat, fmtAVFoundation, flagInput, ":" + id}, nil
	case osWindows:
		return append(
			[]string{flagFormat, "dshow"},
			append(deviceCaptureArgs(goos, config.deviceBuffer), flagInput, "audio="+id)...,
		), nil
	default: // linux and the BSDs
		return append(
			[]string{flagFormat, fmtPulse},
			append(deviceCaptureArgs(goos, config.deviceBuffer), flagInput, id)...,
		), nil
	}
}

// deviceCaptureArgs translates a duration into the private knobs documented
// by FFmpeg's DirectShow and PulseAudio inputs. Pulse fragment_size is bytes at
// its documented default format (48 kHz, stereo, signed 16-bit).
func deviceCaptureArgs(goos string, duration time.Duration) []string {
	if duration <= 0 {
		return nil
	}

	switch goos {
	case osWindows:
		milliseconds := max(int64(1), int64((duration+time.Millisecond-1)/time.Millisecond))

		return []string{"-audio_buffer_size", strconv.FormatInt(milliseconds, 10)}
	case osLinux:
		const bytesPerSecond = 48_000 * 2 * 2

		byteCount := max(int64(1), int64((duration*time.Duration(bytesPerSecond)+time.Second-1)/time.Second))

		return []string{"-fragment_size", strconv.FormatInt(byteCount, 10)}
	default:
		return nil
	}
}

// DeviceOutputArgs builds the playback/injection side of a device push;
// platforms without a muxer report an honest error. resolve rebinds an output
// label to its current device index and may be nil when none is wired.
func DeviceOutputArgs(url string, resolve OutputIndexResolver) ([]string, error) {
	return deviceOutputArgs(runtime.GOOS, url, resolve)
}

func deviceOutputArgs(goos, url string, resolve OutputIndexResolver) ([]string, error) {
	kind, id, label, err := deviceParts(url)
	if err != nil {
		return nil, err
	}

	switch {
	case kind == kindAudio && goos == osDarwin:
		return []string{
			flagAudioCodec, codecPCM16LE, flagFormat, "audiotoolbox",
			"-audio_device_index", outputIndex(id, label, resolve), "-",
		}, nil
	case kind == kindAudio && goos == osLinux:
		// The pulse muxer's URL is only the stream NAME shown in mixers;
		// the sink is chosen by -device (default sink otherwise).
		return []string{flagAudioCodec, codecPCM16LE, flagFormat, fmtPulse, "-device", id, "prukka-dub"}, nil
	case kind == "video" && goos == osLinux:
		return []string{"-pix_fmt", "yuv420p", flagFormat, "v4l2", id}, nil
	default:
		return nil, fmt.Errorf(
			"device target %q: no %s output on %s yet — install the platform's virtual device and see docs/DEVICES.md",
			url, kind, goos)
	}
}

// IsAudioDeviceTarget reports whether target names a local audio device.
func IsAudioDeviceTarget(target string) bool {
	return strings.HasPrefix(target, deviceScheme+kindAudio+"/")
}

// DeviceTargetLabel extracts the display-name rebinding hint from a device
// push target, or "" for unlabeled targets.
func DeviceTargetLabel(target string) string {
	if !IsAudioDeviceTarget(target) {
		return ""
	}
	if _, _, label, err := deviceParts(target); err == nil {
		return label
	}

	return ""
}

// playbackSink is the native playback helper's stdin as a device sink. Close
// seals the pipe so the helper drains its scheduled audio and exits, with a
// bounded wait before the process is killed.
type playbackSink struct {
	stdin    io.WriteCloser
	cmd      *exec.Cmd
	closeErr error
	// drain is the bounded wait before the sealed helper is killed; a field
	// (not the const directly) so the timeout/kill branch is testable without
	// reintroducing package-global mutable state.
	drain time.Duration
	once  sync.Once
}

func (s *playbackSink) Write(p []byte) (int, error) { return s.stdin.Write(p) }

func (s *playbackSink) Close() error {
	s.once.Do(func() {
		sealErr := s.stdin.Close()
		done := make(chan error, 1)
		go func() { done <- s.cmd.Wait() }()
		select {
		case err := <-done:
			s.closeErr = errors.Join(sealErr, err)
		case <-time.After(s.drain):
			killErr := s.cmd.Process.Kill()
			s.closeErr = errors.Join(sealErr, killErr, <-done)
		}
	})

	return s.closeErr
}

// StartDevicePlayback spawns the native playback helper, which binds one
// output device by NAME and renders s16le mono PCM from its stdin. Name
// binding replaces the audiotoolbox array index — a position Continuity
// devices reshuffle at will — and the helper exits on an unrecoverable
// device change, handing recovery to the caller's reopen path. The helper
// path comes from the managed-runtime resolver and the label from the
// session's device URL; both are tokenized arguments, no shell is involved.
func StartDevicePlayback(
	ctx context.Context, helper, label string, rate int, log *slog.Logger,
) (io.WriteCloser, error) {
	cmd := exec.CommandContext(ctx, helper,
		"--play", "--device", label, "--rate", strconv.Itoa(rate))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("playback helper stdin: %w", err)
	}
	// A writer the command owns, not StderrPipe: Wait would otherwise race
	// the pipe reader — the documented os/exec misuse — and could drop the
	// helper's final diagnostic line on teardown.
	cmd.Stderr = &lineLogger{log: log, msg: "playback helper"}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start playback helper: %w", err)
	}
	log.Info("playback helper started", "device", label, "pid", cmd.Process.Pid)

	return &playbackSink{stdin: stdin, cmd: cmd, drain: playbackDrainTimeout}, nil
}

// lineLogger forwards a child's stderr to the daemon log one line at a time.
type lineLogger struct {
	log     *slog.Logger
	msg     string
	pending []byte
}

func (l *lineLogger) Write(p []byte) (int, error) {
	l.pending = append(l.pending, p...)
	for {
		nl := bytes.IndexByte(l.pending, '\n')
		if nl < 0 {
			return len(p), nil
		}
		if line := strings.TrimSpace(string(l.pending[:nl])); line != "" {
			l.log.Info(l.msg, "line", line)
		}
		l.pending = l.pending[nl+1:]
	}
}
