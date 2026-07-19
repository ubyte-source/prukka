//go:build darwin || linux

package procio

import (
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
)

func TestPrepareTreeArmsANewProcessGroup(t *testing.T) {
	t.Parallel()

	cmd := &exec.Cmd{}
	PrepareTree(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatalf("SysProcAttr = %#v, want Setpgid so descendants share the child's group", cmd.SysProcAttr)
	}
}

func TestUnixTreeKillIsIdempotent(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	tree := &unixTree{
		pid: 42,
		signalGroup: func(pid int, signal syscall.Signal) error {
			if pid != -42 || signal != syscall.SIGKILL {
				t.Errorf("signal group = (%d, %d), want (-42, SIGKILL)", pid, signal)
			}
			calls.Add(1)

			return nil
		},
	}

	var callers sync.WaitGroup
	for range 32 {
		callers.Go(func() {
			if err := tree.Kill(); err != nil {
				t.Errorf("kill: %v", err)
			}
		})
	}
	callers.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("process-group signals = %d, want 1", got)
	}
}

// TestUnixTreeReleaseNeverSignals is the contract test for the half of
// procio.Tree that runs AFTER cmd.Wait. By then the leader is reaped and its
// process-group id is free — on a busy daemon the kernel may already have given
// that number to an unrelated group — so Release must free the handle without
// signaling, and must leave a later Kill silent rather than let it SIGKILL a
// stranger's group.
func TestUnixTreeReleaseNeverSignals(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	tree := &unixTree{
		pid: 42,
		signalGroup: func(int, syscall.Signal) error {
			calls.Add(1)

			return nil
		},
	}

	if err := tree.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := tree.Kill(); err != nil {
		t.Fatalf("kill after release: %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("process-group signals from the post-reap path = %d, want 0", got)
	}
}
