//go:build darwin

package ffmpeg

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const darwinCameraApp = "/Applications/Prukka Camera.app"

const nativeVideoReadyTimeout = 15 * time.Second

func nativeVideoFeeder() string {
	return filepath.Join(darwinCameraApp, "Contents", "MacOS", "prukka-camfeed")
}

func nativeVideoFeederInstalled() bool {
	info, err := os.Stat(nativeVideoFeeder())

	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

func nativeVideoAvailable(ctx context.Context) bool {
	if !nativeVideoFeederInstalled() {
		return false
	}

	probeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	return newCommand(probeCtx, nativeVideoFeeder(), []string{"--probe"}).Run() == nil
}

func (s *Supervisor) startNativeVideoDevice(ctx context.Context, playlist string) (<-chan error, error) {
	feeder := nativeVideoFeeder()
	if !nativeVideoFeederInstalled() {
		return nil, fmt.Errorf("camera feeder is not installed at %s", feeder)
	}

	cmd := newCommand(ctx, feeder, []string{playlist})
	stderr := &tailBuffer{limit: stderrTail}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open Prukka Camera feeder output: %w", err)
	}

	if err = cmd.Start(); err != nil {
		return nil, fmt.Errorf("start Prukka Camera feeder: %w", err)
	}

	ready := make(chan struct{}, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			ready <- struct{}{}
		}
		for scanner.Scan() {
		}
	}()

	done := make(chan error, 1)
	go func() {
		waitErr := cmd.Wait()
		if tail := stderr.String(); tail != "" {
			if waitErr == nil {
				waitErr = errors.New(tail)
			} else {
				waitErr = fmt.Errorf("%w: %s", waitErr, tail)
			}
		}
		done <- waitErr
		close(done)
	}()

	if readyErr := waitNativeVideoReady(ctx, cmd, ready, done); readyErr != nil {
		return nil, readyErr
	}

	s.log.Info("Prukka Camera feeder started", "pid", cmd.Process.Pid, "source", "hls")

	return done, nil
}

func waitNativeVideoReady(
	ctx context.Context, cmd *exec.Cmd, ready <-chan struct{}, done <-chan error,
) error {
	timer := time.NewTimer(nativeVideoReadyTimeout)
	defer timer.Stop()

	select {
	case <-ready:
		return nil
	case err := <-done:
		if err == nil {
			return errors.New("camera feeder exited during startup")
		}

		return fmt.Errorf("camera feeder startup: %w", err)
	case <-timer.C:
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("stop unresponsive camera feeder: %w", err)
		}

		return fmt.Errorf("camera feeder did not become ready within %s", nativeVideoReadyTimeout)
	case <-ctx.Done():
		return ctx.Err()
	}
}
