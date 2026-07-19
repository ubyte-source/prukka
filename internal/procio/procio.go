// Package procio holds the small I/O helpers shared by every package that
// supervises stdio child processes.
package procio

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// DefaultStderrTail bounds how much child stderr is retained for failure
// classification.
const DefaultStderrTail = 4 << 10

// TailBuffer keeps the last limit bytes written to it, so a child's final
// diagnostic survives no matter how much it printed before failing. A
// non-positive limit retains nothing.
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

// RunQuiet runs a prepared command, folding its output into any error.
func RunQuiet(cmd *exec.Cmd) error {
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", strings.Join(cmd.Args, " "), err, strings.TrimSpace(string(out)))
	}

	return nil
}
