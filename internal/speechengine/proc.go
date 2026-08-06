package speechengine

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
)

// stdioProc is the shared lifecycle of a line-protocol helper child: one
// idempotent stdin close and a kill that treats an exited child as success.
type stdioProc struct {
	cmd            *exec.Cmd
	stdin          io.WriteCloser
	closeInputOnce func() error
}

// newStdioProc adopts a started child and its stdin pipe.
func newStdioProc(cmd *exec.Cmd, stdin io.WriteCloser) *stdioProc {
	p := &stdioProc{cmd: cmd, stdin: stdin}
	// A helper that exits on its own has its pipes closed by cmd.Wait first, so
	// an already-closed stdin is the expected outcome, not a failure.
	p.closeInputOnce = sync.OnceValue(func() error {
		if err := p.stdin.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			return err
		}

		return nil
	})

	return p
}

func (p *stdioProc) closeInput() error { return p.closeInputOnce() }

func (p *stdioProc) kill() error {
	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	err := p.cmd.Process.Kill()
	if err == nil || errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	// Windows releases the process handle during Wait, so killing an already
	// exited child reports EINVAL, not ErrProcessDone; ProcessState is the proof.
	if p.cmd.ProcessState != nil {
		return nil
	}

	return err
}
