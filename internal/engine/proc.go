package engine

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
)

// stdioProc is the shared lifecycle of a line-protocol helper child: one
// idempotent stdin close (the helper's shutdown signal) and a kill that
// treats an already-exited child as success.
type stdioProc struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdinErr  error
	stdinOnce sync.Once
}

func (p *stdioProc) closeInput() error {
	p.stdinOnce.Do(func() { p.stdinErr = p.stdin.Close() })

	return p.stdinErr
}

func (p *stdioProc) kill() error {
	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	err := p.cmd.Process.Kill()
	if err == nil || errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	// Windows releases the process handle during Wait, so killing a child that
	// already exited reports EINVAL ("invalid argument") instead of
	// ErrProcessDone; a set ProcessState is the portable proof it is gone.
	if p.cmd.ProcessState != nil {
		return nil
	}

	return err
}
