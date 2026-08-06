//go:build darwin || windows

package ffmpeg

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/procio"
)

// The fakes below re-exec this test binary (reexecCommand) instead of a
// POSIX shell: this file also builds on Windows, where sh does not exist.

func TestWaitCommandFoldsStderrIntoTheExitError(t *testing.T) {
	t.Parallel()

	cmd := reexecCommand(context.Background(), "TestFeederStderrHelper", "PRUKKA_FEEDER_STDERR_HELPER")
	stderr := procio.NewTailBuffer(procio.DefaultStderrTail)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	err := <-waitCommand(cmd, stderr)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("exit error = %v, want the stderr tail folded in", err)
	}
}

func TestWaitProcessReadyReportsAStartupExit(t *testing.T) {
	t.Parallel()

	cmd := reexecCommand(context.Background(), "TestFeederExitHelper", "PRUKKA_FEEDER_EXIT_HELPER")
	stderr := procio.NewTailBuffer(procio.DefaultStderrTail)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := waitCommand(cmd, stderr)

	// The timeout only guards a wedged run: a cold re-exec of the race-built
	// binary can take close to a second.
	err := waitProcessReady(context.Background(), cmd, make(chan struct{}), done, 10*time.Second, "test feeder")
	if err == nil || !strings.Contains(err.Error(), "test feeder startup:") {
		t.Fatalf("startup error = %v, want the startup-exit branch naming the feeder", err)
	}
}

func TestFeederStderrHelper(_ *testing.T) {
	if os.Getenv("PRUKKA_FEEDER_STDERR_HELPER") != "1" {
		return
	}

	if _, err := os.Stderr.WriteString("boom\n"); err != nil {
		os.Exit(9)
	}
	os.Exit(3)
}

func TestFeederExitHelper(_ *testing.T) {
	if os.Getenv("PRUKKA_FEEDER_EXIT_HELPER") != "1" {
		return
	}

	os.Exit(1)
}
