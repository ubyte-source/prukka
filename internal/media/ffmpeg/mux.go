package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

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
	args := argv(quietArgs,
		s16le(pipeline.SampleRate, 1), []string{flagInput, pipeIn},
		[]string{"-c:a", "aac", "-b:a", "128k", flagFormat, "mpegts", pipeOut})

	// The one package authorized to exec.
	cmd := newCommand(ctx, s.bin, args)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mux stdin: %w", err)
	}

	stdout, outErr := cmd.StdoutPipe()
	if outErr != nil {
		return nil, fmt.Errorf("mux stdout: %w", outErr)
	}

	stderr := &tailBuffer{limit: stderrTail}
	cmd.Stderr = stderr

	if startErr := cmd.Start(); startErr != nil {
		return nil, fmt.Errorf("start mux: %w", startErr)
	}

	s.log.Info("ffmpeg mux started", "pid", cmd.Process.Pid)

	proc := &process{cmd: cmd, out: stdout, log: s.log, stderr: stderr, src: "mux"}

	return &Mux{In: stdin, Out: proc, close: proc.Close}, nil
}

// sink wraps the encoder's stdin: closing it drains the child and reaps it.
type sink struct {
	in   io.WriteCloser
	proc *process
}

// Write implements io.Writer.
func (s *sink) Write(b []byte) (int, error) {
	return s.in.Write(b)
}

// Close drains and reaps the encoder.
func (s *sink) Close() error {
	inErr := s.in.Close()

	return errors.Join(inErr, s.proc.Close())
}

// StartSink launches an encoder toward an external target; the caller
// feeds reference PCM and closes to stop.
func (s *Supervisor) StartSink(ctx context.Context, output []string) (io.WriteCloser, error) {
	args := argv(quietArgs, s16le(pipeline.SampleRate, 1), []string{flagInput, pipeIn}, output)

	cmd := newCommand(ctx, s.bin, args)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("sink stdin: %w", err)
	}

	stderr := &tailBuffer{limit: stderrTail}
	cmd.Stderr = stderr

	if startErr := cmd.Start(); startErr != nil {
		return nil, fmt.Errorf("start sink: %w", startErr)
	}

	s.log.Info("ffmpeg sink started", "pid", cmd.Process.Pid, "output", output[len(output)-1])

	return &sink{in: stdin, proc: &process{cmd: cmd, out: nopReader{}, log: s.log, stderr: stderr, src: "sink"}}, nil
}

// StartAVSink pairs the live video playlist with dub PCM on stdin (vf is
// the optional burn-in filter).
func (s *Supervisor) StartAVSink(
	ctx context.Context, videoPlaylist, vf string, output []string,
) (io.WriteCloser, error) {
	filter := []string{}
	if vf != "" {
		filter = []string{"-vf", vf}
	}

	// -shortest ends the push with the video: a live source never ends, and
	// a finite one must not leave an audio-only tail running forever.
	args := argv(quietArgs,
		[]string{"-re", flagInput, videoPlaylist},
		s16le(pipeline.SampleRate, 1), []string{flagInput, pipeIn},
		[]string{flagMap, "0:v:0", flagMap, "1:a:0", "-shortest"},
		filter, output)

	cmd := newCommand(ctx, s.bin, args)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("av sink stdin: %w", err)
	}

	stderr := &tailBuffer{limit: stderrTail}
	cmd.Stderr = stderr

	if startErr := cmd.Start(); startErr != nil {
		return nil, fmt.Errorf("start av sink: %w", startErr)
	}

	s.log.Info("ffmpeg av sink started", "pid", cmd.Process.Pid, "video", videoPlaylist,
		"burn", vf != "", "output", output[len(output)-1])

	return &sink{in: stdin, proc: &process{cmd: cmd, out: nopReader{}, log: s.log, stderr: stderr, src: "av sink"}}, nil
}

// nopReader satisfies the process reader when there is no stdout to drain.
type nopReader struct{}

// Read implements io.Reader.
func (nopReader) Read([]byte) (int, error) { return 0, io.EOF }

// Close implements io.Closer.
func (nopReader) Close() error { return nil }
