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
	"github.com/ubyte-source/prukka/internal/media/deviceurl"
	"github.com/ubyte-source/prukka/internal/procio"
)

// Supervisor runs capture processes whose stdout is a reference-format PCM
// pipe (16 kHz mono s16le).
type Supervisor struct {
	log        *slog.Logger
	bin        string
	micCapture string
}

// PCMOption configures latency-sensitive demux behavior, per open.
type PCMOption func(*pcmConfig)

type pcmConfig struct {
	deviceBuffer time.Duration
}

// WithDeviceCaptureBuffer requests a smaller native capture fragment where
// FFmpeg's platform input exposes one; other backends keep their defaults.
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
// microphone helper at path, because macOS silences ffmpeg's AVFoundation
// input for a process launchd started.
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

var quietArgs = []string{"-hide_banner", "-nostdin", "-loglevel", "error"}

// deviceQuietArgs raises local captures to warning so the platform input's
// authorization and format diagnostics survive into the classified stderr tail.
var deviceQuietArgs = []string{"-hide_banner", "-nostdin", "-loglevel", "warning"}

func quietArgsFor(src string) []string {
	if deviceurl.Is(src) {
		return deviceQuietArgs
	}

	return quietArgs
}

func s16le() []string {
	return []string{flagFormat, "s16le", "-ar", strconv.Itoa(core.SampleRate), "-ac", "1"}
}

func argv(groups ...[]string) []string {
	return slices.Concat(groups...)
}

// pcmArgs builds the demux invocation; a non-empty videoDir adds the video tap
// to the SAME process, because listen sources accept one connection.
func pcmArgs(src, videoDir string, delay time.Duration, options ...PCMOption) ([]string, error) {
	config := applyPCMOptions(options)
	if deviceurl.IsKind(src, deviceurl.AV) {
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

func sourceInput(src string, config pcmConfig) ([]string, error) {
	switch {
	case strings.HasPrefix(src, "rtmp://"):
		return []string{"-listen", "1", flagInput, src}, nil
	case strings.HasPrefix(src, "srt://") && !strings.Contains(src, "mode="):
		return []string{flagInput, src + listenQuery(src)}, nil
	case strings.HasPrefix(src, "srt://"):
		return []string{flagInput, src}, nil
	case deviceurl.Is(src):
		return deviceInputArgsConfigured(src, config)
	default:
		return []string{flagRealtime, flagInput, src}, nil
	}
}

func hlsVideoArgs(dir string, delay time.Duration) []string {
	return HLSOutput(dir, delay, flagMap, "0:v:0?", "-c:v", "copy")
}

// avArgs demuxes a paired camera+microphone capture; the camera delivers raw
// frames, so the HLS rendition must encode rather than copy.
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
// timestamps. FFmpeg 8.1.2 drops native frames when its single-slot
// AVFoundation callback outruns avf_read_packet, and the raw pipe would
// compact those gaps into progressive earliness; the asynchronous resampler
// represents them as silence instead.
func deviceTimelineArgs(goos, src string) []string {
	if goos != hostos.Darwin || !deviceurl.Is(src) {
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

func listenQuery(src string) string {
	if strings.Contains(src, "?") {
		return "&mode=listener"
	}

	return "?mode=listener"
}

// childWaitDelay bounds how long cmd.Wait blocks for the stdio copiers once
// the child has exited. os/exec joins them only when EVERY holder of the
// pipes' write ends closes, and an ffmpeg resolved from a snap, flatpak or nix
// shim is a wrapper script that forks a grandchild holding those descriptors.
const childWaitDelay = 2 * time.Second

// newCommand is the one exec path in this package, so no child misses the wait
// bound or the process group its teardown kills. CommandContext's
// kill-on-cancel default must not be cleared: exits are classified against
// ctx.Done.
func newCommand(ctx context.Context, bin string, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.WaitDelay = childWaitDelay
	procio.PrepareTree(cmd)

	return cmd
}

// startChild starts a prepared command and binds its process tree; an
// attachment failure is a diagnostic, not a start failure.
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
// the reader stops the process, a self-exit ends it with EOF.
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

func (s *Supervisor) micCaptureInvocation(src, videoDir string) (bin string, args []string, ok bool) {
	return micCaptureCommand(runtime.GOOS, s.micCapture, src, videoDir)
}

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

	return stopFirst(&process{
		cmd:    cmd,
		out:    stdout,
		log:    s.log,
		stderr: stderr,
		tree:   tree,
		src:    label,
		name:   name,
		done:   ctx.Done(),
	}), nil
}
