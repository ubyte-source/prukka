package audio

// The device-sink recovery machinery: a write-stall guard that severs a
// wedged-but-alive encoder, and the bounded-backoff reopen that rebuilds the
// sink on the same job without losing the mixer cursor.

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// deviceReopenBackoff paces device-sink reopen attempts; the last step
// repeats. Tests shrink it to keep retries fast.
var deviceReopenBackoff = []time.Duration{
	250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 5 * time.Second,
}

// deviceWriteStallOverride tightens the stall budget in tests; zero keeps the
// default.
var deviceWriteStallOverride atomic.Int64

// deviceWriteStallTimeout bounds one quantum write into a device sink. A
// healthy audiotoolbox process drains a 20-100ms quantum immediately; a queue
// that wedges while its process stays alive stops reading, the pipe fills and
// Write blocks forever with no error — the one silence the reopen path cannot
// see on its own.
func deviceWriteStallTimeout() time.Duration {
	if v := deviceWriteStallOverride.Load(); v > 0 {
		return time.Duration(v)
	}

	return 3 * time.Second
}

// stallGuard wraps a device sink writer and closes it when a single Write
// makes no progress within the stall timeout. Closing the underlying pipe
// unblocks the stuck Write with an error, which the encoder job's device
// recovery turns into a fresh sink.
type stallGuard struct {
	w          io.WriteCloser
	done       chan struct{}
	closeErr   error
	closeOnce  sync.Once
	writeStart atomic.Int64 // unix nanos of the in-flight write; 0 when idle
}

func newStallGuard(w io.WriteCloser) *stallGuard {
	g := &stallGuard{w: w, done: make(chan struct{})}
	go g.watch()

	return g
}

// guardedDeviceStart wraps a device start hook so every sink it opens carries
// the write stall guard.
func guardedDeviceStart(
	start func(context.Context) (io.WriteCloser, error),
) func(context.Context) (io.WriteCloser, error) {
	return func(ctx context.Context) (io.WriteCloser, error) {
		w, err := start(ctx)
		if err != nil {
			return nil, err
		}

		return newStallGuard(w), nil
	}
}

func (g *stallGuard) Write(p []byte) (int, error) {
	g.writeStart.Store(time.Now().UnixNano())
	defer g.writeStart.Store(0)

	return g.w.Write(p)
}

// sever closes the underlying writer exactly once, recording its error for
// Close; the watcher calls it with nothing useful to do with a close failure.
func (g *stallGuard) sever() {
	g.closeOnce.Do(func() {
		close(g.done)
		g.closeErr = g.w.Close()
	})
}

// Close is idempotent: the watcher and the feed may both close on a stall.
func (g *stallGuard) Close() error {
	g.sever()

	return g.closeErr
}

// watch polls the in-flight write and severs the sink when one stalls past
// the timeout. Polling at half the timeout bounds detection latency without
// per-write timer churn.
func (g *stallGuard) watch() {
	timeout := deviceWriteStallTimeout()
	ticker := time.NewTicker(timeout / 2)
	defer ticker.Stop()

	for {
		select {
		case <-g.done:
			return
		case <-ticker.C:
		}
		started := g.writeStart.Load()
		if started != 0 && time.Since(time.Unix(0, started)) > timeout {
			g.sever()

			return
		}
	}
}

// reopenPause waits before a reopen attempt; false means the job ended.
func reopenPause(ctx context.Context, delay time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(delay):
		return true
	}
}

// logSinkReopened records how a device sink came back: routine after a
// reconfiguration, a warning after an error.
func (r *Registry) logSinkReopened(jobID string, attempt int, feedErr error) {
	switch {
	case errors.Is(feedErr, errDeviceReconfigured):
		r.log.Info("device output reconfigured; encoder reopened", "job", jobID)
	case feedErr != nil:
		r.log.Warn("device sink reopened after error",
			"job", jobID, "attempt", attempt, "err", feedErr)
	}
}

// reopenDeviceSink reopens a device sink, retrying with backoff until the
// open succeeds or the job context ends. Every attempt re-runs the start
// hook, which rebinds the target label to its current device index. After an
// encoder death the first attempt is delayed one backoff step, letting
// coreaudiod reap the dead HAL client — an instant reopen can win that race
// and produce a queue that accepts PCM but never reaches the device.
func (r *Registry) reopenDeviceSink(
	ctx context.Context, jobID, pairID string, feedErr error,
	start func(context.Context) (io.WriteCloser, error),
) (io.WriteCloser, encoderVerdict) {
	session := sessionOfPair(pairID)
	if feedErr != nil && !errors.Is(feedErr, errDeviceReconfigured) &&
		!reopenPause(ctx, deviceReopenBackoff[0]) {
		return nil, encoderDone
	}
	for attempt := 1; ; attempt++ {
		// End instead of retrying a dead device forever when the session is
		// finishing or dropped: the finite completion path waits on this job's
		// done channel and cursor, so a perpetual reopen would hang WaitPlayout.
		// pairGone alone terminates; pairBusy (lock contention) keeps retrying.
		if _, _, state := r.pairSnapshot(pairID, session); state == pairGone {
			return nil, encoderDone
		}
		next, err := start(ctx)
		if err == nil {
			r.logSinkReopened(jobID, attempt, feedErr)

			return next, encoderResume
		}
		if attempt == 1 || attempt%10 == 0 {
			r.log.Warn("device sink reopen failed; retrying",
				"job", jobID, "attempt", attempt, "err", err)
		}
		if !reopenPause(ctx, deviceReopenBackoff[min(attempt, len(deviceReopenBackoff))-1]) {
			return nil, encoderDone
		}
	}
}
