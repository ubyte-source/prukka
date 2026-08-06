package speechengine

import (
	"errors"
	"io"
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

// closeInput must close stdin once and keep returning the first result.
func TestStdioProcCloseInputIsIdempotent(t *testing.T) {
	t.Parallel()

	closer := &countingCloser{err: errors.New("pipe already gone")}
	proc := newStdioProc(nil, closer)

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

	unstarted := newStdioProc(nil, nil)
	if err := unstarted.kill(); err != nil {
		t.Fatalf("kill without a process = %v, want nil", err)
	}

	// Re-execute this test binary matching no tests: an immediate clean exit on
	// every OS, with no dependency on a host `true` being on PATH.
	//nolint:gosec // Re-executes only the trusted test binary with a constant argument.
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^$")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	finished := newStdioProc(cmd, nil)
	if err := finished.kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("kill after exit = %v, want done-tolerant nil", err)
	}
}
