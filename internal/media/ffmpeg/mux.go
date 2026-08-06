package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/ubyte-source/prukka/internal/procio"
)

// sinkDrainTimeout bounds a sealed sink encoder's flush before it is killed.
const sinkDrainTimeout = 5 * time.Second

// OutputArgs returns a low-latency mux selection ending in target; the mpegts
// muxer's default ~0.7s interleave buffer would otherwise hold a live take.
func OutputArgs(format, target string) []string {
	args := []string{}
	if format == "mpegts" {
		args = append(args, "-muxdelay", "0", "-muxpreload", "0", "-flush_packets", "1")
	}

	return append(args, flagFormat, format, target)
}

// Mux is one running PCM→MPEG-TS encoder; closing In drains and ends Out.
type Mux struct {
	In    io.WriteCloser
	Out   io.ReadCloser
	close func() error
}

// Close stops the encoder.
func (m *Mux) Close() error {
	return m.close()
}

// StartMux launches the AAC/MPEG-TS encoder for one output stream.
func (s *Supervisor) StartMux(ctx context.Context) (*Mux, error) {
	args := argv(quietArgs,
		s16le(), []string{flagInput, pipeIn},
		[]string{flagAudioCodec, "aac", "-b:a", "128k"},
		OutputArgs("mpegts", pipeOut))

	cmd := newCommand(ctx, s.bin, args)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mux stdin: %w", err)
	}

	stdout, outErr := cmd.StdoutPipe()
	if outErr != nil {
		// Start never runs, so exec's own pipe cleanup won't; close stdin here
		// or its fds leak until finalization.
		return nil, errors.Join(fmt.Errorf("mux stdout: %w", outErr), stdin.Close())
	}

	stderr := procio.NewTailBuffer(procio.DefaultStderrTail)
	cmd.Stderr = stderr

	tree, startErr := startChild(cmd, s.log, "mux")
	if startErr != nil {
		return nil, startErr
	}

	s.log.Info("ffmpeg mux started", "pid", cmd.Process.Pid)

	proc := stopFirst(&process{
		cmd:    cmd,
		out:    stdout,
		log:    s.log,
		stderr: stderr,
		tree:   tree,
		src:    "mux",
		done:   ctx.Done(),
	})

	return &Mux{In: stdin, Out: proc, close: proc.Close}, nil
}

// sink wraps the encoder's stdin: closing it drains the child and reaps it.
type sink struct {
	in        io.WriteCloser
	proc      *process
	closeOnce func() error
}

func newSink(in io.WriteCloser, proc *process) *sink {
	s := &sink{in: in, proc: proc}
	s.closeOnce = sync.OnceValue(s.drain)

	return s
}

func (s *sink) Write(b []byte) (int, error) {
	return s.in.Write(b)
}

// Close seals stdin so the encoder drains and exits, then reaps it.
func (s *sink) Close() error { return s.closeOnce() }

// drain bounds the wait, then kills the whole process group: the child is
// drainOnly, so its own reap never kills and Close would hang on a wedged
// encoder.
func (s *sink) drain() error {
	sealErr := s.in.Close()
	done := make(chan error, 1)
	go func() { done <- s.proc.Close() }()
	select {
	case err := <-done:
		return errors.Join(sealErr, err)
	case <-time.After(sinkDrainTimeout):
		killErr := s.proc.tree.Kill()
		<-done // let the reaping goroutine observe the kill and return

		return errors.Join(sealErr, killErr)
	}
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

	// -shortest ends the push with the video, so a finite source leaves no
	// audio-only tail behind.
	args := argv(quietArgs,
		[]string{flagRealtime, flagInput, videoPlaylist},
		s16le(), []string{flagInput, pipeIn},
		[]string{flagMap, mapFirstVideo, flagMap, mapSecondAudio, "-shortest"},
		filter, output)

	return s.startSink(ctx, args, "av sink",
		"video", "hls", "burn", vf != "", "output", endpointLabel(target))
}

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

	tree, startErr := startChild(cmd, s.log, src)
	if startErr != nil {
		return nil, startErr
	}

	s.log.Info("ffmpeg "+src+" started", append([]any{"pid", cmd.Process.Pid}, logAttrs...)...)

	return newSink(stdin, drainOnly(&process{
		cmd:    cmd,
		out:    nopReader{},
		log:    s.log,
		stderr: stderr,
		tree:   tree,
		src:    src,
		done:   ctx.Done(),
	})), nil
}

// nopReader satisfies the process reader when there is no stdout to drain.
type nopReader struct{}

func (nopReader) Read([]byte) (int, error) { return 0, io.EOF }

func (nopReader) Close() error { return nil }
