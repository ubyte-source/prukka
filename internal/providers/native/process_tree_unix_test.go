//go:build darwin || linux

package native

import (
	"errors"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
)

func descendantAlive(pid int) bool {
	err := syscall.Kill(pid, 0)

	return err == nil || errors.Is(err, syscall.EPERM)
}

func TestUnixProcessTreeKillIsIdempotent(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	tree := &unixProcessTree{
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
		callers.Add(2)
		go func() {
			defer callers.Done()
			if err := tree.kill(); err != nil {
				t.Errorf("kill: %v", err)
			}
		}()
		go func() {
			defer callers.Done()
			if err := tree.close(); err != nil {
				t.Errorf("close: %v", err)
			}
		}()
	}
	callers.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("process-group signals = %d, want 1", got)
	}
}
