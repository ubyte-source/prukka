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
	out     io.ReadCloser
	err     error
	cmd     *exec.Cmd
	log     *slog.Logger
	stderr  *procio.TailBuffer
	tree    procio.Tree
	done    <-chan struct{}
	src     string
	name    string
	once    sync.Once
	drained atomic.Bool
}

// backend names a supervised child in diagnostics. Only capture children
// carry a label — ffmpeg or the native miccapture helper — while mux and
// sink children are always the ffmpeg binary, so the zero value defaults to
// ffmpeg as the common case.
func (p *process) backend() string {
	if p.name == "" {
		return ffmpegName
	}

	return p.name
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

// reap runs the one ordering procio.Tree allows: signal the tree while the
// child is still unreaped, then Wait, then free the handle with the call that
// cannot signal.
func (p *process) reap(stop bool) error {
	p.once.Do(func() {
		stopped := stop && !p.drained.Load() && p.stopTree()
		waitErr := p.cmd.Wait()
		p.releaseTree()
		p.classifyExit(waitErr, stopped)
	})

	return p.err
}

// stopTree kills the child's whole process group — a Job Object on Windows —
// and reports whether the stop was ours. Killing the leader alone would leave
// a wrapper script's grandchild holding both the capture device and the
// inherited stdio pipes, so the next open fails and cmd.Wait blocks until the
// wait delay expires.
func (p *process) stopTree() bool {
	if err := p.tree.Kill(); err != nil {
		p.log.Debug(p.backend()+" kill", "source", p.src, "err", err)

		return false
	}

	return true
}

// releaseTree frees the tree handle once the child is reaped. Release is the
// only tree call that is legal here, and it runs on the drain path too, where
// nothing was killed: the handle must not leak, but a signal at this point
// would carry a process-group id — or a parent PID — the kernel already
// released and may have handed to an unrelated process.
func (p *process) releaseTree() {
	if err := p.tree.Release(); err != nil {
		p.log.Debug(p.backend()+" release process tree", "source", p.src, "err", err)
	}
}

func (p *process) classifyExit(waitErr error, stopped bool) {
	switch {
	case waitErr == nil:
		p.log.Info(p.backend()+" finished", "source", p.src)
	case stopped, channelClosed(p.done):
		p.log.Debug(p.backend()+" stopped", "source", p.src)
	case errors.Is(waitErr, exec.ErrWaitDelay):
		// os/exec reports the delay only after an otherwise clean exit: the
		// stream is complete and a descendant merely outlived the child holding
		// its pipes. A teardown breadcrumb, not a media failure.
		p.log.Warn(p.backend()+" left a descendant holding its pipes", "source", p.src)
	default:
		tail := p.stderr.String()
		p.err = classifyProcessFailure(waitErr, tail)
		p.log.Warn(p.backend()+" exited", "source", p.src, "err", p.err)
		// The classified reason is a fixed phrase; the child's own stderr names
		// the real cause. Keep it as a Debug breadcrumb (off by default) with
		// its URLs reduced by redact.Text, so an unmatched exit stays
		// diagnosable without leaking stream keys, userinfo or query secrets
		// into the logs.
		p.log.Debug(p.backend()+" stderr", "source", p.src, "tail", redact.Text(tail))
	}
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

// IsRetryableStartupFailure reports the narrow class of media-process exits
// that a local device may transiently return while its native capture format
// is being renegotiated. Callers must still restrict retries to device sources
// that have not delivered media: the same I/O text can describe a genuine
// terminal failure after capture has started.
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
