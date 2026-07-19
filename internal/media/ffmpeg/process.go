package ffmpeg

import (
	"errors"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ubyte-source/prukka/internal/procio"
)

// process ties the PCM reader to the child's lifecycle; Wait must not run
// before the reader is done or buffered audio is lost.
type process struct {
	out     io.ReadCloser
	err     error
	cmd     *exec.Cmd
	log     *slog.Logger
	stderr  *procio.TailBuffer
	done    <-chan struct{}
	src     string
	name    string
	once    sync.Once
	drained atomic.Bool
}

// backend names the capture process in diagnostics, defaulting to ffmpeg for
// the zero value used by the process-lifecycle tests.
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

func (p *process) reap(stop bool) error {
	p.once.Do(func() {
		stop = stop && !p.drained.Load()
		stopped := false
		if stop && p.cmd.Process != nil {
			if err := p.cmd.Process.Kill(); err == nil {
				stopped = true
			} else if !errors.Is(err, os.ErrProcessDone) {
				p.log.Debug(p.backend()+" kill", "source", p.src, "err", err)
			}
		}

		waitErr := p.cmd.Wait()
		if waitErr == nil {
			p.log.Info(p.backend()+" finished", "source", p.src)

			return
		}
		if stopped || channelClosed(p.done) {
			p.log.Debug(p.backend()+" stopped", "source", p.src)

			return
		}

		tail := p.stderr.String()
		p.err = classifyProcessFailure(waitErr, tail)
		p.log.Warn(p.backend()+" exited", "source", p.src, "err", p.err)
		// The classified reason is a fixed phrase; the child's own stderr names
		// the real cause. Keep it as a Debug breadcrumb (off by default) with
		// URLs redacted, so an unmatched exit stays diagnosable without leaking
		// stream keys, userinfo or query secrets into the logs.
		p.log.Debug(p.backend()+" stderr", "source", p.src, "tail", redactMediaURLs(tail))
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

// mediaURLPattern matches URL-like tokens ffmpeg prints in its diagnostics.
var mediaURLPattern = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s'"]+`)

// redactMediaURLs strips credentials, paths and query strings from any
// URL-like tokens in free-form ffmpeg stderr, keeping only scheme://host[:port]
// so a diagnostic breadcrumb never carries a stream key, userinfo or query
// secret. A token that does not parse to a host is reduced to scheme://[redacted].
func redactMediaURLs(text string) string {
	return mediaURLPattern.ReplaceAllStringFunc(text, func(token string) string {
		u, err := url.Parse(token)
		if err != nil || u.Host == "" {
			scheme := token
			if before, _, ok := strings.Cut(token, "://"); ok {
				scheme = before
			}

			return scheme + "://[redacted]"
		}

		return u.Scheme + "://" + u.Host
	})
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
