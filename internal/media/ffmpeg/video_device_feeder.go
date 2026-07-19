//go:build darwin || windows

package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/ubyte-source/prukka/internal/procio"
)

// waitCommand reaps one feeder child in the background, folding its stderr
// tail into the terminal error.
func waitCommand(cmd *exec.Cmd, stderr *procio.TailBuffer) <-chan error {
	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		if tail := tailOf(stderr); tail != "" {
			if err == nil {
				err = errors.New(tail)
			} else {
				err = fmt.Errorf("%w: %s", err, tail)
			}
		}
		done <- err
		close(done)
	}()

	return done
}

// waitProcessReady blocks until the feeder prints its readiness line, exits,
// times out (killing the unresponsive child) or the context ends.
func waitProcessReady(
	ctx context.Context, cmd *exec.Cmd, ready <-chan struct{}, done <-chan error,
	timeout time.Duration, name string,
) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ready:
		return nil
	case err := <-done:
		if err == nil {
			return fmt.Errorf("%s exited during startup", name)
		}

		return fmt.Errorf("%s startup: %w", name, err)
	case <-timer.C:
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("stop unresponsive %s: %w", name, err)
		}

		return fmt.Errorf("%s did not become ready within %s", name, timeout)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// tailOf reads a possibly-nil stderr tail once.
func tailOf(stderr *procio.TailBuffer) string {
	if stderr == nil {
		return ""
	}

	return stderr.String()
}
