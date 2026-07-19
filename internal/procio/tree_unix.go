//go:build darwin || linux

package procio

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// unixTree retires a child through its own process group, so a wrapper script
// that forks rather than execs takes its grandchildren down with it.
type unixTree struct {
	killErr     error
	process     *os.Process
	signalGroup func(int, syscall.Signal) error
	mu          sync.Mutex
	pid         int
	killed      bool
	released    bool
}

// PrepareTree makes the child the leader of its own process group. Call it
// before Start; the child's own descendants inherit the group.
func PrepareTree(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// AttachTree binds the started child's process group. Call it after Start.
func AttachTree(cmd *exec.Cmd) (Tree, error) {
	return &unixTree{
		process: cmd.Process, signalGroup: syscall.Kill, pid: cmd.Process.Pid,
	}, nil
}

func (t *unixTree) Kill() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.released || t.killed {
		return t.killErr
	}
	t.killed = true
	t.killErr = t.killProcessGroup()

	return t.killErr
}

// Release frees nothing on this platform and signals nothing either: a process
// group is a NUMBER, not a handle, and once the leader is reaped that number is
// free — the kernel may already have given it to somebody else's group. All
// Release can honestly do is latch the tree, so a later Kill stays silent
// instead of SIGKILLing a stranger.
func (t *unixTree) Release() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.released = true

	return nil
}

func (t *unixTree) killProcessGroup() error {
	err := t.signalGroup(-t.pid, syscall.SIGKILL)
	if err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}

	directErr := t.process.Kill()
	processDone := errors.Is(directErr, os.ErrProcessDone)
	if processDone {
		directErr = nil
	}
	if errors.Is(err, syscall.EPERM) && directErr == nil {
		// Darwin can report EPERM for a group whose only member is an exited,
		// unreaped leader. A same-user live descendant would make the group
		// signal succeed; the direct result confirms the root is retired.
		return nil
	}

	return errors.Join(fmt.Errorf("kill process group %d: %w", t.pid, err), directErr)
}
