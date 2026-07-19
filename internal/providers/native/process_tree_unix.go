//go:build darwin || linux

package native

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

type unixProcessTree struct {
	killErr     error
	process     *os.Process
	signalGroup func(int, syscall.Signal) error
	killOnce    sync.Once
	pid         int
}

func prepareProcessTree(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func attachProcessTree(cmd *exec.Cmd) (processTree, error) {
	return &unixProcessTree{
		process: cmd.Process, signalGroup: syscall.Kill, pid: cmd.Process.Pid,
	}, nil
}

func (t *unixProcessTree) kill() error {
	t.killOnce.Do(func() { t.killErr = t.killProcessGroup() })

	return t.killErr
}

func (t *unixProcessTree) killProcessGroup() error {
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

func (t *unixProcessTree) close() error { return t.kill() }
