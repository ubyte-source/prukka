//go:build darwin || linux || windows

package native

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	coreengine "github.com/ubyte-source/prukka/internal/core/engine"

	"github.com/ubyte-source/prukka/internal/testkit"
)

type blockingTreeFrames struct{}

func (blockingTreeFrames) Next(ctx context.Context) (core.PCM, error) {
	<-ctx.Done()

	return core.PCM{}, ctx.Err()
}

type descendantSink struct{ pid chan<- int }

func (s descendantSink) Append(segment *core.TranslatedSegment) {
	pid, err := strconv.Atoi(segment.Text)
	if err == nil {
		s.pid <- pid
	}
}

func startTestDescendant(t *testing.T) (process *warmProcess, descendantPID int) {
	t.Helper()

	log := slog.New(slog.DiscardHandler)
	// Re-executes the fixed test binary; no user-controlled executable or args.
	cmd := exec.CommandContext(t.Context(), os.Args[0], fakeTreeParent)
	proc, err := startWarmProcess(t.Context(), cmd, "native tree test", log)
	if err != nil {
		t.Fatalf("start process tree: %v", err)
	}
	t.Cleanup(proc.abort)

	pidResult := make(chan int, 1)
	go drainTestProcess(proc, pidResult)

	return proc, waitDescendantPID(t, pidResult)
}

func TestSTTCancellationKillsDescendantProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	transcription, err := NewSTT(&STTConfig{
		Bin: os.Args[0], Model: fakeTreeModel, Rate: 16000,
	}).Open(ctx, "it")
	if err != nil {
		t.Fatalf("open process-tree STT: %v", err)
	}

	events := transcription.Events()
	eventResult := make(chan int, 1)
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for event := range events {
			pid, parseErr := strconv.Atoi(event.Text)
			if parseErr == nil {
				select {
				case eventResult <- pid:
				default:
				}
			}
		}
	}()

	pid := waitDescendantPID(t, eventResult)
	if !descendantAlive(pid) {
		t.Fatalf("STT descendant %d exited before cancellation", pid)
	}

	cancel()
	if closeErr := transcription.Close(); closeErr != nil {
		t.Fatalf("close transcription: %v", closeErr)
	}
	waitDone(t, eventsDone)
	waitDescendantExit(t, pid)
}

func TestSTTKilledWrapperReapsDescendant(t *testing.T) {
	transcription, err := NewSTT(&STTConfig{
		Bin: os.Args[0], Model: fakeTreeModel, Rate: 16000,
	}).Open(t.Context(), "it")
	if err != nil {
		t.Fatalf("open process-tree STT: %v", err)
	}
	session, ok := transcription.(*sttSession)
	if !ok {
		t.Fatalf("transcription type = %T, want native session", transcription)
	}

	pidResult := make(chan int, 1)
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for event := range transcription.Events() {
			pid, parseErr := strconv.Atoi(event.Text)
			if parseErr == nil {
				pidResult <- pid
			}
		}
	}()
	pid := waitDescendantPID(t, pidResult)
	if killErr := session.cmd.Process.Kill(); killErr != nil {
		t.Fatalf("kill wrapper: %v", killErr)
	}

	waitDone(t, eventsDone)
	if transcription.Err() == nil {
		t.Fatal("killed wrapper was reported as a clean transcription")
	}
	if closeErr := transcription.Close(); closeErr != nil {
		t.Fatalf("close transcription: %v", closeErr)
	}
	waitDescendantExit(t, pid)
}

func TestSTTCloseReportsProcessTreeFailure(t *testing.T) {
	transcription, err := NewSTT(&STTConfig{
		Bin: os.Args[0], Model: fakeTreeModel, Rate: 16000,
	}).Open(t.Context(), "it")
	if err != nil {
		t.Fatalf("open process-tree STT: %v", err)
	}
	session, ok := transcription.(*sttSession)
	if !ok {
		t.Fatalf("transcription type = %T, want native session", transcription)
	}

	pidResult := make(chan int, 1)
	go func() {
		for event := range transcription.Events() {
			pid, parseErr := strconv.Atoi(event.Text)
			if parseErr == nil {
				pidResult <- pid
			}
		}
	}()
	pid := waitDescendantPID(t, pidResult)
	wantCleanup := errors.New("stt tree cleanup failed")
	session.tree = &failingCloseTree{processTree: session.tree, err: wantCleanup}

	if closeErr := transcription.Close(); !errors.Is(closeErr, wantCleanup) {
		t.Fatalf("Close = %v, want process-tree failure", closeErr)
	}
	waitDescendantExit(t, pid)
}

func TestSTTHelperExitKillsAbandonedDescendant(t *testing.T) {
	transcription, err := NewSTT(&STTConfig{
		Bin: os.Args[0], Model: fakeExitTree, Rate: 16000,
	}).Open(t.Context(), "it")
	if err != nil {
		t.Fatalf("open process-tree STT: %v", err)
	}

	var pid int
	for event := range transcription.Events() {
		pid, err = strconv.Atoi(event.Text)
		if err != nil {
			t.Fatalf("parse descendant PID %q: %v", event.Text, err)
		}
	}
	if pid == 0 {
		t.Fatal("helper did not report its descendant")
	}
	if closeErr := transcription.Close(); closeErr != nil {
		t.Fatalf("close transcription: %v", closeErr)
	}
	waitDescendantExit(t, pid)
	if transcription.Err() == nil {
		t.Fatal("unexpected helper exit was reported as a clean transcription")
	}
}

func TestEngineCancellationWaitsForNativeProcessTree(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	pidResult := make(chan int, 1)
	lane := coreengine.New(&coreengine.Config{
		Stream: coreengine.Stream{Session: "tree", Track: "main", Source: "it"},
		Providers: coreengine.Providers{Transcriber: NewSTT(&STTConfig{
			Bin: os.Args[0], Model: fakeTreeModel, Rate: 16000,
		})},
		Output: coreengine.Output{Sinks: map[core.Lang]coreengine.Sink{
			"it": descendantSink{pid: pidResult},
		}},
	}, slog.New(slog.DiscardHandler))

	done := make(chan error, 1)
	go func() { done <- lane.Run(ctx, blockingTreeFrames{}) }()
	pid := waitDescendantPID(t, pidResult)
	if !descendantAlive(pid) {
		t.Fatalf("STT descendant %d exited before lane cancellation", pid)
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want cancellation", err)
	}
	waitDescendantExit(t, pid)
}

func drainTestProcess(proc *warmProcess, pidResult chan<- int) {
	scanner := bufio.NewScanner(proc.stdout)
	if scanner.Scan() {
		pid, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
		if err == nil {
			pidResult <- pid
		}
	}
	for scanner.Scan() {
	}
	proc.finish("native tree test", scanner.Err())
}

func waitDescendantPID(t *testing.T, result <-chan int) int {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	select {
	case pid := <-result:
		return pid
	case <-ctx.Done():
		t.Fatalf("wait for descendant PID: %v", ctx.Err())
	}

	return 0
}

func waitDescendantExit(t *testing.T, pid int) {
	t.Helper()

	testkit.Eventually(t, 3*time.Second, func() bool { return !descendantAlive(pid) },
		"descendant "+strconv.Itoa(pid)+" survived tree cancellation")
}
