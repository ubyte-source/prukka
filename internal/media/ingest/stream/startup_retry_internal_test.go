package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
)

type retryFrameResult struct {
	err   error
	frame core.PCM
}

type retryTestFrames struct {
	closeErr error
	results  []retryFrameResult
	mu       sync.Mutex
	next     int
	closes   int
}

type blockingRetryFrames struct {
	started   chan struct{}
	closeErr  error
	startOnce sync.Once
	closes    atomic.Int32
}

type deadlineProbeFrames struct {
	postFirstErr error
	next         atomic.Int32
}

func (f *deadlineProbeFrames) Next(ctx context.Context) (core.PCM, error) {
	if f.next.Add(1) == 1 {
		return core.PCM{Data: []int16{13}, Rate: 16_000, Ch: 1}, nil
	}
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return core.PCM{}, errors.New("unexpected post-start frame deadline")
	}

	return core.PCM{}, f.postFirstErr
}

func newBlockingRetryFrames(closeErr error) *blockingRetryFrames {
	return &blockingRetryFrames{started: make(chan struct{}), closeErr: closeErr}
}

func (f *blockingRetryFrames) Next(ctx context.Context) (core.PCM, error) {
	f.startOnce.Do(func() { close(f.started) })
	<-ctx.Done()

	return core.PCM{}, ctx.Err()
}

func (f *blockingRetryFrames) Close() error {
	f.closes.Add(1)

	return f.closeErr
}

func (f *retryTestFrames) Next(context.Context) (core.PCM, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.next >= len(f.results) {
		return core.PCM{}, io.EOF
	}
	result := f.results[f.next]
	f.next++

	return result.frame, result.err
}

func (f *retryTestFrames) Close() error {
	f.mu.Lock()
	f.closes++
	f.mu.Unlock()

	return f.closeErr
}

func (f *retryTestFrames) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.closes
}

func immediateRetryPolicy(maxRetries int, retryable error) startupRetryPolicy {
	return startupRetryPolicy{
		maxRetries: maxRetries,
		retryable:  func(err error) bool { return errors.Is(err, retryable) },
		wait:       func(context.Context, int) error { return nil },
	}
}

func TestDeviceStartupRetryIsDarwinDeviceOnly(t *testing.T) {
	t.Parallel()

	cases := []struct {
		goos   string
		source string
		want   bool
	}{
		{goos: darwinOS, source: "device://audio/1", want: true},
		{goos: darwinOS, source: "device://av/0|1", want: true},
		{goos: darwinOS, source: "file:///tmp/audio.aiff", want: false},
		{goos: darwinOS, source: "rtmp://127.0.0.1/live", want: false},
		{goos: "linux", source: "device://audio/default", want: false},
		{goos: "windows", source: "device://audio/Microphone", want: false},
	}

	for _, tc := range cases {
		if got := deviceStartupRetryEnabled(tc.goos, tc.source); got != tc.want {
			t.Errorf("deviceStartupRetryEnabled(%q, %q) = %v, want %v", tc.goos, tc.source, got, tc.want)
		}
	}
}

func TestDeviceStartupRetryDelayIsShortAndBounded(t *testing.T) {
	t.Parallel()

	want := []time.Duration{
		200 * time.Millisecond, 400 * time.Millisecond,
		800 * time.Millisecond, 1200 * time.Millisecond,
	}
	for retry, expected := range want {
		if got := deviceStartupRetryDelay(retry + 1); got != expected {
			t.Errorf("retry %d delay = %v, want %v", retry+1, got, expected)
		}
	}
	if got := deviceStartupRetryDelay(20); got != deviceStartupRetryMax {
		t.Fatalf("late retry delay = %v, want cap %v", got, deviceStartupRetryMax)
	}
}

func TestStartupRetryRecoversBeforeFirstFrameThenDisarms(t *testing.T) {
	t.Parallel()

	errStartup := errors.New("transient device startup")
	errAfterStart := errors.New("device failed after start")
	initial := &retryTestFrames{results: []retryFrameResult{{err: errStartup}}}
	recovered := &retryTestFrames{results: []retryFrameResult{
		{frame: core.PCM{Data: []int16{7}, Rate: 16_000, Ch: 1}},
		{err: errAfterStart},
	}}
	var opens atomic.Int32
	processDone := make(chan (<-chan struct{}), 1)
	frames := newStartupRetryFrames(t.Context(), initial, func(ctx context.Context) (core.Frames, error) {
		opens.Add(1)
		processDone <- ctx.Done()

		return recovered, nil
	}, immediateRetryPolicy(3, errStartup))
	t.Cleanup(func() { requireStartupRetryClose(t, frames) })

	got, err := frames.Next(t.Context())
	if err != nil || len(got.Data) != 1 || got.Data[0] != 7 {
		t.Fatalf("recovered Next = (%v, %v), want one sample", got.Data, err)
	}
	reopenedDone := <-processDone
	requireProcessLive(t, reopenedDone)
	if _, err = frames.Next(t.Context()); !errors.Is(err, errAfterStart) {
		t.Fatalf("post-start Next error = %v, want %v", err, errAfterStart)
	}
	requireProcessStopped(t, reopenedDone)
	if got := opens.Load(); got != 1 {
		t.Fatalf("reopens = %d after post-start failure, want 1", got)
	}
	if initial.closeCount() != 1 || recovered.closeCount() != 1 {
		t.Fatalf("attempt closes = %d/%d, want 1/1", initial.closeCount(), recovered.closeCount())
	}
}

func TestStartupRetryReopensProcessThatProducesNoFirstFrame(t *testing.T) {
	t.Parallel()

	initial := newBlockingRetryFrames(errors.New("silent process close"))
	recovered := &retryTestFrames{results: []retryFrameResult{{
		frame: core.PCM{Data: []int16{11}, Rate: 16_000, Ch: 1},
	}}}
	policy := immediateRetryPolicy(1, errors.New("unrelated startup failure"))
	policy.firstFrameTimeout = 20 * time.Millisecond
	var opens atomic.Int32
	frames := newStartupRetryFrames(t.Context(), initial, func(context.Context) (core.Frames, error) {
		opens.Add(1)

		return recovered, nil
	}, policy)
	t.Cleanup(func() { requireStartupRetryClose(t, frames) })

	got, err := frames.Next(t.Context())
	if err != nil || len(got.Data) != 1 || got.Data[0] != 11 {
		t.Fatalf("recovered first frame = (%v, %v), want sample 11", got.Data, err)
	}
	if got := opens.Load(); got != 1 {
		t.Fatalf("reopens = %d, want 1", got)
	}
	if got := initial.closes.Load(); got != 1 {
		t.Fatalf("silent initial attempt closes = %d, want 1", got)
	}
}

func TestStartupRetryDoesNotConvertCallerCancellationIntoTimeout(t *testing.T) {
	t.Parallel()

	initial := newBlockingRetryFrames(nil)
	policy := immediateRetryPolicy(1, context.Canceled)
	policy.firstFrameTimeout = time.Second
	var opens atomic.Int32
	frames := newStartupRetryFrames(t.Context(), initial, func(context.Context) (core.Frames, error) {
		opens.Add(1)

		return nil, errors.New("unexpected reopen")
	}, policy)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := frames.Next(ctx)
		done <- err
	}()
	<-initial.started
	cancel()

	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled first read error = %v, want context.Canceled", err)
	}
	if got := opens.Load(); got != 0 {
		t.Fatalf("reopens after caller cancellation = %d, want 0", got)
	}
	if err := frames.Close(); err != nil {
		t.Fatalf("Close after caller cancellation: %v", err)
	}
}

func TestStartupRetryRemovesFirstFrameDeadlineAfterDelivery(t *testing.T) {
	t.Parallel()

	errAfterStart := errors.New("device failed after first frame")
	initial := &deadlineProbeFrames{postFirstErr: errAfterStart}
	policy := immediateRetryPolicy(1, errAfterStart)
	policy.firstFrameTimeout = 20 * time.Millisecond
	var opens atomic.Int32
	frames := newStartupRetryFrames(t.Context(), initial, func(context.Context) (core.Frames, error) {
		opens.Add(1)

		return nil, errors.New("unexpected reopen")
	}, policy)
	t.Cleanup(func() { requireStartupRetryClose(t, frames) })

	first, err := frames.Next(context.Background())
	if err != nil || len(first.Data) != 1 || first.Data[0] != 13 {
		t.Fatalf("first frame = (%v, %v), want sample 13", first.Data, err)
	}
	if _, err = frames.Next(context.Background()); !errors.Is(err, errAfterStart) {
		t.Fatalf("post-start error = %v, want %v without a deadline", err, errAfterStart)
	}
	if got := opens.Load(); got != 0 {
		t.Fatalf("reopens after first delivery = %d, want 0", got)
	}
}

func TestStartupRetryCapClosesEveryAttempt(t *testing.T) {
	t.Parallel()

	errStartup := errors.New("transient device startup")
	attempts := []*retryTestFrames{
		{results: []retryFrameResult{{err: fmt.Errorf("initial: %w", errStartup)}}},
		{results: []retryFrameResult{{err: fmt.Errorf("retry one: %w", errStartup)}}},
		{results: []retryFrameResult{{err: fmt.Errorf("retry two: %w", errStartup)}}},
	}
	var opens atomic.Int32
	frames := newStartupRetryFrames(t.Context(), attempts[0], func(context.Context) (core.Frames, error) {
		index := int(opens.Add(1))

		return attempts[index], nil
	}, immediateRetryPolicy(2, errStartup))

	if _, err := frames.Next(t.Context()); !errors.Is(err, errStartup) {
		t.Fatalf("exhausted Next error = %v, want retryable cause", err)
	}
	if _, err := frames.Next(t.Context()); !errors.Is(err, errStartup) {
		t.Fatalf("cached terminal error = %v, want retryable cause", err)
	}
	if got := opens.Load(); got != 2 {
		t.Fatalf("reopens = %d, want hard cap 2", got)
	}
	for index, attempt := range attempts {
		if got := attempt.closeCount(); got != 1 {
			t.Errorf("attempt %d closes = %d, want 1", index, got)
		}
	}
	if err := frames.Close(); err != nil {
		t.Fatalf("Close after exhaustion: %v", err)
	}
}

func TestStartupRetryWaitIsCancelableWithoutReopen(t *testing.T) {
	t.Parallel()

	errStartup := errors.New("transient device startup")
	initial := &retryTestFrames{results: []retryFrameResult{{err: errStartup}}}
	waiting := make(chan struct{})
	var opens atomic.Int32
	policy := startupRetryPolicy{
		maxRetries: 2,
		retryable:  func(err error) bool { return errors.Is(err, errStartup) },
		wait: func(ctx context.Context, _ int) error {
			close(waiting)
			<-ctx.Done()

			return ctx.Err()
		},
	}
	frames := newStartupRetryFrames(t.Context(), initial, func(context.Context) (core.Frames, error) {
		opens.Add(1)

		return nil, errors.New("unexpected reopen")
	}, policy)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := frames.Next(ctx)
		done <- err
	}()
	<-waiting
	cancel()

	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled retry error = %v, want context.Canceled", err)
	}
	if got := opens.Load(); got != 0 {
		t.Fatalf("reopens after wait cancellation = %d, want 0", got)
	}
	if got := initial.closeCount(); got != 1 {
		t.Fatalf("initial closes = %d, want 1", got)
	}
	if err := frames.Close(); err != nil {
		t.Fatalf("Close after cancellation: %v", err)
	}
}

func TestStartupRetryClosesCandidateReturnedAfterCancellation(t *testing.T) {
	t.Parallel()

	errStartup := errors.New("transient device startup")
	initial := &retryTestFrames{results: []retryFrameResult{{err: errStartup}}}
	candidate := &retryTestFrames{results: []retryFrameResult{{frame: core.PCM{Data: []int16{1}}}}}
	opening := make(chan struct{})
	frames := newStartupRetryFrames(t.Context(), initial, func(ctx context.Context) (core.Frames, error) {
		close(opening)
		<-ctx.Done()

		return candidate, nil
	}, immediateRetryPolicy(1, errStartup))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := frames.Next(ctx)
		done <- err
	}()
	<-opening
	cancel()

	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled reopen error = %v, want context.Canceled", err)
	}
	if initial.closeCount() != 1 || candidate.closeCount() != 1 {
		t.Fatalf("attempt closes = %d/%d, want 1/1", initial.closeCount(), candidate.closeCount())
	}
	if err := frames.Close(); err != nil {
		t.Fatalf("Close after canceled reopen: %v", err)
	}
}

func requireStartupRetryClose(t *testing.T, frames *startupRetryFrames) {
	t.Helper()

	if err := frames.Close(); err != nil {
		t.Errorf("cleanup startup retry frames: %v", err)
	}
}

func requireProcessLive(t *testing.T, done <-chan struct{}) {
	t.Helper()

	select {
	case <-done:
		t.Fatal("reopened process context ended after publication")
	default:
	}
}

func requireProcessStopped(t *testing.T, done <-chan struct{}) {
	t.Helper()

	select {
	case <-done:
	default:
		t.Fatal("reopened process context remained live after terminal failure")
	}
}
