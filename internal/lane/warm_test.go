package lane

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/session"
	"github.com/ubyte-source/prukka/internal/dispatch"
)

type recordingMTWarmer struct{ calls chan string }

func (w recordingMTWarmer) Warm(_ context.Context, from, to core.Lang) error {
	w.calls <- string(from) + ">" + string(to)

	return nil
}

type recordingTTSWarmer struct{ calls chan string }

func (w recordingTTSWarmer) Warm(_ context.Context, to core.Lang, voice core.Voice) error {
	w.calls <- string(to) + ">" + voice.ID

	return nil
}

type blockingMTWarmer struct{}

func (blockingMTWarmer) Warm(ctx context.Context, _, _ core.Lang) error {
	<-ctx.Done()

	return ctx.Err()
}

type failingMTWarmer struct{ err error }

func (w failingMTWarmer) Warm(context.Context, core.Lang, core.Lang) error { return w.err }

type concurrencyMTWarmer struct {
	release chan struct{}
	started chan struct{}
	mu      sync.Mutex
	active  int
	maximum int
}

func (w *concurrencyMTWarmer) Warm(ctx context.Context, _, _ core.Lang) error {
	w.mu.Lock()
	w.active++
	w.maximum = max(w.maximum, w.active)
	w.mu.Unlock()
	w.started <- struct{}{}
	defer func() {
		w.mu.Lock()
		w.active--
		w.mu.Unlock()
	}()

	select {
	case <-w.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *concurrencyMTWarmer) maxConcurrent() int {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.maximum
}

// TestProvidersWarmExactPairAndSelectedVoice runs on a broadcast session: both
// profiles pay model initialization before capture, or the first committed
// clause would.
func TestProvidersWarmExactPairAndSelectedVoice(t *testing.T) {
	t.Parallel()

	pool := dispatch.New(2, 4)
	defer pool.Close()
	mtCalls := make(chan string, 4)
	ttsCalls := make(chan string, 4)
	s := &session.Session{
		Slug:       "warm-broadcast",
		Profile:    session.ProfileBroadcast,
		Langs:      []core.Lang{"it", "en"},
		SourceLang: "it",
		DubLangs:   session.DubOnly("en"),
	}
	voices := []core.Voice{{ID: "voice-en", Lang: "en"}, {ID: "voice-it", Lang: "it"}}
	if err := warmProviders(
		t.Context(), providerWarmTimeout, pool, s, recordingMTWarmer{calls: mtCalls},
		recordingTTSWarmer{calls: ttsCalls}, voices, nil,
	); err != nil {
		t.Fatalf("warmProviders: %v", err)
	}
	if got := <-mtCalls; got != "it>en" {
		t.Fatalf("warmed pair = %q, want it>en", got)
	}
	if got := <-ttsCalls; got != "en>voice-en" {
		t.Fatalf("warmed voice = %q, want en>voice-en", got)
	}
	select {
	case extra := <-mtCalls:
		t.Fatalf("extra MT warm %q", extra)
	default:
	}
	select {
	case extra := <-ttsCalls:
		t.Fatalf("extra TTS warm %q", extra)
	default:
	}
}

func TestProviderWarmupLogsStructuredDuration(t *testing.T) {
	t.Parallel()

	pool := dispatch.New(2, 4)
	defer pool.Close()
	s := &session.Session{
		Slug:       "observable-warm",
		Profile:    session.ProfileCall,
		Source:     core.SourceSpec{URL: "file:///Users/alice/private.wav?token=source-secret"},
		Langs:      []core.Lang{"en"},
		SourceLang: "it",
		DubLangs:   session.DubOnly("en"),
	}
	var logs bytes.Buffer
	observer := startupObserverForTest(&logs, s, 125*time.Millisecond)
	voices := []core.Voice{{ID: "voice-secret", Lang: "en"}}
	if err := warmProviders(
		t.Context(), providerWarmTimeout, pool, s, recordingMTWarmer{calls: make(chan string, 1)},
		recordingTTSWarmer{calls: make(chan string, 1)}, voices, observer,
	); err != nil {
		t.Fatalf("warmProviders: %v", err)
	}

	entries := decodeStartupLogs(t, logs.Bytes())
	assertStartupPhases(t, entries, "providers_warming", "providers_ready")
	if entries[0]["mt_tasks"] != float64(1) || entries[0]["tts_tasks"] != float64(1) {
		t.Fatalf("provider task counts = %v, want one MT and one TTS", entries[0])
	}
	if got := entries[1]["phase_duration_ms"]; got != float64(125) {
		t.Fatalf("provider warm duration = %v, want 125 ms", got)
	}
	if got := entries[1]["startup_duration_ms"]; got != float64(250) {
		t.Fatalf("startup duration = %v, want 250 ms", got)
	}
	assertLogOmits(t, logs.String(), "/Users/alice", "source-secret", "voice-secret")
}

func TestProviderWarmupFailureLogOmitsProviderDetails(t *testing.T) {
	t.Parallel()

	pool := dispatch.New(1, 1)
	defer pool.Close()
	s := &session.Session{
		Slug:       "safe-warm-failure",
		Profile:    session.ProfileCall,
		Langs:      []core.Lang{"en"},
		SourceLang: "it",
	}
	var logs bytes.Buffer
	observer := startupObserverForTest(&logs, s, 10*time.Millisecond)
	warmErr := errors.New("open /Users/alice/models/private.bin: token=provider-secret")
	err := warmProviders(
		t.Context(), providerWarmTimeout, pool, s, failingMTWarmer{err: warmErr}, nil, nil, observer,
	)
	if !errors.Is(err, warmErr) {
		t.Fatalf("warmProviders error = %v, want provider failure", err)
	}

	entries := decodeStartupLogs(t, logs.Bytes())
	if len(entries) != 2 || entries[1]["phase"] != "providers_failed" {
		t.Fatalf("provider failure logs = %v, want bounded failure phase", entries)
	}
	assertLogOmits(t, logs.String(), "/Users/alice", "private.bin", "provider-secret")
}

func TestProviderWarmupHasStartupDeadline(t *testing.T) {
	t.Parallel()

	pool := dispatch.New(1, 1)
	defer pool.Close()
	s := &session.Session{
		Profile:    session.ProfileCall,
		Langs:      []core.Lang{"en"},
		SourceLang: "it",
		DubLangs:   session.DubOnly("en"),
	}
	started := time.Now()
	err := warmProviders(
		t.Context(), 20*time.Millisecond, pool, s, blockingMTWarmer{}, nil, nil, nil,
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("warmup error = %v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("warmup deadline returned after %s", elapsed)
	}
}

func TestProviderWarmupUsesSharedWorkerBound(t *testing.T) {
	t.Parallel()

	pool := dispatch.New(2, 1)
	defer pool.Close()
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()
	warmer := &concurrencyMTWarmer{release: release, started: make(chan struct{}, 4)}
	s := &session.Session{
		Profile:    session.ProfileCall,
		Langs:      []core.Lang{"en", "de", "fr", "es"},
		SourceLang: "it",
	}

	result := make(chan error, 1)
	go func() {
		result <- warmProviders(t.Context(), time.Second, pool, s, warmer, nil, nil, nil)
	}()
	for range 2 {
		select {
		case <-warmer.started:
		case <-time.After(time.Second):
			t.Fatal("warmup did not use both available workers")
		}
	}
	select {
	case <-warmer.started:
		t.Fatal("warmup exceeded the shared two-worker limit")
	case <-time.After(50 * time.Millisecond):
	}
	releaseAll()
	if err := <-result; err != nil {
		t.Fatalf("warmProvidersWithin: %v", err)
	}
	if got := warmer.maxConcurrent(); got != 2 {
		t.Fatalf("maximum concurrent warmups = %d, want 2", got)
	}
}
