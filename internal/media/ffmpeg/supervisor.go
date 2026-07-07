package ffmpeg

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

// stderrTail bounds how much of ffmpeg's stderr is kept for error reports.
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
	flagInput  = "-i"
	flagFormat = "-f"
	flagMap    = "-map"
	pipeIn     = "pipe:0"
	pipeOut    = "pipe:1"
)

// quietArgs is the shared invocation prefix: no banner, no stdin, errors
// only. Package-level immutable data.
var quietArgs = []string{"-hide_banner", "-nostdin", "-loglevel", "error"}

// s16le describes a raw PCM stream of the given format — the one place the
// s16le triplet is spelled.
func s16le(rate, channels int) []string {
	return []string{flagFormat, "s16le", "-ar", strconv.Itoa(rate), "-ac", strconv.Itoa(channels)}
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
	var input []string

	switch {
	case strings.HasPrefix(src, "rtmp://"):
		input = []string{"-listen", "1", flagInput, src}
	case strings.HasPrefix(src, "srt://") && !strings.Contains(src, "mode="):
		input = []string{flagInput, src + listenQuery(src)}
	case strings.HasPrefix(src, "srt://"):
		input = []string{flagInput, src}
	case IsDeviceURL(src):
		// Device capture is inherently live; a bad id fails at start and
		// is reported through the process's stderr tail.
		if deviceInput, err := deviceInputArgs(src); err == nil {
			input = deviceInput
		} else {
			input = []string{flagInput, src}
		}
	default:
		input = []string{"-re", flagInput, src}
	}

	args := argv(quietArgs, input,
		[]string{flagMap, "0:a:0", "-vn", "-sn", "-dn"},
		s16le(pipeline.SampleRate, 1), []string{pipeOut})

	if videoDir != "" {
		args = argv(args, hlsVideoArgs(videoDir, delay))
	}

	return args
}

// hlsVideoArgs is the passthrough HLS video output: no re-encode, shifted
// by delay D onto the shared clock.
func hlsVideoArgs(dir string, delay time.Duration) []string {
	return HLSOutput(dir, delay, flagMap, "0:v:0?", "-c:v", "copy")
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

	s.log.Info("ffmpeg started", "source", src, "pid", cmd.Process.Pid)

	return &process{cmd: cmd, out: stdout, log: s.log, stderr: stderr, src: src}, nil
}

// process ties the PCM reader to the child's lifecycle; Wait must not run
// before the reader is done or buffered audio is lost.
type process struct {
	cmd    *exec.Cmd
	out    io.ReadCloser
	log    *slog.Logger
	stderr *tailBuffer
	src    string
	once   sync.Once
}

// Read implements io.Reader over the PCM pipe; it reaches EOF once the
// child exits and the pipe drains.
func (p *process) Read(b []byte) (int, error) {
	return p.out.Read(b)
}

// Close stops a still-running child, reaps it exactly once and reports how
// it ended. Callers close only after they finished reading.
func (p *process) Close() error {
	p.once.Do(func() {
		if p.cmd.Process != nil {
			// A child that already exited makes this a logged no-op.
			if err := p.cmd.Process.Kill(); err != nil {
				p.log.Debug("ffmpeg kill", "source", p.src, "err", err)
			}
		}

		err := p.cmd.Wait()

		tail := p.stderr.String()

		switch {
		case err == nil:
			p.log.Info("ffmpeg finished", "source", p.src)
		case tail != "":
			p.log.Warn("ffmpeg exited", "source", p.src, "err", err, "stderr", tail)
		default:
			p.log.Warn("ffmpeg exited", "source", p.src, "err", err)
		}
	})

	return nil
}

// tailBuffer keeps the last limit bytes written to it.
type tailBuffer struct {
	buf   []byte
	mu    sync.Mutex
	limit int
}

// Write implements io.Writer.
func (t *tailBuffer) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.buf = append(t.buf, b...)
	if len(t.buf) > t.limit {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-t.limit:]...)
	}

	return len(b), nil
}

// String returns the buffered tail.
func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	return strings.TrimSpace(string(t.buf))
}
