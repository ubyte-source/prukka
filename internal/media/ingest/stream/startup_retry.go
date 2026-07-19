package stream

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
)

const (
	deviceStartupMaxRetries = 4
	deviceStartupRetryBase  = 200 * time.Millisecond
	deviceStartupRetryMax   = 1200 * time.Millisecond
	deviceStartupFirstFrame = 5 * time.Second
	darwinOS                = "darwin"
)

var errDeviceStartupFirstFrame = errors.New("device capture produced no first frame before startup deadline")

// deviceStartupRetryEnabled isolates the CoreAudio handoff workaround to
// AVFoundation capture. Network, file and the other native device backends
// retain their existing one-attempt semantics.
func deviceStartupRetryEnabled(goos, source string) bool {
	return goos == darwinOS && ffmpeg.IsDeviceURL(source)
}

type startupRetryPolicy struct {
	retryable         func(error) bool
	wait              func(context.Context, int) error
	firstFrameTimeout time.Duration
	maxRetries        int
}

func productionStartupRetryPolicy() startupRetryPolicy {
	return startupRetryPolicy{
		maxRetries:        deviceStartupMaxRetries,
		retryable:         ffmpeg.IsRetryableStartupFailure,
		wait:              waitDeviceStartupRetry,
		firstFrameTimeout: deviceStartupFirstFrame,
	}
}

func waitDeviceStartupRetry(ctx context.Context, retry int) error {
	timer := time.NewTimer(deviceStartupRetryDelay(retry))
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func deviceStartupRetryDelay(retry int) time.Duration {
	delay := deviceStartupRetryBase << max(0, retry-1)

	return min(delay, deviceStartupRetryMax)
}

type frameOpener func(context.Context) (core.Frames, error)

// startupRetryFrames absorbs AVFoundation's short format-negotiation race
// without tearing down the already-warm speech providers. A successful Next
// permanently disarms retry: later device failures are genuine lane failures.
type startupRetryFrames struct {
	ctx         context.Context
	terminalErr error
	closeErr    error
	cancel      context.CancelFunc
	open        frameOpener
	current     *frameAttempt
	closeDone   chan struct{}
	policy      startupRetryPolicy
	nextMu      sync.Mutex
	mu          sync.Mutex
	retries     int
	closeOnce   sync.Once
	delivered   bool
	closed      bool
}

func newStartupRetryFrames(
	parent context.Context, initial core.Frames, open frameOpener, policy startupRetryPolicy,
) *startupRetryFrames {
	ctx, cancel := context.WithCancel(parent)

	return &startupRetryFrames{
		ctx: ctx, cancel: cancel, open: open, policy: policy,
		current: newFrameAttempt(initial, nil), closeDone: make(chan struct{}),
	}
}

func (f *startupRetryFrames) Next(ctx context.Context) (core.PCM, error) {
	f.nextMu.Lock()
	defer f.nextMu.Unlock()

	for {
		attempt, err := f.activeAttempt()
		if err != nil {
			return core.PCM{}, err
		}

		frame, nextErr := f.nextAttempt(ctx, attempt)
		if nextErr == nil {
			f.markDelivered()

			return frame, nil
		}

		failure := errors.Join(nextErr, attempt.close())
		f.clearAttempt(attempt)
		if callErr := ctx.Err(); callErr != nil {
			return core.PCM{}, f.setTerminal(errors.Join(callErr, failure))
		}
		if retryErr := f.retryStartup(ctx, failure); retryErr != nil {
			return core.PCM{}, retryErr
		}
	}
}

// nextAttempt bounds only the first media read. A live AVFoundation process
// can occasionally survive device negotiation without ever writing PCM; in
// that state there is no process error for the ordinary startup classifier to
// inspect. Caller cancellation always wins and successful delivery permanently
// removes this deadline.
func (f *startupRetryFrames) nextAttempt(
	call context.Context, attempt *frameAttempt,
) (core.PCM, error) {
	f.mu.Lock()
	timeout := f.policy.firstFrameTimeout
	delivered := f.delivered
	f.mu.Unlock()
	if delivered || timeout <= 0 {
		return attempt.frames.Next(call)
	}

	readCtx, cancelRead := context.WithTimeoutCause(call, timeout, errDeviceStartupFirstFrame)
	frame, err := attempt.frames.Next(readCtx)
	timedOut := errors.Is(err, context.DeadlineExceeded) &&
		errors.Is(context.Cause(readCtx), errDeviceStartupFirstFrame) && call.Err() == nil
	cancelRead()
	if timedOut {
		return core.PCM{}, errors.Join(errDeviceStartupFirstFrame, err)
	}

	return frame, err
}

func (f *startupRetryFrames) activeAttempt() (*frameAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	case f.terminalErr != nil:
		return nil, f.terminalErr
	case f.closed:
		return nil, io.ErrClosedPipe
	case f.current == nil:
		return nil, f.setTerminalLocked(errors.New("device capture has no active process"))
	default:
		return f.current, nil
	}
}

func (f *startupRetryFrames) markDelivered() {
	f.mu.Lock()
	f.delivered = true
	f.mu.Unlock()
}

func (f *startupRetryFrames) clearAttempt(attempt *frameAttempt) {
	f.mu.Lock()
	if f.current == attempt {
		f.current = nil
	}
	f.mu.Unlock()
}

func (f *startupRetryFrames) retryStartup(ctx context.Context, failure error) error {
	retry, terminalErr := f.reserveRetry(failure)
	if terminalErr != nil {
		return terminalErr
	}

	waitCtx, stopWait := f.retryContext(ctx)
	waitErr := f.policy.wait(waitCtx, retry)
	stopWait()
	if waitErr != nil {
		return f.setTerminal(f.contextError(ctx, waitErr))
	}

	attempt, openErr := f.openRetryAttempt(ctx)
	if openErr != nil {
		if attempt != nil {
			openErr = errors.Join(openErr, attempt.close())
		}

		return f.setTerminal(openErr)
	}

	f.mu.Lock()
	if f.closed || f.ctx.Err() != nil || ctx.Err() != nil {
		f.mu.Unlock()
		closeErr := attempt.close()

		return f.setTerminal(errors.Join(f.contextError(ctx, io.ErrClosedPipe), closeErr))
	}
	f.current = attempt
	f.mu.Unlock()

	return nil
}

func (f *startupRetryFrames) openRetryAttempt(call context.Context) (*frameAttempt, error) {
	processCtx, cancelProcess := context.WithCancel(f.ctx)
	stopCall := context.AfterFunc(call, cancelProcess)
	frames, openErr := f.open(processCtx)
	callActive := stopCall()
	if openErr != nil {
		attempt := newFrameAttempt(frames, cancelProcess)

		return nil, errors.Join(f.contextError(call, openErr), attempt.close())
	}
	if frames == nil {
		cancelProcess()

		return nil, errors.New("device capture retry returned no frames")
	}

	attempt := newFrameAttempt(frames, cancelProcess)
	if !callActive || call.Err() != nil {
		return attempt, f.contextError(call, context.Canceled)
	}

	return attempt, nil
}

func (f *startupRetryFrames) reserveRetry(failure error) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.closed || f.ctx.Err() != nil {
		return 0, f.setTerminalLocked(f.contextErrorLocked(io.ErrClosedPipe))
	}
	retryable := errors.Is(failure, errDeviceStartupFirstFrame) || f.policy.retryable(failure)
	if f.delivered || f.retries >= f.policy.maxRetries || !retryable {
		return 0, f.setTerminalLocked(failure)
	}

	f.retries++

	return f.retries, nil
}

func (f *startupRetryFrames) retryContext(
	call context.Context,
) (retryCtx context.Context, stopRetry func()) {
	retryCtx, cancel := context.WithCancel(f.ctx)
	stopCall := context.AfterFunc(call, cancel)
	stopRetry = func() {
		stopCall()
		cancel()
	}

	return retryCtx, stopRetry
}

func (f *startupRetryFrames) contextError(call context.Context, fallback error) error {
	if err := call.Err(); err != nil {
		return err
	}
	if err := f.ctx.Err(); err != nil {
		return err
	}

	return fallback
}

func (f *startupRetryFrames) contextErrorLocked(fallback error) error {
	if err := f.ctx.Err(); err != nil {
		return err
	}

	return fallback
}

func (f *startupRetryFrames) setTerminal(err error) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.setTerminalLocked(err)
}

func (f *startupRetryFrames) setTerminalLocked(err error) error {
	if f.terminalErr == nil {
		f.terminalErr = err
	}

	return f.terminalErr
}

// Close cancels a retry wait or reopen and closes the current process once.
// An opener that returns after cancellation loses publication and its result
// is closed by retryStartup before Next returns.
func (f *startupRetryFrames) Close() error {
	f.closeOnce.Do(func() {
		f.cancel()
		f.mu.Lock()
		f.closed = true
		attempt := f.current
		f.current = nil
		f.mu.Unlock()

		if attempt != nil {
			f.closeErr = attempt.close()
		}
		close(f.closeDone)
	})
	<-f.closeDone

	return f.closeErr
}

type frameAttempt struct {
	frames core.Frames
	err    error
	cancel context.CancelFunc
	once   sync.Once
}

func newFrameAttempt(frames core.Frames, cancel context.CancelFunc) *frameAttempt {
	return &frameAttempt{frames: frames, cancel: cancel}
}

func (a *frameAttempt) close() error {
	a.once.Do(func() {
		if a.cancel != nil {
			a.cancel()
		}
		if closer, ok := a.frames.(io.Closer); ok {
			a.err = closer.Close()
		}
	})

	return a.err
}
