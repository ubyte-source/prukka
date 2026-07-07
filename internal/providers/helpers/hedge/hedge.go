// Package hedge cuts STT tail latency: past the observed p95 an identical
// backup fires and the first answer wins.
package hedge

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
)

// Latency window: p95 over the last windowSize successful calls; no hedge
// fires until minSamples exist, and never before floor.
const (
	windowSize = 64
	minSamples = 8
)

// STT decorates a transcriber with p95 hedging; wrap the retry layer, not
// the raw client.
type STT struct {
	inner core.STT
	lat   *window
	floor time.Duration
}

// NewSTT wires the decorator; floor is the minimum delay before a backup.
func NewSTT(inner core.STT, floor time.Duration) *STT {
	return &STT{inner: inner, lat: &window{}, floor: floor}
}

// attempt is one racer's outcome.
type attempt struct {
	err error
	t   core.Transcript
}

// Transcribe implements core.STT. The winner's latency feeds the window;
// the loser is canceled through the shared race context.
func (s *STT) Transcribe(ctx context.Context, u *core.Utterance, hint core.Lang) (core.Transcript, error) {
	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan attempt, 2)
	start := time.Now()

	launch := func() {
		t, err := s.inner.Transcribe(raceCtx, u, hint)
		results <- attempt{t: t, err: err}
	}

	go launch()

	backup, stop := s.backupTimer()
	defer stop()

	var firstErr error

	for inFlight := 1; ; {
		select {
		case <-backup:
			inFlight++

			go launch()
		case r := <-results:
			if r.err == nil {
				s.lat.record(time.Since(start))

				return r.t, nil
			}

			if firstErr == nil {
				firstErr = r.err
			}

			inFlight--
			if inFlight == 0 {
				return core.Transcript{}, firstErr
			}
		}
	}
}

// backupTimer arms the hedge at max(p95, floor); with too few samples the
// channel is nil and never fires.
func (s *STT) backupTimer() (c <-chan time.Time, stop func()) {
	p95, ok := s.lat.p95()
	if !ok {
		return nil, func() {}
	}

	if p95 < s.floor {
		p95 = s.floor
	}

	t := time.NewTimer(p95)

	return t.C, func() { t.Stop() }
}

// window is a sliding sample set of successful-call latencies.
type window struct {
	samples []time.Duration
	next    int
	mu      sync.Mutex
}

func (w *window) record(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(w.samples) < windowSize {
		w.samples = append(w.samples, d)

		return
	}

	w.samples[w.next] = d
	w.next = (w.next + 1) % windowSize
}

func (w *window) p95() (time.Duration, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(w.samples) < minSamples {
		return 0, false
	}

	sorted := slices.Clone(w.samples)
	slices.Sort(sorted)

	return sorted[len(sorted)*95/100], true
}
