package ffmpeg

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/procio"
)

// reexecCommand re-runs this test binary against one env-gated helper test:
// the package's portable child-process fake, used where a shell fake would not
// exec on Windows.
func reexecCommand(ctx context.Context, run, envGate string) *exec.Cmd {
	cmd := newCommand(ctx, os.Args[0], []string{"-test.run=" + run})
	cmd.Env = append(os.Environ(), envGate+"=1")

	return cmd
}

// startTestChild starts a fixture command the way the supervisor does, so its
// process tree is attached before any test reaps it.
func startTestChild(t *testing.T, cmd *exec.Cmd) procio.Tree {
	t.Helper()

	tree, err := startChild(cmd, slog.New(slog.DiscardHandler), "test")
	if err != nil {
		t.Fatalf("start helper: %v", err)
	}
	t.Cleanup(func() {
		// Kill first, exactly as production does: a still-running fixture must
		// be signaled before its handle is dropped.
		if retireErr := errors.Join(tree.Kill(), tree.Release()); retireErr != nil {
			t.Logf("retire fixture process tree: %v", retireErr)
		}
	})

	return tree
}

func TestProcessCloseReturnsTheChildFailure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cmd := reexecCommand(ctx, "TestProcessFailureHelper", "PRUKKA_PROCESS_FAILURE_HELPER")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	tree := startTestChild(t, cmd)

	proc := stopFirst(&process{
		cmd:    cmd,
		out:    stdout,
		log:    slog.New(slog.DiscardHandler),
		tree:   tree,
		stderr: procio.NewTailBuffer(procio.DefaultStderrTail),
		src:    "test",
		done:   ctx.Done(),
	})
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
	cmd := reexecCommand(ctx, "TestProcessBlockingHelper", "PRUKKA_PROCESS_BLOCKING_HELPER")
	tree := startTestChild(t, cmd)

	proc := stopFirst(&process{
		cmd:    cmd,
		out:    nopReader{},
		log:    slog.New(slog.DiscardHandler),
		tree:   tree,
		stderr: procio.NewTailBuffer(procio.DefaultStderrTail),
		src:    "test",
		done:   ctx.Done(),
	})
	if err := proc.Close(); err != nil {
		t.Fatalf("Close returned %v for an owned stop", err)
	}
	// Every owner in the tree may close; a second reap would Wait on a child
	// the kernel already released.
	if err := proc.Close(); err != nil {
		t.Fatalf("second Close = %v, want the cached first result", err)
	}
}

func TestSinkCloseDrainsItsChild(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cmd := reexecCommand(ctx, "TestProcessDrainHelper", "PRUKKA_PROCESS_DRAIN_HELPER")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	tree := startTestChild(t, cmd)

	s := newSink(stdin, drainOnly(&process{
		cmd:    cmd,
		out:    nopReader{},
		log:    slog.New(slog.DiscardHandler),
		tree:   tree,
		stderr: procio.NewTailBuffer(procio.DefaultStderrTail),
		src:    "test",
		done:   ctx.Done(),
	}))
	if _, err := s.Write([]byte("pcm")); err != nil {
		t.Fatalf("write helper input: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close did not drain the child: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close = %v, want the cached first result", err)
	}
}

// TestDrainReapIsBoundedByAPipeHoldingDescendant pins cmd.WaitDelay as the only
// thing that can end the drain reap: it kills nothing by design.
func TestDrainReapIsBoundedByAPipeHoldingDescendant(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cmd := reexecCommand(ctx,
		"TestProcessExitingDescendantHelper", "PRUKKA_PROCESS_EXITING_DESCENDANT_HELPER")
	stderr := procio.NewTailBuffer(procio.DefaultStderrTail)
	cmd.Stderr = stderr
	tree := startTestChild(t, cmd)

	proc := drainOnly(&process{
		cmd:    cmd,
		out:    nopReader{},
		log:    slog.New(slog.DiscardHandler),
		tree:   tree,
		stderr: stderr,
		src:    "test",
		done:   ctx.Done(),
	})

	drained := make(chan error, 1)
	go func() { drained <- proc.Close() }()

	// The bound is childWaitDelay measured from the child's own exit; the
	// slack covers starting the fixture, which re-execs this test binary.
	select {
	case err := <-drained:
		if err != nil {
			t.Fatalf("drain reap = %v, want a clean bound: the child exited, only its pipes outlived it", err)
		}
	case <-time.After(5 * childWaitDelay):
		t.Fatal("the drain reap never returned: a descendant holding the stderr pipe left cmd.Wait unbounded")
	}
}

// TestSinkCloseIsBoundedByAPipeHoldingDescendant pins that Close returns; two
// independent nets hold it and this fixture cannot tell them apart.
func TestSinkCloseIsBoundedByAPipeHoldingDescendant(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cmd := reexecCommand(ctx,
		"TestProcessExitingDescendantHelper", "PRUKKA_PROCESS_EXITING_DESCENDANT_HELPER")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stderr := procio.NewTailBuffer(procio.DefaultStderrTail)
	cmd.Stderr = stderr
	tree := startTestChild(t, cmd)

	s := newSink(stdin, drainOnly(&process{
		cmd:    cmd,
		out:    nopReader{},
		log:    slog.New(slog.DiscardHandler),
		tree:   tree,
		stderr: stderr,
		src:    "test",
		done:   ctx.Done(),
	}))

	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()

	select {
	case closeErr := <-closed:
		if closeErr != nil {
			t.Fatalf("Close = %v, want a clean bounded drain", closeErr)
		}
	case <-time.After(3 * sinkDrainTimeout):
		t.Fatal("sink.Close never returned: a descendant holding the stderr pipe left cmd.Wait unbounded")
	}
}

// TestProcessCloseKillsTheChildsWholeGroup: both fixture streams are the same
// *os.File pipe, so it reaches EOF only once the descendant's write end closes.
func TestProcessCloseKillsTheChildsWholeGroup(t *testing.T) {
	t.Parallel()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := reader.Close(); closeErr != nil {
			t.Logf("close the inherited pipe: %v", closeErr)
		}
	})

	ctx := context.Background()
	cmd := reexecCommand(ctx,
		"TestProcessBlockingDescendantHelper", "PRUKKA_PROCESS_BLOCKING_DESCENDANT_HELPER")
	cmd.Stdout, cmd.Stderr = writer, writer
	tree := startTestChild(t, cmd)
	if err := writer.Close(); err != nil {
		t.Fatalf("close the parent's write end: %v", err)
	}

	// Stopping before the fork would prove nothing.
	inherited := bufio.NewReader(reader)
	if line, readErr := inherited.ReadString('\n'); readErr != nil || line != descendantReadyMarker {
		t.Fatalf("descendant readiness = (%q, %v), want %q", line, readErr, descendantReadyMarker)
	}

	proc := stopFirst(&process{
		cmd:    cmd,
		out:    nopReader{},
		log:    slog.New(slog.DiscardHandler),
		tree:   tree,
		stderr: procio.NewTailBuffer(procio.DefaultStderrTail),
		src:    "test",
		done:   ctx.Done(),
	})
	if err := proc.Close(); err != nil {
		t.Fatalf("Close returned %v for an owned stop", err)
	}

	drained := make(chan error, 1)
	go func() {
		_, readErr := io.ReadAll(inherited)
		drained <- readErr
	}()
	select {
	case readErr := <-drained:
		if readErr != nil {
			t.Fatalf("reading the inherited pipe: %v", readErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the child's descendant outlived Close: killing the leader's PID left the group running")
	}
}

// reapOrderTree records every procio.Tree call with whether the child was
// already reaped: os/exec publishes cmd.ProcessState exactly when Wait returns.
type reapOrderTree struct {
	cmd   *exec.Cmd
	calls []string
	mu    sync.Mutex
}

func (t *reapOrderTree) record(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.cmd.ProcessState != nil {
		name += "-after-reap"
	}
	t.calls = append(t.calls, name)
}

func (t *reapOrderTree) Kill() error {
	t.record("kill")

	return nil
}

func (t *reapOrderTree) Release() error {
	t.record("release")

	return nil
}

func (t *reapOrderTree) recorded() []string {
	t.mu.Lock()
	defer t.mu.Unlock()

	return append([]string(nil), t.calls...)
}

func TestReapSignalsTheTreeOnlyWhileTheChildIsUnreaped(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cmd := reexecCommand(ctx, "TestProcessFailureHelper", "PRUKKA_PROCESS_FAILURE_HELPER")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}

	tree := &reapOrderTree{cmd: cmd}
	proc := drainOnly(&process{
		cmd:    cmd,
		out:    nopReader{},
		log:    slog.New(slog.DiscardHandler),
		tree:   tree,
		stderr: procio.NewTailBuffer(procio.DefaultStderrTail),
		src:    "test",
		done:   ctx.Done(),
	})
	if err := proc.Close(); err == nil {
		t.Fatal("the drain reap discarded the child process failure")
	}

	got := strings.Join(tree.recorded(), ",")
	if got != "release-after-reap" {
		t.Fatalf("tree calls = %q, want only release-after-reap: nothing may signal a reaped child", got)
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

// TestProcessExitingDescendantHelper forks a descendant that inherits its stdio
// and exits at once, leaving the supervisor's Wait joining copier goroutines.
func TestProcessExitingDescendantHelper(t *testing.T) {
	if os.Getenv("PRUKKA_PROCESS_EXITING_DESCENDANT_HELPER") != "1" {
		return
	}

	spawnTestDescendant(t)
}

// descendantReadyMarker is the line the blocking fixture prints on the
// inherited pipe once its descendant exists.
const descendantReadyMarker = "descendant-up\n"

// TestProcessBlockingDescendantHelper keeps both itself and its descendant
// alive until the supervisor retires their process group.
func TestProcessBlockingDescendantHelper(t *testing.T) {
	if os.Getenv("PRUKKA_PROCESS_BLOCKING_DESCENDANT_HELPER") != "1" {
		return
	}

	spawnTestDescendant(t)
	if _, err := os.Stdout.WriteString(descendantReadyMarker); err != nil {
		t.Fatalf("publish descendant marker: %v", err)
	}
	time.Sleep(time.Hour)
}

// descendantLifetime bounds a descendant that outlives the reap: a latched tree
// refuses to signal, so the grandchild has to retire itself.
const descendantLifetime = 30 * time.Second

// spawnTestDescendant forks a sleeping grandchild that inherits this process's
// stdio and starts no process group of its own, so it stays inside its parent's.
func spawnTestDescendant(t *testing.T) {
	t.Helper()

	cmd := exec.CommandContext(context.Background(), os.Args[0],
		"-test.run=TestProcessBlockingHelper", "-test.timeout="+descendantLifetime.String())
	cmd.Env = append(os.Environ(), "PRUKKA_PROCESS_BLOCKING_HELPER=1")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start descendant: %v", err)
	}
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

// TestClassifyExitRedactsTheStderrBreadcrumb assembles its URL from parts so it
// is not a hardcoded-credential literal.
func TestClassifyExitRedactsTheStderrBreadcrumb(t *testing.T) {
	t.Parallel()

	user, pass := "user", "secret"
	tail := procio.NewTailBuffer(procio.DefaultStderrTail)
	stderrLine := "rtmp://" + user + ":" + pass + "@live.example:1935/app/streamkey: Connection refused"
	if _, err := tail.Write([]byte(stderrLine)); err != nil {
		t.Fatalf("seed stderr tail: %v", err)
	}

	var logged bytes.Buffer
	p := &process{
		log:    slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})),
		stderr: tail,
		src:    "rtmp://live.example:1935",
	}
	classified := p.classifyExit(errors.New("exit status 1"), false)

	if classified == nil || !strings.Contains(classified.Error(), "refused the connection") {
		t.Fatalf("classifyExit did not classify the failure: %v", classified)
	}

	got := logged.String()
	for _, secret := range []string{pass, "streamkey", user + ":"} {
		if strings.Contains(got, secret) {
			t.Fatalf("breadcrumb leaked %q: %q", secret, got)
		}
	}
	if !strings.Contains(got, "rtmp://live.example:1935 Connection refused") {
		t.Fatalf("breadcrumb lost the safe endpoint and prose: %q", got)
	}
}
