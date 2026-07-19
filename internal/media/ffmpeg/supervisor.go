package ffmpeg

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/hostos"
	"github.com/ubyte-source/prukka/internal/procio"
)

// Supervisor runs capture processes whose stdout is a reference-format PCM
// pipe (16 kHz mono s16le): ffmpeg for network, file and paired A/V sources,
// and — where configured — the native microphone helper for macOS
// audio-device capture.
type Supervisor struct {
	log        *slog.Logger
	bin        string
	micCapture string
}

// PCMOption configures latency-sensitive demux behavior. Options are passed
// per open so one Supervisor can serve both robust broadcast lanes and
// low-buffer call lanes.
type PCMOption func(*pcmConfig)

type pcmConfig struct {
	deviceBuffer time.Duration
}

// WithDeviceCaptureBuffer requests a smaller native capture fragment where
// FFmpeg's platform input exposes one. Unsupported device backends retain
// their defaults.
func WithDeviceCaptureBuffer(duration time.Duration) PCMOption {
	if duration <= 0 {
		panic("device capture buffer must be positive")
	}

	return func(config *pcmConfig) { config.deviceBuffer = duration }
}

func applyPCMOptions(options []PCMOption) pcmConfig {
	var config pcmConfig
	for _, option := range options {
		option(&config)
	}

	return config
}

// SupervisorOption customizes a supervisor at construction.
type SupervisorOption func(*Supervisor)

// WithMicCapture routes macOS audio-device capture through the native
// microphone helper at path instead of ffmpeg's AVFoundation input, which
// macOS silences for a process launchd started. An empty path is ignored.
func WithMicCapture(path string) SupervisorOption {
	return func(s *Supervisor) { s.micCapture = path }
}

// NewSupervisor wires a supervisor around a resolved ffmpeg binary.
func NewSupervisor(bin string, log *slog.Logger, options ...SupervisorOption) *Supervisor {
	supervisor := &Supervisor{bin: bin, log: log}
	for _, option := range options {
		option(supervisor)
	}

	return supervisor
}

// ffmpegName is both the ffmpeg executable basename and the backend label
// used in capture diagnostics.
const ffmpegName = "ffmpeg"

// ffmpeg argument tokens named once (the linter's constant rule and DRY).
const (
	flagInput      = "-i"
	flagFormat     = "-f"
	flagMap        = "-map"
	flagRealtime   = "-re"
	flagAudioCodec = "-c:a"
	codecPCM16LE   = "pcm_s16le"
	pipeIn         = "pipe:0"
	pipeOut        = "pipe:1"
)

// quietArgs is the shared invocation prefix: no banner, no stdin, errors
// only. Package-level immutable data.
var quietArgs = []string{"-hide_banner", "-nostdin", "-loglevel", "error"}

// deviceQuietArgs raises local captures to warning so the platform input's
// authorization and format diagnostics survive into the classified stderr
// tail instead of being suppressed; network and file sources stay error-only.
var deviceQuietArgs = []string{"-hide_banner", "-nostdin", "-loglevel", "warning"}

// quietArgsFor picks the invocation prefix for one source: local device
// captures keep their warning-level diagnostics, everything else stays quiet.
func quietArgsFor(src string) []string {
	if IsDeviceURL(src) {
		return deviceQuietArgs
	}

	return quietArgs
}

// s16le describes a raw PCM stream of the given format — the one place the
// s16le triplet is spelled.
func s16le() []string {
	return []string{flagFormat, "s16le", "-ar", strconv.Itoa(core.SampleRate), "-ac", "1"}
}

// argv concatenates argument groups into one invocation.
func argv(groups ...[]string) []string {
	return slices.Concat(groups...)
}

// pcmArgs builds the demux invocation; a non-empty videoDir adds the video
// tap to the SAME process (listen sources accept one connection). A source
// the package cannot translate is an error, never a doomed child process.
func pcmArgs(src, videoDir string, delay time.Duration, options ...PCMOption) ([]string, error) {
	config := applyPCMOptions(options)
	if IsAVDeviceURL(src) {
		av, err := deviceAVConfigured(src, config)
		if err != nil {
			return nil, err
		}

		return avArgs(av, videoDir, delay, deviceTimelineArgs(runtime.GOOS, src)), nil
	}

	input, err := sourceInput(src, config)
	if err != nil {
		return nil, err
	}

	args := argv(quietArgsFor(src), input,
		[]string{flagMap, mapFirstAudio, "-vn", "-sn", "-dn"},
		deviceTimelineArgs(runtime.GOOS, src),
		s16le(), []string{pipeOut})

	if videoDir != "" {
		args = argv(args, hlsVideoArgs(videoDir, delay))
	}

	return args, nil
}

// sourceInput picks the capture or listen input arguments for one source.
func sourceInput(src string, config pcmConfig) ([]string, error) {
	switch {
	case strings.HasPrefix(src, "rtmp://"):
		return []string{"-listen", "1", flagInput, src}, nil
	case strings.HasPrefix(src, "srt://") && !strings.Contains(src, "mode="):
		return []string{flagInput, src + listenQuery(src)}, nil
	case strings.HasPrefix(src, "srt://"):
		return []string{flagInput, src}, nil
	case IsDeviceURL(src):
		return deviceInputArgsConfigured(src, config)
	default:
		return []string{flagRealtime, flagInput, src}, nil
	}
}

// hlsVideoArgs is the passthrough HLS video output: no re-encode, shifted
// by delay D onto the shared clock.
func hlsVideoArgs(dir string, delay time.Duration) []string {
	return HLSOutput(dir, delay, flagMap, "0:v:0?", "-c:v", "copy")
}

// avArgs demuxes a paired camera+microphone capture: PCM to stdout and,
// with a videoDir, the camera's raw frames encoded into the HLS rendition
// (cameras deliver raw video — nothing to "copy").
func avArgs(av avSource, videoDir string, delay time.Duration, timeline []string) []string {
	args := argv(deviceQuietArgs, av.input,
		[]string{flagMap, av.audioMap, "-vn", "-sn", "-dn"},
		timeline,
		s16le(), []string{pipeOut})

	if videoDir != "" {
		args = argv(args, HLSOutput(videoDir, delay,
			flagMap, av.videoMap, "-c:v", "libx264", "-preset", "veryfast",
			"-b:v", "2500k", "-pix_fmt", "yuv420p", "-g", "60"))
	}

	return args
}

// deviceTimelineArgs makes the PCM sample clock follow AVFoundation's capture
// timestamps. FFmpeg 8.1.2 can drop whole native frames when its single-slot
// AVFoundation callback outruns avf_read_packet; without this repair the raw
// pipe compacts the gaps and a live call becomes progressively early and
// choppy. The asynchronous resampler represents missing intervals as silence,
// preserving cadence until the managed runtime contains the upstream demuxer
// fix. Other device backends and network/file sources remain bit-for-bit
// unchanged.
func deviceTimelineArgs(goos, src string) []string {
	if goos != hostos.Darwin || !IsDeviceURL(src) {
		return nil
	}

	return []string{
		"-af", "aresample=16000:async=1:min_hard_comp=0.001:first_pts=0",
	}
}

// HLSOutput is the rolling live-window output shape; offset shifts
// timestamps onto the shared clock.
func HLSOutput(dir string, offset time.Duration, codec ...string) []string {
	shift := []string{}
	if offset > 0 {
		shift = []string{"-output_ts_offset", strconv.FormatFloat(offset.Seconds(), 'f', 3, 64)}
	}

	return argv(codec, shift, []string{
		flagFormat, "hls",
		"-hls_time", "4",
		"-hls_list_size", "6",
		"-hls_flags", "delete_segments+independent_segments",
		"-hls_segment_filename", dir + "/seg%05d.ts",
		dir + "/index.m3u8",
	})
}

// listenQuery appends SRT listener mode with the right separator.
func listenQuery(src string) string {
	if strings.Contains(src, "?") {
		return "&mode=listener"
	}

	return "?mode=listener"
}

// childWaitDelay bounds how long cmd.Wait blocks for the stdio copier
// goroutines once the child itself has exited. Every media child is given a
// non-*os.File stderr (the tail buffer) plus piped stdio, so os/exec joins
// copier goroutines that return only when EVERY holder of those pipes' write
// ends closes them — and an ffmpeg resolved from a snap, flatpak or nix shim
// is a wrapper script that forks a grandchild holding exactly those
// descriptors. Without this bound one escaped descendant turns every
// "bounded" close in this package into an unbounded one; with it, Wait
// force-closes the parent-side descriptors and returns.
const childWaitDelay = 2 * time.Second

// newCommand builds the authorized media invocation: every exec in this
// package funnels through here, so no child can miss the wait bound or the
// process group its teardown kills. Cancellation stays with CommandContext —
// exits are classified against ctx.Done and the PCM and mux teardowns are
// context-driven — so the kill-on-cancel default must not be cleared here.
func newCommand(ctx context.Context, bin string, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.WaitDelay = childWaitDelay
	procio.PrepareTree(cmd)

	return cmd
}

// startChild starts a prepared command and binds its process tree: the one
// place a command becomes a supervised child. An attachment failure is a
// diagnostic, not a start failure — the returned tree still retires the
// direct child. name labels the child in diagnostics.
func startChild(cmd *exec.Cmd, log *slog.Logger, name string) (procio.Tree, error) {
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", name, err)
	}

	tree, err := procio.AttachTree(cmd)
	if err != nil {
		log.Warn(name+" process-tree fallback", "pid", cmd.Process.Pid, "err", err)
	}

	return tree, nil
}

// StartPCM launches the capture for src and returns its PCM stdout; closing
// the reader stops the process, a self-exit ends it with EOF. A macOS
// audio-device source is captured through the native microphone helper when
// one is configured; everything else demuxes through ffmpeg.
func (s *Supervisor) StartPCM(
	ctx context.Context, src, videoDir string, delay time.Duration, options ...PCMOption,
) (io.ReadCloser, error) {
	if bin, args, ok := s.micCaptureInvocation(src, videoDir); ok {
		return s.start(ctx, bin, args, src, "miccapture")
	}

	args, err := pcmArgs(src, videoDir, delay, options...)
	if err != nil {
		return nil, err
	}

	return s.start(ctx, s.bin, args, src, ffmpegName)
}

// micCaptureInvocation returns the native helper command for a macOS
// audio-device source when a helper is configured.
func (s *Supervisor) micCaptureInvocation(src, videoDir string) (bin string, args []string, ok bool) {
	return micCaptureCommand(runtime.GOOS, s.micCapture, src, videoDir)
}

// start spawns a capture child and wraps its stdout as a PCM process; name
// labels the backend in diagnostics.
func (s *Supervisor) start(
	ctx context.Context, bin string, args []string, src, name string,
) (io.ReadCloser, error) {
	cmd := newCommand(ctx, bin, args)

	stderr := procio.NewTailBuffer(procio.DefaultStderrTail)
	cmd.Stderr = stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("%s stdout: %w", name, err)
	}

	tree, startErr := startChild(cmd, s.log, name)
	if startErr != nil {
		return nil, startErr
	}

	label := endpointLabel(src)
	s.log.Info(name+" started", "source", label, "pid", cmd.Process.Pid)

	return &process{
		cmd: cmd, out: stdout, log: s.log, stderr: stderr, tree: tree,
		src: label, name: name, done: ctx.Done(),
	}, nil
}
