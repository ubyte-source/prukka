//go:build darwin || windows

package ffmpeg

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/procio"
)

func TestWaitCommandFoldsStderrIntoTheExitError(t *testing.T) {
	t.Parallel()

	cmd := exec.CommandContext(t.Context(), "sh", "-c", "echo boom >&2; exit 3")
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

	cmd := exec.CommandContext(t.Context(), "sh", "-c", "exit 1")
	stderr := procio.NewTailBuffer(procio.DefaultStderrTail)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := waitCommand(cmd, stderr)

	err := waitProcessReady(t.Context(), cmd, make(chan struct{}), done, time.Second, "test feeder")
	if err == nil || !strings.Contains(err.Error(), "test feeder") {
		t.Fatalf("startup error = %v, want it to name the feeder", err)
	}
}
