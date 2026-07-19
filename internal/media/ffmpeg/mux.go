package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/ubyte-source/prukka/internal/procio"
)

// sinkDrainTimeout bounds how long a sealed sink encoder may take to flush its
// last packets and exit before it is force-killed, so Close cannot hang on a
// child that never drains (a stuck external target, a wedged mpegts flush).
const sinkDrainTimeout = 5 * time.Second

// OutputArgs returns a low-latency mux selection ending in target. MPEG-TS
// needs explicit zero delay/preload and packet flushing; its defaults can hold
// a conversational SRT take for roughly another second.
func OutputArgs(format, target string) []string {
	args := []string{}
	if format == "mpegts" {
		args = append(args, "-muxdelay", "0", "-muxpreload", "0", "-flush_packets", "1")
	}

	return append(args, flagFormat, format, target)
}

// Mux is one running PCM→MPEG-TS encoder: write reference PCM bytes in,
// read a transport stream out. Closing In drains and ends Out.
type Mux struct {
	In  io.WriteCloser
	Out io.ReadCloser
	// close tears the process down; wired by StartMux.
	close func() error
}

// Close stops the encoder.
func (m *Mux) Close() error {
	return m.close()
}

// StartMux launches the AAC/MPEG-TS encoder for one output stream
// (/{session}/{lang}/audio.ts is an audio-only transport stream).
func (s *Supervisor) StartMux(ctx context.Context) (*Mux, error) {
	// -muxdelay/-muxpreload 0 and -flush_packets 1 drop the mpegts muxer's
	// default ~0.7s interleave buffer so a dubbed clause reaches the transport
	// (and thus a live device/OBS) as soon as it is encoded, not up to a
	// second later. The cost is smaller, more frequent TS packets.
	args := argv(quietArgs,
		s16le(), []string{flagInput, pipeIn},
		[]string{flagAudioCodec, "aac", "-b:a", "128k"},
		OutputArgs("mpegts", pipeOut))

	// The one package authorized to exec.
	cmd := newCommand(ctx, s.bin, args)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mux stdin: %w", err)
	}

	stdout, outErr := cmd.StdoutPipe()
	if outErr != nil {
		// Start never runs, so exec's own pipe cleanup won't; close the stdin
		// pipe here or its fds leak until finalization (matches process.go).
		return nil, errors.Join(fmt.Errorf("mux stdout: %w", outErr), stdin.Close())
	}

	stderr := procio.NewTailBuffer(procio.DefaultStderrTail)
	cmd.Stderr = stderr

	if startErr := cmd.Start(); startErr != nil {
		return nil, fmt.Errorf("start mux: %w", startErr)
	}

	s.log.Info("ffmpeg mux started", "pid", cmd.Process.Pid)

	proc := &process{
		cmd: cmd, out: stdout, log: s.log, stderr: stderr, src: "mux", done: ctx.Done(),
	}

	return &Mux{In: stdin, Out: proc, close: proc.Close}, nil
}

// sink wraps the encoder's stdin: closing it drains the child and reaps it.
type sink struct {
	in       io.WriteCloser
	proc     *process
	closeErr error
	once     sync.Once
}

func (s *sink) Write(b []byte) (int, error) {
	return s.in.Write(b)
}

// Close seals stdin so the encoder drains and exits, then reaps it with a
// bounded wait: if the child does not finish within sinkDrainTimeout it is
// force-killed directly (proc.wait's reap is once-guarded and never kills on
// the drain path, so Close would otherwise hang on a wedged encoder).
func (s *sink) Close() error {
	s.once.Do(func() {
		sealErr := s.in.Close()
		done := make(chan error, 1)
		go func() { done <- s.proc.wait() }()
		select {
		case err := <-done:
			s.closeErr = errors.Join(sealErr, err)
		case <-time.After(sinkDrainTimeout):
			killErr := s.proc.cmd.Process.Kill()
			if errors.Is(killErr, os.ErrProcessDone) {
				killErr = nil
			}
			<-done // let the reaping goroutine observe the kill and return
			s.closeErr = errors.Join(sealErr, killErr)
		}
	})

	return s.closeErr
}

// StartSink launches an encoder toward an external target; the caller
// feeds reference PCM and closes to stop.
func (s *Supervisor) StartSink(ctx context.Context, output []string) (io.WriteCloser, error) {
	if len(output) == 0 {
		return nil, errors.New("sink output: required")
	}
	target := output[len(output)-1]
	args := argv(quietArgs, s16le(), []string{flagInput, pipeIn}, output)

	return s.startSink(ctx, args, "sink", "output", endpointLabel(target))
}

// StartAVSink pairs the live video playlist with dub PCM on stdin (vf is
// the optional burn-in filter).
func (s *Supervisor) StartAVSink(
	ctx context.Context, videoPlaylist, vf string, output []string,
) (io.WriteCloser, error) {
	if len(output) == 0 {
		return nil, errors.New("av sink output: required")
	}
	target := output[len(output)-1]
	filter := []string{}
	if vf != "" {
		filter = []string{"-vf", vf}
	}

	// -shortest ends the push with the video: a live source never ends, and
	// a finite one must not leave an audio-only tail running forever.
	args := argv(quietArgs,
		[]string{flagRealtime, flagInput, videoPlaylist},
		s16le(), []string{flagInput, pipeIn},
		[]string{flagMap, mapFirstVideo, flagMap, mapSecondAudio, "-shortest"},
		filter, output)

	return s.startSink(ctx, args, "av sink",
		"video", "hls", "burn", vf != "", "output", endpointLabel(target))
}

// startSink spawns one PCM-fed encoder child and wraps its stdin as the
// drain-on-close sink; src labels the child in diagnostics and logAttrs
// carry the per-shape log fields.
func (s *Supervisor) startSink(
	ctx context.Context, args []string, src string, logAttrs ...any,
) (io.WriteCloser, error) {
	cmd := newCommand(ctx, s.bin, args)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("%s stdin: %w", src, err)
	}

	stderr := procio.NewTailBuffer(procio.DefaultStderrTail)
	cmd.Stderr = stderr

	if startErr := cmd.Start(); startErr != nil {
		return nil, fmt.Errorf("start %s: %w", src, startErr)
	}

	s.log.Info("ffmpeg "+src+" started", append([]any{"pid", cmd.Process.Pid}, logAttrs...)...)

	return &sink{in: stdin, proc: &process{
		cmd: cmd, out: nopReader{}, log: s.log, stderr: stderr, src: src, done: ctx.Done(),
	}}, nil
}

// nopReader satisfies the process reader when there is no stdout to drain.
type nopReader struct{}

func (nopReader) Read([]byte) (int, error) { return 0, io.EOF }

func (nopReader) Close() error { return nil }
