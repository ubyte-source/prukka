//go:build darwin

package ffmpeg

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ubyte-source/prukka/internal/procio"
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
	stderr := procio.NewTailBuffer(procio.DefaultStderrTail)
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

	done := waitCommand(cmd, stderr)

	if readyErr := waitProcessReady(
		ctx, cmd, ready, done, nativeVideoReadyTimeout, "camera feeder",
	); readyErr != nil {
		return nil, readyErr
	}

	s.log.Info("Prukka Camera feeder started", "pid", cmd.Process.Pid, "source", "hls")

	return done, nil
}
