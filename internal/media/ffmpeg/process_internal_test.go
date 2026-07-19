package ffmpeg

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/procio"
)

func TestProcessCloseReturnsTheChildFailure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestProcessFailureHelper")
	cmd.Env = append(os.Environ(), "PRUKKA_PROCESS_FAILURE_HELPER=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}

	proc := &process{
		cmd: cmd, out: stdout, log: slog.New(slog.DiscardHandler),
		stderr: procio.NewTailBuffer(procio.DefaultStderrTail), src: "test", done: ctx.Done(),
	}
	if _, err := io.Copy(io.Discard, proc); err != nil {
		t.Fatalf("wait for helper output: %v", err)
	}
	if err := proc.Close(); err == nil {
		t.Fatal("Close discarded the child process failure")
	}
}

func TestProcessCloseStopsItsChildCleanly(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestProcessBlockingHelper")
	cmd.Env = append(os.Environ(), "PRUKKA_PROCESS_BLOCKING_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}

	proc := &process{
		cmd: cmd, out: nopReader{}, log: slog.New(slog.DiscardHandler),
		stderr: procio.NewTailBuffer(procio.DefaultStderrTail), src: "test", done: ctx.Done(),
	}
	if err := proc.Close(); err != nil {
		t.Fatalf("Close returned %v for an owned stop", err)
	}
}

func TestSinkCloseDrainsItsChild(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestProcessDrainHelper")
	cmd.Env = append(os.Environ(), "PRUKKA_PROCESS_DRAIN_HELPER=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}

	s := &sink{in: stdin, proc: &process{
		cmd: cmd, out: nopReader{}, log: slog.New(slog.DiscardHandler),
		stderr: procio.NewTailBuffer(procio.DefaultStderrTail), src: "test", done: ctx.Done(),
	}}
	if _, err := s.Write([]byte("pcm")); err != nil {
		t.Fatalf("write helper input: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close did not drain the child: %v", err)
	}
}

func TestProcessFailureHelper(_ *testing.T) {
	if os.Getenv("PRUKKA_PROCESS_FAILURE_HELPER") != "1" {
		return
	}

	os.Exit(7)
}

func TestProcessBlockingHelper(_ *testing.T) {
	if os.Getenv("PRUKKA_PROCESS_BLOCKING_HELPER") != "1" {
		return
	}

	time.Sleep(time.Hour)
}

func TestProcessDrainHelper(t *testing.T) {
	if os.Getenv("PRUKKA_PROCESS_DRAIN_HELPER") != "1" {
		return
	}

	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatalf("drain stdin: %v", err)
	}
}

func TestClassifyProcessFailureReturnsSafeActionableErrors(t *testing.T) {
	t.Parallel()

	cause := errors.New("exit status 1")
	cases := []struct {
		stderr string
		want   string
	}{
		{stderr: "Permission denied", want: "media source permission denied"},
		{stderr: "Address already in use", want: "media endpoint is already in use"},
		{stderr: "Connection refused", want: "media endpoint refused the connection"},
		{stderr: "Connection timed out", want: "media endpoint timed out"},
		{stderr: "Stream map '0:a:0' matches no streams", want: "media source has no usable audio stream"},
		{stderr: "Invalid data found when processing input", want: "media source format is invalid"},
		{stderr: "No such file or directory", want: "media source was not found"},
		{stderr: "Audio format is not supported", want: "media device audio format is temporarily unavailable"},
		{stderr: "Input/output error", want: "media source I/O failed"},
		{stderr: "Error opening input: I/O error", want: "media source I/O failed"},
		{stderr: "opaque private failure", want: "media process exited unexpectedly"},
	}

	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()

			got := classifyProcessFailure(cause, tc.stderr)
			if !errors.Is(got, cause) || !strings.Contains(got.Error(), tc.want) {
				t.Fatalf("classifyProcessFailure = %v, want %q wrapping cause", got, tc.want)
			}
			if strings.Contains(got.Error(), tc.stderr) {
				t.Fatalf("classified error exposed stderr: %v", got)
			}
		})
	}
}

func TestRetryableStartupFailureClassificationIsNarrowAndWrapped(t *testing.T) {
	t.Parallel()

	cause := errors.New("exit status 1")
	for _, stderr := range []string{
		"Audio format is not supported",
		"Input/output error",
		"Error opening input: I/O error",
	} {
		err := classifyProcessFailure(cause, stderr)
		if !IsRetryableStartupFailure(err) || !errors.Is(err, cause) {
			t.Errorf("classification for %q = %v, want retryable wrapper", stderr, err)
		}
	}

	for _, stderr := range []string{
		"Permission denied",
		"Device not found",
		"Invalid data found when processing input",
		"opaque private failure",
	} {
		if err := classifyProcessFailure(cause, stderr); IsRetryableStartupFailure(err) {
			t.Errorf("classification for %q = %v, want non-retryable", stderr, err)
		}
	}

	if IsRetryableStartupFailure(errors.New("media source I/O failed")) {
		t.Fatal("plain message-matching error was treated as a classified startup failure")
	}
}
