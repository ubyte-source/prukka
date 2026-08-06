package audio

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ubyte-source/prukka/internal/besteffort"
)

// defaultDeviceReopenBackoff paces device-sink reopen attempts; the last step
// repeats.
func defaultDeviceReopenBackoff() []time.Duration {
	return []time.Duration{
		250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 5 * time.Second,
	}
}

// deviceReopenBackoff is this registry's reopen schedule; a configured retry
// pause REPLACES the ramp rather than starting it.
func (r *Registry) deviceReopenBackoff() []time.Duration {
	if r.timing.retry > 0 {
		return []time.Duration{r.timing.retry}
	}

	return defaultDeviceReopenBackoff()
}

// defaultDeviceWriteStall bounds one quantum write into a device sink: a
// healthy audiotoolbox process drains a 20-100ms quantum immediately, while a
// queue that wedges with its process alive blocks Write forever with no error.
const defaultDeviceWriteStall = 3 * time.Second

func (r *Registry) deviceWriteStallTimeout() time.Duration {
	if r.timing.stall > 0 {
		return r.timing.stall
	}

	return defaultDeviceWriteStall
}

// stallGuard closes a device sink whose single Write makes no progress within
// the stall timeout; closing the pipe unblocks the stuck Write with an error
// the job's device recovery turns into a fresh sink.
type stallGuard struct {
	w          io.WriteCloser
	done       chan struct{}
	closeOnce  func() error
	timeout    time.Duration
	writeStart atomic.Int64 // unix nanos of the in-flight write; 0 when idle
}

func newStallGuard(w io.WriteCloser, timeout time.Duration) *stallGuard {
	g := &stallGuard{w: w, done: make(chan struct{}), timeout: timeout}
	g.closeOnce = sync.OnceValue(g.sever)
	go g.watch()

	return g
}

func guardedDeviceStart(
	start func(context.Context) (io.WriteCloser, error), timeout time.Duration,
) func(context.Context) (io.WriteCloser, error) {
	return func(ctx context.Context) (io.WriteCloser, error) {
		w, err := start(ctx)
		if err != nil {
			return nil, err
		}

		return newStallGuard(w, timeout), nil
	}
}

func (g *stallGuard) Write(p []byte) (int, error) {
	g.writeStart.Store(time.Now().UnixNano())
	defer g.writeStart.Store(0)

	return g.w.Write(p)
}

func (g *stallGuard) sever() error {
	close(g.done)

	return g.w.Close()
}

// Close is idempotent: the watcher and the feed may both close on a stall.
func (g *stallGuard) Close() error { return g.closeOnce() }

// watch severs the sink when an in-flight write stalls past the timeout;
// polling at half the timeout bounds detection latency without timer churn.
func (g *stallGuard) watch() {
	ticker := time.NewTicker(g.timeout / 2)
	defer ticker.Stop()

	for {
		select {
		case <-g.done:
			return
		case <-ticker.C:
		}
		started := g.writeStart.Load()
		if started != 0 && time.Since(time.Unix(0, started)) > g.timeout {
			// Nobody to report a close failure to; Close returns the same result.
			besteffort.Ignore(g.closeOnce())

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

func (r *Registry) logSinkReopened(label string, attempt int, feedErr error) {
	switch {
	case errors.Is(feedErr, errDeviceReconfigured):
		r.log.Info("device output reconfigured; encoder reopened", "job", label)
	case feedErr != nil:
		r.log.Warn("device sink reopened after error",
			"job", label, "attempt", attempt, "err", feedErr)
	}
}

// reopenDeviceSink reopens a device sink, retrying with backoff until the open
// succeeds or the job context ends. After an encoder death the first attempt
// waits one backoff step: an instant reopen can beat coreaudiod's reap of the
// dead HAL client and produce a queue that accepts PCM but never reaches the
// device.
func (r *Registry) reopenDeviceSink(
	ctx context.Context, id jobID, feedErr error,
	start func(context.Context) (io.WriteCloser, error),
) (io.WriteCloser, encoderVerdict) {
	backoff := r.deviceReopenBackoff()
	if feedErr != nil && !errors.Is(feedErr, errDeviceReconfigured) &&
		!reopenPause(ctx, backoff[0]) {
		return nil, encoderDone
	}

	label := id.String()
	for attempt := 1; ; attempt++ {
		// A perpetual reopen would hang WaitPlayout, which waits on this job's
		// done channel; pairBusy (lock contention) keeps retrying.
		if _, state := r.live.pairSnapshot(id.pair); state == pairGone {
			return nil, encoderDone
		}
		next, err := start(ctx)
		if err == nil {
			r.logSinkReopened(label, attempt, feedErr)

			return next, encoderResume
		}
		if attempt == 1 || attempt%10 == 0 {
			r.log.Warn("device sink reopen failed; retrying",
				"job", label, "attempt", attempt, "err", err)
		}
		if !reopenPause(ctx, backoff[min(attempt, len(backoff))-1]) {
			return nil, encoderDone
		}
	}
}
