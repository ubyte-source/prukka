package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

// stderrTail bounds how much of ffmpeg's stderr is kept for classification.
const stderrTail = 4 << 10

// Supervisor runs ffmpeg processes whose stdout is a reference-format PCM
// pipe (demux → 16 kHz mono s16le).
type Supervisor struct {
	log *slog.Logger
	bin string
}

// NewSupervisor wires a supervisor around a resolved ffmpeg binary.
func NewSupervisor(bin string, log *slog.Logger) *Supervisor {
	return &Supervisor{bin: bin, log: log}
}

// ffmpeg argument tokens named once (the linter's constant rule and DRY).
const (
	flagInput    = "-i"
	flagFormat   = "-f"
	flagMap      = "-map"
	flagRealtime = "-re"
	pipeIn       = "pipe:0"
	pipeOut      = "pipe:1"
)

// quietArgs is the shared invocation prefix: no banner, no stdin, errors
// only. Package-level immutable data.
var quietArgs = []string{"-hide_banner", "-nostdin", "-loglevel", "error"}

// s16le describes a raw PCM stream of the given format — the one place the
// s16le triplet is spelled.
func s16le() []string {
	return []string{flagFormat, "s16le", "-ar", strconv.Itoa(pipeline.SampleRate), "-ac", "1"}
}

// argv concatenates argument groups into one invocation.
func argv(groups ...[]string) []string {
	out := make([]string, 0, 24)
	for _, g := range groups {
		out = append(out, g...)
	}

	return out
}

// pcmArgs builds the demux invocation; a non-empty videoDir adds the video
// tap to the SAME process (listen sources accept one connection).
func pcmArgs(src, videoDir string, delay time.Duration) []string {
	if IsAVDeviceURL(src) {
		if av, err := deviceAV(src); err == nil {
			return avArgs(av, videoDir, delay)
		}
		// A malformed pairing falls through to the generic input below;
		// ffmpeg's stderr tail names the problem at start.
	}

	args := argv(quietArgs, sourceInput(src),
		[]string{flagMap, mapFirstAudio, "-vn", "-sn", "-dn"},
		s16le(), []string{pipeOut})

	if videoDir != "" {
		args = argv(args, hlsVideoArgs(videoDir, delay))
	}

	return args
}

// sourceInput picks the capture or listen input arguments for one source.
func sourceInput(src string) []string {
	switch {
	case strings.HasPrefix(src, "rtmp://"):
		return []string{"-listen", "1", flagInput, src}
	case strings.HasPrefix(src, "srt://") && !strings.Contains(src, "mode="):
		return []string{flagInput, src + listenQuery(src)}
	case strings.HasPrefix(src, "srt://"):
		return []string{flagInput, src}
	case IsDeviceURL(src):
		// Device capture is inherently live; a bad id fails at start and
		// is reported through the process's stderr tail.
		if deviceInput, err := deviceInputArgs(src); err == nil {
			return deviceInput
		}

		return []string{flagInput, src}
	default:
		return []string{flagRealtime, flagInput, src}
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
func avArgs(av avSource, videoDir string, delay time.Duration) []string {
	args := argv(quietArgs, av.input,
		[]string{flagMap, av.audioMap, "-vn", "-sn", "-dn"},
		s16le(), []string{pipeOut})

	if videoDir != "" {
		args = argv(args, HLSOutput(videoDir, delay,
			flagMap, av.videoMap, "-c:v", "libx264", "-preset", "veryfast",
			"-b:v", "2500k", "-pix_fmt", "yuv420p", "-g", "60"))
	}

	return args
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

// newCommand builds the authorized ffmpeg invocation: every
// exec in the codebase funnels through here.
func newCommand(ctx context.Context, bin string, args []string) *exec.Cmd {
	return exec.CommandContext(ctx, bin, args...)
}

// StartPCM launches ffmpeg for src and returns its PCM stdout; closing the
// reader stops the process, a self-exit ends it with EOF.
func (s *Supervisor) StartPCM(
	ctx context.Context, src, videoDir string, delay time.Duration,
) (io.ReadCloser, error) {
	cmd := newCommand(ctx, s.bin, pcmArgs(src, videoDir, delay))

	stderr := &tailBuffer{limit: stderrTail}
	cmd.Stderr = stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg stdout: %w", err)
	}

	if startErr := cmd.Start(); startErr != nil {
		return nil, fmt.Errorf("start ffmpeg: %w", startErr)
	}

	label := endpointLabel(src)
	s.log.Info("ffmpeg started", "source", label, "pid", cmd.Process.Pid)

	return &process{
		cmd: cmd, out: stdout, log: s.log, stderr: stderr, src: label, done: ctx.Done(),
	}, nil
}

// process ties the PCM reader to the child's lifecycle; Wait must not run
// before the reader is done or buffered audio is lost.
type process struct {
	out     io.ReadCloser
	err     error
	cmd     *exec.Cmd
	log     *slog.Logger
	stderr  *tailBuffer
	done    <-chan struct{}
	src     string
	once    sync.Once
	drained atomic.Bool
}

// Read implements io.Reader over the PCM pipe; it reaches EOF once the
// child exits and the pipe drains.
func (p *process) Read(b []byte) (int, error) {
	n, err := p.out.Read(b)
	if errors.Is(err, io.EOF) {
		p.drained.Store(true)
	}

	return n, err
}

// Close stops a still-running child, reaps it exactly once and reports how
// it ended. Callers close only after they finished reading.
func (p *process) Close() error {
	return p.reap(true)
}

func (p *process) wait() error {
	return p.reap(false)
}

func (p *process) reap(stop bool) error {
	p.once.Do(func() {
		stop = stop && !p.drained.Load()
		stopped := false
		if stop && p.cmd.Process != nil {
			if err := p.cmd.Process.Kill(); err == nil {
				stopped = true
			} else if !errors.Is(err, os.ErrProcessDone) {
				p.log.Debug("ffmpeg kill", "source", p.src, "err", err)
			}
		}

		waitErr := p.cmd.Wait()
		if waitErr == nil {
			p.log.Info("ffmpeg finished", "source", p.src)

			return
		}
		if stopped || channelClosed(p.done) {
			p.log.Debug("ffmpeg stopped", "source", p.src)

			return
		}

		p.err = classifyProcessFailure(waitErr, p.stderr.String())
		p.log.Warn("ffmpeg exited", "source", p.src, "err", p.err)
	})

	return p.err
}

func channelClosed(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

type processError struct {
	cause  error
	reason string
}

func (e *processError) Error() string { return "ffmpeg: " + e.reason }

func (e *processError) Unwrap() error { return e.cause }

func classifyProcessFailure(cause error, stderr string) error {
	message := strings.ToLower(stderr)
	reason := "media process exited unexpectedly"

	switch {
	case strings.Contains(message, "permission denied"):
		reason = "media source permission denied"
	case strings.Contains(message, "address already in use"):
		reason = "media endpoint is already in use"
	case strings.Contains(message, "connection refused"):
		reason = "media endpoint refused the connection"
	case strings.Contains(message, "connection timed out"), strings.Contains(message, "i/o timeout"):
		reason = "media endpoint timed out"
	case strings.Contains(message, "matches no streams"), strings.Contains(message, "no audio stream"):
		reason = "media source has no usable audio stream"
	case strings.Contains(message, "invalid data found"):
		reason = "media source format is invalid"
	case strings.Contains(message, "no such file or directory"), strings.Contains(message, "device not found"):
		reason = "media source was not found"
	case strings.Contains(message, "input/output error"):
		reason = "media source I/O failed"
	}

	return &processError{cause: cause, reason: reason}
}

// tailBuffer keeps the last limit bytes written to it.
type tailBuffer struct {
	buf   []byte
	mu    sync.Mutex
	limit int
}

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.buf = append(t.buf, b...)
	if len(t.buf) > t.limit {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-t.limit:]...)
	}

	return len(b), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	return strings.TrimSpace(string(t.buf))
}
