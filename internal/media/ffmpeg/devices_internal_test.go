package ffmpeg

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/media/deviceurl"
)

func TestStartDevicePlaybackFeedsAndSeals(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake helpers cannot exec on windows")
	}
	t.Parallel()

	dir := t.TempDir()
	captured := filepath.Join(dir, "out.pcm")
	helper := filepath.Join(dir, "helper")
	mode := os.FileMode(0o700)
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexec cat > \""+captured+"\"\n"), mode); err != nil {
		t.Fatal(err)
	}

	prewarmHelper(t, helper)

	sink, err := StartDevicePlayback(t.Context(), helper, "Prukka Microphone", 16000, discardLog())
	if err != nil {
		t.Fatalf("StartDevicePlayback: %v", err)
	}
	patientDrain(t, sink)
	payload := []byte("pcm-payload")
	if _, writeErr := sink.Write(payload); writeErr != nil {
		t.Fatalf("write: %v", writeErr)
	}
	if closeErr := sink.Close(); closeErr != nil {
		t.Fatalf("close: %v", closeErr)
	}
	if closeErr := sink.Close(); closeErr != nil {
		t.Fatalf("second close must stay idempotent: %v", closeErr)
	}
	data, readErr := os.ReadFile(filepath.Clean(captured))
	if readErr != nil || !bytes.Equal(data, payload) {
		t.Fatalf("helper captured %q (%v), want %q", data, readErr, payload)
	}
}

// prewarmHelper runs the freshly written script once: the FIRST exec pays
// Gatekeeper's assessment, which can take seconds on a loaded machine.
func prewarmHelper(t *testing.T, helper string) {
	t.Helper()
	warm, err := StartDevicePlayback(t.Context(), helper, "warm", 16000, discardLog())
	if err != nil {
		return
	}
	patientDrain(t, warm)
	if closeErr := warm.Close(); closeErr != nil {
		t.Logf("warm close: %v", closeErr)
	}
}

// patientDrain widens a sink's drain bound for tests that assert the seal
// contract, not exit latency: on an oversubscribed CI scheduler the shell
// helper can take longer than the 5s production bound to observe EOF.
func patientDrain(t *testing.T, sink io.WriteCloser) {
	t.Helper()

	playback, ok := sink.(*playbackSink)
	if !ok {
		t.Fatalf("sink type = %T, want the native playback sink", sink)
	}
	playback.drain = time.Minute
}

func discardLog() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestPlaybackSinkCloseKillsAStuckHelper(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("uses the POSIX sleep helper")
	}

	cmd := newCommand(t.Context(), "sleep", []string{"30"})
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	tree, startErr := startChild(cmd, discardLog(), "test")
	if startErr != nil {
		t.Fatalf("start helper: %v", startErr)
	}

	sink := newPlaybackSink(stdin, cmd, tree, 20*time.Millisecond)

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- sink.Close() }()

	var closeErr error
	select {
	case closeErr = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close blocked well past the drain: kill branch never fired")
	}

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Close took %v; the 20ms drain should have killed the helper promptly", elapsed)
	}
	// A helper that could not drain is killed, so Wait reports the signal.
	if closeErr == nil {
		t.Fatal("Close returned nil; a killed helper's Wait must surface an error")
	}
}

func TestLineLoggerSplitsBuffersAndDropsBlankLines(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ll := &lineLogger{log: log, msg: "helper"}

	for _, w := range []string{"star", "ted\n", "a\n\nb\n", "  \n", "partial-no-nl"} {
		n, err := ll.Write([]byte(w))
		if err != nil {
			t.Fatalf("Write(%q): %v", w, err)
		}
		if n != len(w) {
			t.Fatalf("Write(%q) returned %d, want %d", w, n, len(w))
		}
	}

	out := buf.String()
	for _, want := range []string{"line=started", "line=a", "line=b"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "partial-no-nl") {
		t.Errorf("held partial line was emitted before its newline:\n%s", out)
	}
	if got := strings.Count(out, "msg=helper"); got != 3 {
		t.Errorf("emitted %d lines, want 3 (blank + whitespace-only dropped):\n%s", got, out)
	}
}

// TestDeviceErrorsNameNoDeviceIdentity pins that a rejected capture or feeder
// target is named by its scheme alone: an id and a label are capture hardware.
func TestDeviceErrorsNameNoDeviceIdentity(t *testing.T) {
	t.Parallel()

	_, audioErr := deviceInputArgsFor(runtime.GOOS, "device://video/Blue Yeti USB", pcmConfig{})
	_, pairErr := avInputArgs("freebsd", deviceurl.Ref{Kind: deviceurl.AV, ID: "Cam|Blue Yeti USB"}, pcmConfig{})
	feeder, feederErr := (&Supervisor{}).StartVideoDevice(t.Context(), "index.m3u8", "device://video/Blue Yeti USB")
	if feeder != nil {
		t.Fatal("a rejected feeder target still started a child")
	}

	for _, err := range []error{audioErr, pairErr, feederErr} {
		if err == nil {
			t.Fatal("a malformed device target was accepted")
		}
		if strings.Contains(err.Error(), "Blue Yeti") {
			t.Fatalf("error exposes the device id: %v", err)
		}
	}
}
