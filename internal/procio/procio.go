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

// Tree retires a started child together with every process it spawned — the
// only way to stop a wrapper script's grandchild, which inherited the stdio
// pipes and would otherwise keep cmd.Wait blocked long after its parent died.
//
// The two methods are not interchangeable, and that is the whole point of the
// split: the reap boundary is expressed in the type, so the illegal call
// cannot be written.
//
//   - Kill signals the tree. It addresses the child through numbers the kernel
//     recycles — a process-group id on unix, a recorded parent PID in the
//     Windows process walk — so it is legal ONLY while the child is unreaped.
//     Once cmd.Wait has returned, those numbers may already name a stranger.
//   - Release frees the handle and never signals. It is therefore the only call
//     that stays legal after the reap, and it makes every later Kill a
//     permanent no-op.
//
// Every caller follows one order: Kill (when the tree must die), then
// cmd.Wait, then Release. A tree whose Kill was skipped — the drain path, where
// the child exits on its own — is simply released; descendants that outlived it
// are what cmd.WaitDelay bounds, because no signal can safely reach them once
// the leader's identity is gone. Both methods are idempotent and safe for
// concurrent use.
type Tree interface {
	Kill() error
	Release() error
}

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
