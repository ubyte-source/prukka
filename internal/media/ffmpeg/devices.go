package ffmpeg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ubyte-source/prukka/internal/hostos"
	"github.com/ubyte-source/prukka/internal/media/deviceurl"
	"github.com/ubyte-source/prukka/internal/procio"
	"github.com/ubyte-source/prukka/internal/redact"
)

const (
	fmtPulse        = "pulse"
	fmtAVFoundation = "avfoundation"
)

const (
	mapFirstAudio  = "0:a:0"
	mapFirstVideo  = "0:v:0"
	mapSecondAudio = "1:a:0"
)

// playbackDrainTimeout bounds a sealed playback helper's drain before it is killed.
const playbackDrainTimeout = 5 * time.Second

// ListRaw runs one ffmpeg device-listing invocation and returns everything it
// printed on either stream; listings exit non-zero by design, so only a binary
// that could not run at all is an error.
func ListRaw(ctx context.Context, bin string, args ...string) (string, error) {
	out, err := newCommand(ctx, bin, args).CombinedOutput()
	if len(out) == 0 && err != nil {
		return "", fmt.Errorf("list devices: %w", err)
	}

	return string(out), nil
}

// OutputIndexResolver maps an output device label to its current position in
// the system device array.
type OutputIndexResolver func(label string) (int, bool)

func outputIndex(ref deviceurl.Ref, resolve OutputIndexResolver) string {
	if resolve != nil && ref.Label != "" {
		if fresh, ok := resolve(ref.Label); ok {
			return strconv.Itoa(fresh)
		}
	}

	return ref.ID
}

// avSource is a camera paired with a microphone, plus where each stream sits
// in the inputs.
type avSource struct {
	audioMap string
	videoMap string
	input    []string
}

func deviceAVConfigured(url string, config pcmConfig) (avSource, error) {
	return deviceAVConfiguredFor(runtime.GOOS, url, config)
}

func deviceAVConfiguredFor(goos, url string, config pcmConfig) (avSource, error) {
	ref, err := deviceurl.Parse(url)
	if err != nil {
		return avSource{}, err
	}

	return avInputArgs(goos, ref, config)
}

// avInputArgs builds the combined camera+microphone capture; Linux needs a
// second, pulse input because v4l2 nodes carry no audio.
func avInputArgs(goos string, ref deviceurl.Ref, config pcmConfig) (avSource, error) {
	video, audio, err := ref.Pair()
	if err != nil {
		return avSource{}, err
	}

	switch goos {
	case hostos.Darwin:
		if ref.Label != "" && !strings.Contains(ref.Label, ":") {
			audio = ref.Label
		}

		return avSource{
			input:    []string{flagFormat, fmtAVFoundation, "-framerate", "30", flagInput, video + ":" + audio},
			audioMap: mapFirstAudio,
			videoMap: mapFirstVideo,
		}, nil
	case hostos.Windows:
		return avSource{
			input: append(
				[]string{flagFormat, "dshow"}, deviceCaptureArgs(goos, config.deviceBuffer)...,
			),
			audioMap: mapFirstAudio,
			videoMap: mapFirstVideo,
		}.withInput(flagInput, "video="+video+":audio="+audio), nil
	case hostos.Linux:
		return avSource{
			input: append(
				[]string{flagFormat, "v4l2", flagInput, video, flagFormat, fmtPulse},
				deviceCaptureArgs(goos, config.deviceBuffer)...,
			),
			audioMap: mapSecondAudio,
			videoMap: mapFirstVideo,
		}.withInput(flagInput, audio), nil
	default:
		return avSource{}, fmt.Errorf(
			"device source %s: camera capture is not supported on %s", redact.URL(ref.String()), goos)
	}
}

func (source avSource) withInput(tokens ...string) avSource {
	source.input = append(source.input, tokens...)

	return source
}

func deviceInputArgsConfigured(url string, config pcmConfig) ([]string, error) {
	return deviceInputArgsFor(runtime.GOOS, url, config)
}

func deviceInputArgsFor(goos, url string, config pcmConfig) ([]string, error) {
	ref, err := deviceurl.Parse(url)
	if err != nil {
		return nil, err
	}

	if ref.Kind != deviceurl.Audio {
		return nil, fmt.Errorf("device source %s: only audio capture is supported as a session source", redact.URL(url))
	}

	switch goos {
	case hostos.Darwin:
		// avfoundation resolves names itself, but reads a colon as its
		// video:audio separator, so such labels keep the index.
		if ref.Label != "" && !strings.Contains(ref.Label, ":") {
			return []string{flagFormat, fmtAVFoundation, flagInput, ":" + ref.Label}, nil
		}

		return []string{flagFormat, fmtAVFoundation, flagInput, ":" + ref.ID}, nil
	case hostos.Windows:
		return append(
			[]string{flagFormat, "dshow"},
			append(deviceCaptureArgs(goos, config.deviceBuffer), flagInput, "audio="+ref.ID)...,
		), nil
	default: // linux and the BSDs
		return append(
			[]string{flagFormat, fmtPulse},
			append(deviceCaptureArgs(goos, config.deviceBuffer), flagInput, ref.ID)...,
		), nil
	}
}

// deviceCaptureArgs translates a duration into FFmpeg's DirectShow and
// PulseAudio buffer knobs; pulse fragment_size counts bytes at its documented
// default format (48 kHz, stereo, signed 16-bit).
func deviceCaptureArgs(goos string, duration time.Duration) []string {
	if duration <= 0 {
		return nil
	}

	switch goos {
	case hostos.Windows:
		milliseconds := max(int64(1), int64((duration+time.Millisecond-1)/time.Millisecond))

		return []string{"-audio_buffer_size", strconv.FormatInt(milliseconds, 10)}
	case hostos.Linux:
		const bytesPerSecond = 48_000 * 2 * 2

		byteCount := max(int64(1), int64((duration*time.Duration(bytesPerSecond)+time.Second-1)/time.Second))

		return []string{"-fragment_size", strconv.FormatInt(byteCount, 10)}
	default:
		return nil
	}
}

// DeviceOutputArgs builds the playback/injection side of a device push;
// resolve rebinds an output label to its current index and may be nil.
func DeviceOutputArgs(url string, resolve OutputIndexResolver) ([]string, error) {
	return deviceOutputArgs(runtime.GOOS, url, resolve)
}

func deviceOutputArgs(goos, url string, resolve OutputIndexResolver) ([]string, error) {
	ref, err := deviceurl.Parse(url)
	if err != nil {
		return nil, err
	}

	switch {
	case ref.Kind == deviceurl.Audio && goos == hostos.Darwin:
		return []string{
			flagAudioCodec, codecPCM16LE, flagFormat, "audiotoolbox",
			"-audio_device_index", outputIndex(ref, resolve), "-",
		}, nil
	case ref.Kind == deviceurl.Audio && goos == hostos.Linux:
		// The pulse muxer's URL is only the stream NAME shown in mixers;
		// the sink is chosen by -device (default sink otherwise).
		return []string{
			flagAudioCodec, codecPCM16LE, flagFormat, fmtPulse, "-device", ref.ID, "prukka-dub",
		}, nil
	case ref.Kind == deviceurl.Video && goos == hostos.Linux:
		// Never the v4l2 muxer: the capture-only node rejects its S_FMT(OUTPUT)
		// probe, so raw frames go through ffmpeg's file protocol instead.
		return []string{"-pix_fmt", "yuyv422", flagFormat, "rawvideo", ref.ID}, nil
	default:
		return nil, fmt.Errorf(
			"device target %s: no %s output on %s yet — install the platform's virtual device and see docs/DEVICES.md",
			redact.URL(url), ref.Kind, goos)
	}
}

// linuxWebcamFilter normalizes a push to the webcam node's one fixed mode —
// 1280x720 at 30 fps, the PRUKKA_WIDTH/HEIGHT contract of
// drivers/linux/webcam. The node cannot negotiate.
const linuxWebcamFilter = "scale=1280:720,fps=30"

// DeviceVideoFilter returns the geometry-normalization filter a device video
// target requires, or "" when the target accepts the source as-is.
func DeviceVideoFilter(target string) string {
	return deviceVideoFilter(runtime.GOOS, target)
}

func deviceVideoFilter(goos, target string) string {
	if goos != hostos.Linux || !deviceurl.IsKind(target, deviceurl.Video) {
		return ""
	}

	return linuxWebcamFilter
}

// playbackSink is the native playback helper's stdin as a device sink.
type playbackSink struct {
	stdin     io.WriteCloser
	cmd       *exec.Cmd
	tree      procio.Tree
	closeOnce func() error
	drain     time.Duration
}

func newPlaybackSink(
	stdin io.WriteCloser, cmd *exec.Cmd, tree procio.Tree, drain time.Duration,
) *playbackSink {
	s := &playbackSink{stdin: stdin, cmd: cmd, tree: tree, drain: drain}
	s.closeOnce = sync.OnceValue(s.seal)

	return s
}

func (s *playbackSink) Write(p []byte) (int, error) { return s.stdin.Write(p) }

func (s *playbackSink) Close() error { return s.closeOnce() }

// seal closes the helper's input, then kills the whole group once the drain
// elapses. Kill must precede the reap and Release must follow it: procio.Tree's
// one legal order.
func (s *playbackSink) seal() error {
	sealErr := s.stdin.Close()
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	select {
	case err := <-done:
		return errors.Join(sealErr, err, s.tree.Release())
	case <-time.After(s.drain):
		killErr := s.tree.Kill()

		return errors.Join(sealErr, killErr, <-done, s.tree.Release())
	}
}

// StartDevicePlayback spawns the native playback helper, which binds one output
// device by name and renders s16le mono PCM from its stdin; it exits on an
// unrecoverable device change, handing recovery to the caller's reopen path.
func StartDevicePlayback(
	ctx context.Context, helper, label string, rate int, log *slog.Logger,
) (io.WriteCloser, error) {
	cmd := newCommand(ctx, helper,
		[]string{"--play", "--device", label, "--rate", strconv.Itoa(rate)})
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("playback helper stdin: %w", err)
	}
	// A writer the command owns, not StderrPipe: Wait races the pipe reader,
	// the documented os/exec misuse.
	cmd.Stderr = &lineLogger{log: log, msg: "playback helper"}
	tree, startErr := startChild(cmd, log, "playback helper")
	if startErr != nil {
		return nil, startErr
	}
	log.Info("playback helper started", "device", label, "pid", cmd.Process.Pid)

	return newPlaybackSink(stdin, cmd, tree, playbackDrainTimeout), nil
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
