// Package procio holds the small I/O helpers shared by every package that
// supervises stdio child processes.
package procio

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/ubyte-source/prukka/internal/redact"
)

// DefaultStderrTail bounds how much child stderr is retained for failure
// classification.
const DefaultStderrTail = 4 << 10

// Tree retires a started child together with every process it spawned. Kill
// addresses the child through numbers the kernel recycles, so it is legal only
// while the child is unreaped; Release never signals and is the only call that
// stays legal after cmd.Wait, after which it makes every Kill a no-op. Callers
// order them Kill, cmd.Wait, Release. Both are idempotent and safe for
// concurrent use.
type Tree interface {
	Kill() error
	Release() error
}

// TailBuffer keeps the last limit bytes written to it; a non-positive limit
// retains nothing.
type TailBuffer struct {
	buf   []byte
	mu    sync.Mutex
	limit int
}

// NewTailBuffer bounds a tail at limit bytes.
func NewTailBuffer(limit int) *TailBuffer {
	return &TailBuffer{limit: limit}
}

func (t *TailBuffer) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.limit <= 0 {
		return len(b), nil
	}
	if len(b) >= t.limit {
		t.buf = append(t.buf[:0], b[len(b)-t.limit:]...)

		return len(b), nil
	}

	t.buf = append(t.buf, b...)
	if len(t.buf) > t.limit {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-t.limit:]...)
	}

	return len(b), nil
}

// String returns the retained tail with surrounding whitespace trimmed.
func (t *TailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	return strings.TrimSpace(string(t.buf))
}

// StderrError explains a child's failure with the stderr tail that accompanied
// it. The tail is the child's prose, not this program's, so the error names it
// as untrusted and a bound renderer drops that exact span.
type StderrError struct {
	cause error
	tail  string
}

// WithStderr attaches a child's stderr tail to cause; an empty tail leaves
// cause unchanged.
func WithStderr(cause error, tail string) error {
	if tail == "" {
		return cause
	}

	var declared redact.Untrusted = &StderrError{cause: cause, tail: tail}

	return declared
}

func (e *StderrError) Error() string {
	if e.cause == nil {
		return "stderr: " + e.tail
	}

	return e.cause.Error() + "; stderr: " + e.tail
}

func (e *StderrError) Unwrap() error { return e.cause }

// Untrusted implements redact.Untrusted with the child's own output.
func (e *StderrError) Untrusted() string { return e.tail }

// RunQuiet runs a prepared command, folding its output into any error.
func RunQuiet(cmd *exec.Cmd) error {
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", strings.Join(cmd.Args, " "), err, strings.TrimSpace(string(out)))
	}

	return nil
}
