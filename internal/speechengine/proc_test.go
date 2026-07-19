package speechengine

import (
	"errors"
	"os"
	"os/exec"
	"testing"
)

type countingCloser struct {
	err    error
	closes int
}

func (c *countingCloser) Write(b []byte) (int, error) { return len(b), nil }

func (c *countingCloser) Close() error {
	c.closes++

	return c.err
}

// closeInput is the helper's shutdown signal: it must close stdin exactly
// once and keep returning the first result on every later call.
func TestStdioProcCloseInputIsIdempotent(t *testing.T) {
	t.Parallel()

	closer := &countingCloser{err: errors.New("pipe already gone")}
	proc := &stdioProc{stdin: closer}

	first := proc.closeInput()
	second := proc.closeInput()
	if closer.closes != 1 {
		t.Fatalf("stdin closed %d times, want exactly once", closer.closes)
	}
	if !errors.Is(first, closer.err) || !errors.Is(second, closer.err) {
		t.Fatalf("closeInput results = %v / %v, want the first close error cached", first, second)
	}
}

// kill tolerates both an unstarted child and one that already exited.
func TestStdioProcKillToleratesMissingAndFinishedChildren(t *testing.T) {
	t.Parallel()

	unstarted := &stdioProc{}
	if err := unstarted.kill(); err != nil {
		t.Fatalf("kill without a process = %v, want nil", err)
	}

	cmd := exec.CommandContext(t.Context(), "true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	finished := &stdioProc{cmd: cmd}
	if err := finished.kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("kill after exit = %v, want done-tolerant nil", err)
	}
}
