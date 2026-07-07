//go:build !darwin && !linux && !windows

package native

import (
	"errors"
	"os"
	"os/exec"
	"sync"
)

type directProcessTree struct {
	process *os.Process
	killErr error
	once    sync.Once
}

func prepareProcessTree(*exec.Cmd) {}

func attachProcessTree(cmd *exec.Cmd) (processTree, error) {
	return &directProcessTree{process: cmd.Process}, nil
}

func (t *directProcessTree) kill() error {
	t.once.Do(func() {
		t.killErr = t.process.Kill()
		if errors.Is(t.killErr, os.ErrProcessDone) {
			t.killErr = nil
		}
	})

	return t.killErr
}

func (t *directProcessTree) close() error { return t.kill() }
