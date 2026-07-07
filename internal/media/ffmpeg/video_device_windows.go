//go:build windows

package ffmpeg

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const windowsCameraController = "PrukkaWebcamCtl.exe"

func nativeVideoController() string {
	root := os.Getenv("ProgramData")
	if root == "" {
		root = `C:\ProgramData`
	}

	return filepath.Join(root, "Prukka", "devices", "webcam", windowsCameraController)
}

func nativeVideoAvailable(ctx context.Context) bool {
	controller := nativeVideoController()
	if info, err := os.Stat(controller); err != nil || info.IsDir() {
		return false
	}

	probeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	return newCommand(probeCtx, controller, []string{"probe"}).Run() == nil
}

func (s *Supervisor) startNativeVideoDevice(ctx context.Context, playlist string) (<-chan error, error) {
	controller := nativeVideoController()
	if info, err := os.Stat(controller); err != nil || info.IsDir() {
		return nil, fmt.Errorf("camera controller is not installed at %s", controller)
	}

	procCtx, cancel := context.WithCancel(ctx)
	feed := newCommand(procCtx, controller, []string{"feed"})
	feedErr := &tailBuffer{limit: stderrTail}
	feed.Stderr = feedErr
	ready, feedDone, input, err := startWindowsCameraFeed(feed, feedErr)
	if err != nil {
		cancel()

		return nil, err
	}

	if err = waitWindowsCameraReady(procCtx, feed, ready, feedDone); err != nil {
		cancel()

		return nil, err
	}

	encoder := newCommand(procCtx, s.bin, windowsCameraArgs(playlist))
	encoderErr := &tailBuffer{limit: stderrTail}
	encoder.Stderr = encoderErr
	encoder.Stdout = input
	if err = encoder.Start(); err != nil {
		cancel()

		return nil, errors.Join(fmt.Errorf("start webcam encoder: %w", err), input.Close())
	}
	encoderDone := waitCommand(encoder, encoderErr)
	if err = input.Close(); err != nil {
		cancel()

		return nil, fmt.Errorf("close parent webcam pipe: %w", err)
	}

	done := combineVideoProcesses(procCtx, cancel, feedDone, encoderDone)
	s.log.Info("Prukka Webcam feeder started", "pid", feed.Process.Pid, "playlist", playlist)

	return done, nil
}

func windowsCameraArgs(playlist string) []string {
	return argv(quietArgs,
		[]string{flagRealtime, flagInput, playlist, "-an", "-vf", "scale=1280:720,fps=30"},
		[]string{"-pix_fmt", "yuyv422", flagFormat, "rawvideo", pipeOut})
}

func startWindowsCameraFeed(
	cmd *exec.Cmd, stderr *tailBuffer,
) (ready <-chan struct{}, done <-chan error, input io.WriteCloser, err error) {
	input, err = cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open webcam controller input: %w", err)
	}
	stdout, pipeErr := cmd.StdoutPipe()
	if pipeErr != nil {
		return nil, nil, nil, fmt.Errorf("open webcam controller output: %w", pipeErr)
	}
	if err = cmd.Start(); err != nil {
		return nil, nil, nil, fmt.Errorf("start webcam controller: %w", err)
	}

	readyChan := make(chan struct{}, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() && scanner.Text() == "ready" {
			readyChan <- struct{}{}
		}
		for scanner.Scan() {
		}
	}()

	return readyChan, waitCommand(cmd, stderr), input, nil
}

func waitWindowsCameraReady(
	ctx context.Context, cmd *exec.Cmd, ready <-chan struct{}, done <-chan error,
) error {
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()

	select {
	case <-ready:
		return nil
	case err := <-done:
		if err == nil {
			return errors.New("camera controller exited during startup")
		}

		return fmt.Errorf("camera controller startup: %w", err)
	case <-timer.C:
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("stop unresponsive camera controller: %w", err)
		}

		return errors.New("camera controller did not become ready within 3s")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func waitCommand(cmd *exec.Cmd, stderr *tailBuffer) <-chan error {
	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		if stderr != nil && stderr.String() != "" {
			if err == nil {
				err = errors.New(stderr.String())
			} else {
				err = fmt.Errorf("%w: %s", err, stderr.String())
			}
		}
		done <- err
		close(done)
	}()

	return done
}
