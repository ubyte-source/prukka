//go:build darwin || linux || windows

package native

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/procio"
)

type orderedTree struct{ calls *[]string }

type shortWriteCloser struct{}

func (shortWriteCloser) Write(p []byte) (int, error) { return max(0, len(p)-1), nil }
func (shortWriteCloser) Close() error                { return nil }

func (t *orderedTree) Kill() error {
	*t.calls = append(*t.calls, "kill")

	return nil
}

func (t *orderedTree) Release() error {
	*t.calls = append(*t.calls, "release")

	return nil
}

// TestProcessTreeKillsBeforeWaitAndReleasesAfter pins procio.Tree's one legal
// order: the signal lands while the child PID is still reserved, and the
// handle is freed only once the reap has returned.
func TestProcessTreeKillsBeforeWaitAndReleasesAfter(t *testing.T) {
	t.Parallel()

	var calls []string
	tree := &orderedTree{calls: &calls}
	waitErr, treeErr := killTreeAndReap(tree, func() error {
		calls = append(calls, "wait")

		return nil
	})
	if waitErr != nil || treeErr != nil {
		t.Fatalf("killTreeAndReap = (%v, %v), want no errors", waitErr, treeErr)
	}
	if got := strings.Join(calls, ","); got != "kill,wait,release" {
		t.Fatalf("lifecycle order = %q, want kill,wait,release", got)
	}
}

func TestWarmProcessShortWriteWaitsForTerminalDiagnostic(t *testing.T) {
	const marker = "short-write helper diagnostic"

	stderr := procio.NewTailBuffer(procio.DefaultStderrTail)
	if _, err := stderr.Write([]byte(marker)); err != nil {
		t.Fatalf("write stderr marker: %v", err)
	}
	proc := &warmProcess{
		spawnedHelper: &spawnedHelper{
			stdin: shortWriteCloser{}, stdout: io.NopCloser(strings.NewReader("")),
			stderr: stderr, tree: inertProcessTree{},
		},
		log:  discardTestLogger(),
		stop: make(chan struct{}), done: make(chan struct{}), stage: "short-write helper",
	}
	go func() {
		<-proc.stop
		proc.exitErr = withHelperStderr(errors.New("short-write helper exited"), stderr)
		close(proc.done)
	}()

	err := proc.writeLine(context.Background(), []byte("request\n"))
	if !errors.Is(err, io.ErrShortWrite) || !strings.Contains(err.Error(), marker) {
		t.Fatalf("writeLine error = %v, want short write and terminal stderr", err)
	}
}

func TestAbortKillsDescendantProcess(t *testing.T) {
	proc, pid := startTestDescendant(t)
	if !descendantAlive(pid) {
		t.Fatalf("descendant %d exited before cancellation", pid)
	}

	proc.abort()
	waitDone(t, proc.done)
	waitDescendantExit(t, pid)
}
