package audio

// The paced PCM feed that drives every encoder and device sink, plus the
// device-output configuration watcher that reopens a sink wedged by another
// application switching the device's nominal rate.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
)

// Device-output configuration watching. A long-lived audiotoolbox queue
// dies silently when another application switches the device's nominal
// sample rate (OBS does on attach); the watcher fingerprints the target at
// feed start and forces a sink reopen when the fingerprint changes.
var (
	errDeviceReconfigured = errors.New("device output reconfigured")

	// deviceWatchOverride tightens the poll in tests; zero keeps the default.
	deviceWatchOverride atomic.Int64
)

const defaultDeviceWatchInterval = 2 * time.Second

// pairRebuildPoll paces the wait for a restarted lane to publish its rebuilt
// mixers; lane restarts re-warm providers, so the wait can span seconds.
const pairRebuildPoll = 100 * time.Millisecond

func deviceWatchInterval() time.Duration {
	if v := deviceWatchOverride.Load(); v > 0 {
		return time.Duration(v)
	}

	return defaultDeviceWatchInterval
}

// feedWatched runs feed under the device-configuration watcher when the job
// drives a watchable device output; every other job feeds untouched.
func (r *Registry) feedWatched(
	ctx context.Context, in io.WriteCloser, mixer pipeline.Playout, fill bool,
	pacing feedConfig, target string, observers ...func(core.PCM),
) error {
	stamp := r.configStamp
	if !fill || stamp == nil {
		return feed(ctx, in, mixer, fill, pacing, observers...)
	}
	base, hasBaseline := stamp(target)
	if !hasBaseline && ffmpeg.DeviceTargetLabel(target) == "" {
		return feed(ctx, in, mixer, fill, pacing, observers...)
	}

	watchCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	go watchDeviceConfig(watchCtx, cancel, stamp, target, base, hasBaseline)

	err := feed(watchCtx, in, mixer, fill, pacing, observers...)
	if errors.Is(context.Cause(watchCtx), errDeviceReconfigured) && ctx.Err() == nil {
		return errDeviceReconfigured
	}

	return err
}

// watchDeviceConfig cancels the feed with a reopen cause when the target's
// fingerprint changes; a target that stops resolving is left to the feed's
// own write errors.
func watchDeviceConfig(
	ctx context.Context, cancel context.CancelCauseFunc,
	stamp func(string) (string, bool), target, base string, hasBaseline bool,
) {
	ticker := time.NewTicker(deviceWatchInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		current, ok := stamp(target)
		if !ok {
			continue
		}
		if !hasBaseline {
			base, hasBaseline = current, true

			continue
		}
		if current != base {
			cancel(errDeviceReconfigured)

			return
		}
	}
}

// feed paces mixed PCM into the encoder until ctx ends, then closes the
// writer. The enclosing job or stream owns the mixer cursor across writer
// reopenings and releases it only after the complete output lifecycle ends.
func feed(
	ctx context.Context, in io.WriteCloser, mixer pipeline.Playout, fill bool, pacing feedConfig,
	observers ...func(core.PCM),
) error {
	ticker := time.NewTicker(pacing.quantum)
	defer ticker.Stop()

	return feedTicks(ctx, in, mixer, fill, pacing, ticker.C, observers...)
}

func feedTicks(
	ctx context.Context, in io.WriteCloser, mixer pipeline.Playout, fill bool,
	pacing feedConfig, ticks <-chan time.Time, observers ...func(core.PCM),
) error {
	owned := newFeedWriteCloser(in)
	stopClose := context.AfterFunc(ctx, func() {
		// The normal owner below observes the same cached close result.
		if owned.Close() != nil {
			return
		}
	})
	err := paceTicks(ctx, owned, mixer, fill, pacing, ticks, observers...)
	stopClose()
	if closeErr := owned.Close(); closeErr != nil && err == nil {
		err = fmt.Errorf("close encoder feed: %w", closeErr)
	}

	return err
}

// feedWriteCloser gives the paced feed one idempotent close owner. Context
// cancellation invokes Close concurrently with a blocked Write so synchronous
// device queues (notably WASAPI) are released; the normal exit path then waits
// for that same close instead of racing a second drain/reap.
type feedWriteCloser struct {
	io.WriteCloser

	closed   chan struct{}
	closeErr error
	once     sync.Once
}

func newFeedWriteCloser(writer io.WriteCloser) *feedWriteCloser {
	return &feedWriteCloser{WriteCloser: writer, closed: make(chan struct{})}
}

func (w *feedWriteCloser) Close() error {
	w.once.Do(func() {
		w.closeErr = w.WriteCloser.Close()
		close(w.closed)
	})
	<-w.closed

	return w.closeErr
}

func paceTicks(
	ctx context.Context, in io.Writer, mixer pipeline.Playout, fill bool,
	pacing feedConfig, ticks <-chan time.Time, observers ...func(core.PCM),
) error {
	silence := make([]byte, pacing.samples*2)
	samples := make([]int16, pacing.samples)
	encoded := make([]byte, 0, pacing.samples*2)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticks:
		}

		payload, ready := silence, fill
		pcm, status := mixer.NextInto(samples)
		if status == pipeline.PullEOF {
			return nil
		}
		if status == pipeline.PullReady {
			encoded = pipeline.AppendS16LE(encoded[:0], pcm.Data)
			payload, ready = encoded, true
		}
		if !ready {
			continue
		}

		if err := writePacedPayload(in, payload, pcm, status, observers); err != nil {
			return err
		}
	}
}

// writePacedPayload reports PCM only after the encoder accepted the complete
// quantum. A short write is a failed quantum even when the writer reports no
// explicit error.
func writePacedPayload(
	in io.Writer, payload []byte, pcm core.PCM, status pipeline.PullStatus,
	observers []func(core.PCM),
) error {
	written, err := in.Write(payload)
	if err != nil {
		return fmt.Errorf("feed encoder: %w", err)
	}
	if written != len(payload) {
		return fmt.Errorf("feed encoder: %w", io.ErrShortWrite)
	}
	if status != pipeline.PullReady {
		return nil
	}

	for _, observe := range observers {
		observe(pcm)
	}

	return nil
}
