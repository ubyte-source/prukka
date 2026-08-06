package ffmpeg

import (
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ubyte-source/prukka/internal/procio"
	"github.com/ubyte-source/prukka/internal/redact"
)

// process ties the PCM reader to the child's lifecycle; Wait must not run
// before the reader is done or buffered audio is lost.
type process struct {
	out      io.ReadCloser
	cmd      *exec.Cmd
	log      *slog.Logger
	stderr   *procio.TailBuffer
	tree     procio.Tree
	done     <-chan struct{}
	reapOnce func() error
	src      string
	name     string
	drained  atomic.Bool
}

// stopFirst retires a child its owner ends: signal the tree while the child is
// still unreaped, then wait.
func stopFirst(p *process) *process {
	p.reapOnce = sync.OnceValue(func() error {
		return p.finish(!p.drained.Load() && p.stopTree())
	})

	return p
}

// drainOnly retires a child that ends itself once its stdin is sealed;
// signaling it would race the drain it is in the middle of.
func drainOnly(p *process) *process {
	p.reapOnce = sync.OnceValue(func() error { return p.finish(false) })

	return p
}

// backend names a supervised child in diagnostics; only capture children set a
// label, so the zero value means ffmpeg.
func (p *process) backend() string {
	if p.name == "" {
		return ffmpegName
	}

	return p.name
}

func (p *process) Read(b []byte) (int, error) {
	n, err := p.out.Read(b)
	if errors.Is(err, io.EOF) {
		p.drained.Store(true)
	}

	return n, err
}

// Close reaps the child exactly once; callers close only after they finished
// reading.
func (p *process) Close() error { return p.reapOnce() }

func (p *process) finish(stopped bool) error {
	waitErr := p.cmd.Wait()
	p.releaseTree()

	return p.classifyExit(waitErr, stopped)
}

// stopTree kills the child's whole process group — a Job Object on Windows —
// and reports whether the stop was ours.
func (p *process) stopTree() bool {
	if err := p.tree.Kill(); err != nil {
		p.log.Debug(p.backend()+" kill", "source", p.src, "err", err)

		return false
	}

	return true
}

// releaseTree frees the tree handle once the child is reaped. Only Release is
// legal here: a signal would carry a process-group id the kernel already
// released and may have handed to an unrelated process.
func (p *process) releaseTree() {
	if err := p.tree.Release(); err != nil {
		p.log.Debug(p.backend()+" release process tree", "source", p.src, "err", err)
	}
}

func (p *process) classifyExit(waitErr error, stopped bool) error {
	switch {
	case waitErr == nil:
		p.log.Info(p.backend()+" finished", "source", p.src)
	case stopped, channelClosed(p.done):
		p.log.Debug(p.backend()+" stopped", "source", p.src)
	case errors.Is(waitErr, exec.ErrWaitDelay):
		// os/exec reports the delay only after an otherwise clean exit: a
		// descendant merely outlived the child holding its pipes.
		p.log.Warn(p.backend()+" left a descendant holding its pipes", "source", p.src)
	default:
		tail := p.stderr.String()
		err := classifyProcessFailure(waitErr, tail)
		p.log.Warn(p.backend()+" exited", "source", p.src, "err", err)
		p.log.Debug(p.backend()+" stderr", "source", p.src, "tail", redact.Text(tail))

		return err
	}

	return nil
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
	cause            error
	reason           string
	retryableStartup bool
}

func (e *processError) Error() string { return e.reason }

func (e *processError) Unwrap() error { return e.cause }

// IsRetryableStartupFailure reports the exits a local device may transiently
// return while renegotiating its capture format; callers must still restrict
// retries to device sources that have not yet delivered media.
func IsRetryableStartupFailure(err error) bool {
	var processErr *processError

	return errors.As(err, &processErr) && processErr.retryableStartup
}

func classifyProcessFailure(cause error, stderr string) error {
	message := strings.ToLower(stderr)
	reason := "media process exited unexpectedly"
	retryableStartup := false

	switch {
	case strings.Contains(message, "permission denied"), strings.Contains(message, "not authorized"):
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
	case strings.Contains(message, "audio format is not supported"):
		reason = "media device audio format is temporarily unavailable"
		retryableStartup = true
	case strings.Contains(message, "input/output error"), strings.Contains(message, "i/o error"):
		reason = "media source I/O failed"
		retryableStartup = true
	}

	return &processError{cause: cause, reason: reason, retryableStartup: retryableStartup}
}
