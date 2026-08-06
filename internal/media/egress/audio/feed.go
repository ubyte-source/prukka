package audio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/ubyte-source/prukka/internal/besteffort"
	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
	"github.com/ubyte-source/prukka/internal/media/deviceurl"
)

// errDeviceReconfigured forces a sink reopen: a long-lived audiotoolbox queue
// dies silently when another application switches the device's nominal sample
// rate (OBS does on attach).
var errDeviceReconfigured = errors.New("device output reconfigured")

const defaultDeviceWatchInterval = 2 * time.Second

// pairRebuildPoll paces the wait for a restarted lane to publish its rebuilt
// template; lane restarts re-warm providers, so the wait can span seconds.
const pairRebuildPoll = 100 * time.Millisecond

func (r *Registry) deviceWatchInterval() time.Duration {
	if r.timing.watch > 0 {
		return r.timing.watch
	}

	return defaultDeviceWatchInterval
}

// feeder is one paced feed from a mixer cursor into a sink.
type feeder struct {
	out       io.WriteCloser
	mixer     pipeline.Playout
	target    string
	observers []func(core.PCM)
	pacing    feedConfig
	sink      sinkKind
}

// deviceBaseline is the fingerprint a watched feed compares against; unknown
// means the watcher must still adopt the first fingerprint it reads.
type deviceBaseline struct {
	stamp string
	known bool
}

// feedWatched runs the feed under the device-configuration watcher when it
// drives a watchable device output; every other feed runs untouched.
func (r *Registry) feedWatched(ctx context.Context, f *feeder) error {
	if f.sink != sinkDevice || r.configStamp == nil {
		return f.run(ctx)
	}
	stamp, known := r.configStamp(f.target)
	base := deviceBaseline{stamp: stamp, known: known}
	if !base.known && deviceurl.AudioLabel(f.target) == "" {
		return f.run(ctx)
	}

	watchCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	go r.watchDeviceConfig(watchCtx, cancel, f.target, base)

	err := f.run(watchCtx)
	if errors.Is(context.Cause(watchCtx), errDeviceReconfigured) && ctx.Err() == nil {
		return errDeviceReconfigured
	}

	return err
}

// watchDeviceConfig cancels the feed with a reopen cause when the target's
// fingerprint changes; a target that stops resolving is left to the feed's
// own write errors.
func (r *Registry) watchDeviceConfig(
	ctx context.Context, cancel context.CancelCauseFunc, target string, base deviceBaseline,
) {
	stamp := r.configStamp
	ticker := time.NewTicker(r.deviceWatchInterval())
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
		if !base.known {
			base = deviceBaseline{stamp: current, known: true}

			continue
		}
		if current != base.stamp {
			cancel(errDeviceReconfigured)

			return
		}
	}
}

// run paces mixed PCM into the sink until ctx ends, then closes it; the
// enclosing job owns the mixer cursor across sink reopenings.
func (f *feeder) run(ctx context.Context) error {
	ticker := time.NewTicker(f.pacing.quantum)
	defer ticker.Stop()

	return f.ticks(ctx, ticker.C)
}

func (f *feeder) ticks(ctx context.Context, ticks <-chan time.Time) error {
	// Cancellation closes the sink concurrently with a blocked Write: a
	// synchronous device queue (notably WASAPI) is released no other way.
	closeFeed := sync.OnceValue(f.out.Close)
	stopClose := context.AfterFunc(ctx, func() { besteffort.Ignore(closeFeed()) })
	err := f.pace(ctx, ticks)
	stopClose()
	if closeErr := closeFeed(); closeErr != nil && err == nil {
		err = fmt.Errorf("close encoder feed: %w", closeErr)
	}

	return err
}

func (f *feeder) pace(ctx context.Context, ticks <-chan time.Time) error {
	silence := make([]byte, f.pacing.samples*2)
	samples := make([]int16, f.pacing.samples)
	encoded := make([]byte, 0, f.pacing.samples*2)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticks:
		}

		payload, ready := silence, f.sink.fillsIdleTicks()
		pcm, status := f.mixer.NextInto(samples)
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

		if err := f.write(payload, pcm, status); err != nil {
			return err
		}
	}
}

// write reports PCM only after the sink accepted the complete quantum; a
// short write is a failure even without an explicit error.
func (f *feeder) write(payload []byte, pcm core.PCM, status pipeline.PullStatus) error {
	written, err := f.out.Write(payload)
	if err != nil {
		return fmt.Errorf("feed encoder: %w", err)
	}
	if written != len(payload) {
		return fmt.Errorf("feed encoder: %w", io.ErrShortWrite)
	}
	if status != pipeline.PullReady {
		return nil
	}

	for _, observe := range f.observers {
		observe(pcm)
	}

	return nil
}
