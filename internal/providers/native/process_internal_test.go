package native

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/engine"
)

// stubProc is a scriptable helperProc that makes procCache choreography
// observable without spawning real helper processes.
type stubProc struct {
	closeErr       error
	entered        chan struct{}
	release        chan struct{}
	enteredOnce    sync.Once
	aborted        atomic.Bool
	closed         atomic.Bool
	stale          atomic.Bool
	abortedAtClose atomic.Bool
}

func (p *stubProc) unusable() bool { return p.stale.Load() }

func (p *stubProc) abort() { p.aborted.Store(true) }

// close blocks on release when set, so reap concurrency is observable.
func (p *stubProc) close() error {
	p.enteredOnce.Do(func() {
		if p.entered != nil {
			close(p.entered)
		}
	})
	if p.release != nil {
		<-p.release
	}
	p.abortedAtClose.Store(p.aborted.Load())
	p.closed.Store(true)

	return p.closeErr
}

func newStubCache() *procCache[*stubProc] { return newProcCache[*stubProc]("test cache", nil) }

func spawnStub(context.Context, *slog.Logger) (*stubProc, error) { return &stubProc{}, nil }

func TestProcCacheKeepsOneWarmHelperPerKey(t *testing.T) {
	t.Parallel()

	cache := newStubCache()
	spawned := 0
	spawn := func(context.Context, *slog.Logger) (*stubProc, error) {
		spawned++

		return &stubProc{}, nil
	}

	first, err := cache.get(t.Context(), "a", spawn)
	if err != nil {
		t.Fatalf("first get: %v", err)
	}
	if again, getErr := cache.get(t.Context(), "a", spawn); getErr != nil || again != first {
		t.Fatalf("same-key get = (%p, %v), want the cached helper %p", again, getErr, first)
	}
	if other, getErr := cache.get(t.Context(), "b", spawn); getErr != nil || other == first {
		t.Fatalf("distinct-key get = (%p, %v), want a separate helper", other, getErr)
	}
	if spawned != 2 {
		t.Fatalf("spawn calls = %d, want one per key", spawned)
	}
	if err := cache.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestProcCacheReplacesAndReapsStaleHelpers(t *testing.T) {
	t.Parallel()

	cache := newStubCache()
	stale, err := cache.get(t.Context(), "a", spawnStub)
	if err != nil {
		t.Fatalf("first get: %v", err)
	}

	stale.stale.Store(true)
	replacement, err := cache.get(t.Context(), "a", spawnStub)
	if err != nil || replacement == stale {
		t.Fatalf("stale-key get = (%p, %v), want a replacement", replacement, err)
	}
	if !stale.closed.Load() {
		t.Fatal("stale helper was not reaped on replacement")
	}
	if err := cache.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestProcCacheSpawnsHelpersIntoTheCacheLifetime(t *testing.T) {
	t.Parallel()

	cache := newStubCache()
	requestCtx, cancel := context.WithCancel(t.Context())
	lifetimes := make(chan context.Context, 1)
	proc, err := cache.get(requestCtx, "a", func(life context.Context, _ *slog.Logger) (*stubProc, error) {
		lifetimes <- life

		return &stubProc{}, nil
	})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	spawnCtx := <-lifetimes

	cancel()
	if spawnCtx.Err() != nil {
		t.Fatal("request cancellation reached the helper lifetime context")
	}
	if again, getErr := cache.get(t.Context(), "a", func(context.Context, *slog.Logger) (*stubProc, error) {
		return nil, errors.New("respawned a live helper")
	}); getErr != nil || again != proc {
		t.Fatalf("get after request cancellation = (%p, %v), want the cached helper", again, getErr)
	}

	if err := cache.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if spawnCtx.Err() == nil {
		t.Fatal("Close did not cancel the helper lifetime context")
	}
	if !proc.closed.Load() {
		t.Fatal("Close did not reap the cached helper")
	}
}

func TestProcCacheCloseIsIdempotentAndReapsEveryHelper(t *testing.T) {
	t.Parallel()

	wantCleanup := errors.New("helper cleanup failed")
	cache := newStubCache()
	failing := &stubProc{closeErr: wantCleanup}
	clean := &stubProc{}
	cache.procs["failing"], cache.procs["clean"] = failing, clean

	closed := make(chan error, 2)
	go func() { closed <- cache.Close() }()
	go func() { closed <- cache.Close() }()
	for range 2 {
		if err := <-closed; !errors.Is(err, wantCleanup) {
			t.Fatalf("Close = %v, want cleanup failure", err)
		}
	}

	for _, proc := range []*stubProc{failing, clean} {
		if !proc.closed.Load() {
			t.Fatal("Close returned before every helper was reaped")
		}
		if !proc.abortedAtClose.Load() {
			t.Fatal("helper was reaped before the shutdown drain aborted it")
		}
	}

	if _, err := cache.get(t.Context(), "late", spawnStub); !errors.Is(err, ErrClosed) {
		t.Fatalf("get after Close = %v, want ErrClosed", err)
	}
}

func TestProcCacheDiscardReapsOutsideTheLock(t *testing.T) {
	t.Parallel()

	cache := newStubCache()
	blocked := &stubProc{entered: make(chan struct{}), release: make(chan struct{})}
	cache.procs["a"] = blocked

	discarded := make(chan error, 1)
	go func() { discarded <- cache.discard("a", blocked) }()
	<-blocked.entered

	// The entry leaves the map under the lock, but the reap runs unlocked, so
	// a concurrent Close and every other key keep making progress.
	if !cache.mu.TryLock() {
		t.Fatal("discard held the cache lock across the helper reap")
	}
	cache.mu.Unlock()
	if err := cache.Close(); err != nil {
		t.Fatalf("Close during a blocked discard: %v", err)
	}

	close(blocked.release)
	if err := <-discarded; err != nil {
		t.Fatalf("discard: %v", err)
	}

	current, superseded := &stubProc{}, &stubProc{}
	cache.procs["b"] = current
	if err := cache.discard("b", superseded); err != nil {
		t.Fatalf("discard superseded helper: %v", err)
	}
	if !superseded.closed.Load() || cache.procs["b"] != current {
		t.Fatal("discard must reap the superseded helper yet evict only its own entry")
	}
}

func TestProcCacheSpawnFailureSurfacesStaleCleanup(t *testing.T) {
	t.Parallel()

	wantSpawn := errors.New("spawn failed")
	wantCleanup := errors.New("stale cleanup failed")
	cache := newStubCache()
	stale := &stubProc{closeErr: wantCleanup}
	stale.stale.Store(true)
	cache.procs["a"] = stale

	_, err := cache.get(t.Context(), "a", func(context.Context, *slog.Logger) (*stubProc, error) {
		return nil, wantSpawn
	})
	if !errors.Is(err, wantSpawn) || !errors.Is(err, wantCleanup) {
		t.Fatalf("get = %v, want joined spawn and stale-cleanup failures", err)
	}
	if len(cache.procs) != 0 {
		t.Fatal("failed spawn left an entry cached")
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, getErr := cache.get(canceled, "a", func(context.Context, *slog.Logger) (*stubProc, error) {
		return nil, errors.New("canceled get reached spawn")
	}); !errors.Is(getErr, context.Canceled) {
		t.Fatalf("canceled get = %v, want context canceled", getErr)
	}
	if err := cache.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestWarmProcessCloseSurfacesTreeCleanupFailure pins the reap path Close
// shares across adapters: close returns only after the read pump reaped the
// child and reports the process-tree retirement failure.
func TestWarmProcessCloseSurfacesTreeCleanupFailure(t *testing.T) {
	pair, err := startPairProc(t.Context(), os.Args[0], "", fakeLang, "en", discardTestLogger())
	if err != nil {
		t.Fatalf("start helper: %v", err)
	}
	wantCleanup := errors.New("tree cleanup failed")
	pair.tree = &failingCloseTree{processTree: pair.tree, err: wantCleanup}

	if closeErr := pair.close(); !errors.Is(closeErr, wantCleanup) {
		t.Fatalf("close = %v, want the tree cleanup failure", closeErr)
	}
	if !pair.finished() {
		t.Fatal("close returned before the helper was reaped")
	}
}

// cacheAdapterHarness drives one warm-helper adapter through its public
// surface, so procCache behaviors shared by MT and TTS are pinned once.
// helper reports the cached process and its request gate for the warmed key.
type cacheAdapterHarness struct {
	warm      func(ctx context.Context) error
	roundTrip func(ctx context.Context, text string) error
	altTrip   func(ctx context.Context) error
	helper    func() (proc *warmProcess, gate chan struct{})
	count     func() int
	shutdown  func() error
	closedMsg string
}

type cacheAdapterEntry struct {
	open func(t *testing.T) *cacheAdapterHarness
	name string
}

func cacheAdapters() []cacheAdapterEntry {
	return []cacheAdapterEntry{{name: "mt", open: newMTCacheHarness}, {name: "tts", open: newTTSCacheHarness}}
}

func cacheSize[P helperProc](cache *procCache[P]) int {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	return len(cache.procs)
}

// newMTCacheHarness adapts the translator to the shared cache scenarios. Its
// trips use the en-US regional target, so every request also pins base-tag
// keying onto the warmed zz>en pair.
func newMTCacheHarness(t *testing.T) *cacheAdapterHarness {
	t.Helper()

	mt := newTestMT(t, &MTConfig{Bin: os.Args[0]})
	pair := func() *pairProc {
		mt.cache.mu.Lock()
		defer mt.cache.mu.Unlock()

		return mt.cache.procs[pairKey(fakeLang, "en")]
	}

	return &cacheAdapterHarness{
		warm:      func(ctx context.Context) error { return mt.Warm(ctx, fakeLang, "en") },
		roundTrip: func(ctx context.Context, text string) error { return translateTrip(ctx, mt, text) },
		altTrip: func(ctx context.Context) error {
			_, err := mt.Translate(ctx, engine.Segment{Text: "other", Lang: fakeLang}, "de")

			return err
		},
		helper: func() (proc *warmProcess, gate chan struct{}) {
			if cached := pair(); cached != nil {
				return cached.warmProcess, cached.gate
			}

			return nil, nil
		},
		count:     func() int { return cacheSize(mt.cache) },
		shutdown:  mt.Close,
		closedMsg: "provider is closed",
	}
}

func translateTrip(ctx context.Context, mt *MT, text string) error {
	got, err := mt.Translate(ctx, engine.Segment{Text: text, Lang: fakeLang}, "en-US")
	if err != nil {
		return err
	}
	if got != "mt:"+text {
		return fmt.Errorf("translation = %q, want %q", got, "mt:"+text)
	}

	return nil
}

// newTTSCacheHarness adapts the synthesizer to the shared cache scenarios,
// speaking the en-US regional target over the warmed en voice.
func newTTSCacheHarness(t *testing.T) *cacheAdapterHarness {
	t.Helper()

	tts := newTestTTS(t, &TTSConfig{Bin: os.Args[0], Rate: 16000})
	voice := func() *voiceProc {
		tts.cache.mu.Lock()
		defer tts.cache.mu.Unlock()

		return tts.cache.procs[fakeVoice]
	}

	return &cacheAdapterHarness{
		warm: func(ctx context.Context) error {
			return tts.Warm(ctx, "en", core.Voice{ID: fakeVoice, Lang: "en"})
		},
		roundTrip: func(ctx context.Context, text string) error { return synthesizeTrip(ctx, tts, text) },
		altTrip: func(ctx context.Context) error {
			_, err := speakTurn(ctx, tts, "en", core.Voice{ID: fakeVoice + "-b"}, "other")

			return err
		},
		helper: func() (proc *warmProcess, gate chan struct{}) {
			if cached := voice(); cached != nil {
				return cached.warmProcess, cached.gate
			}

			return nil, nil
		},
		count:     func() int { return cacheSize(tts.cache) },
		shutdown:  tts.Close,
		closedMsg: "provider is closed",
	}
}

// speakTurn runs one synthesis turn and returns its PCM with the terminal
// stream result.
func speakTurn(ctx context.Context, tts *TTS, to core.Lang, voice core.Voice, text string) ([]int16, error) {
	clause := make(chan string, 1)
	clause <- text
	close(clause)

	audio, err := tts.Speak(ctx, to, voice, clause)
	if err != nil {
		return nil, err
	}

	var got []int16
	for pcm := range audio.Audio() {
		got = append(got, pcm.Data...)
	}

	return got, audio.Err()
}

// synthesizeTrip validates one turn's PCM; a turn that fails after emitting
// audio is reported as the corruption itself, never as its stream error.
func synthesizeTrip(ctx context.Context, tts *TTS, text string) error {
	got, err := speakTurn(ctx, tts, "en-US", core.Voice{ID: fakeVoice}, text)
	if err != nil {
		if len(got) != 0 {
			return fmt.Errorf("failed turn emitted pcm %v", got)
		}

		return err
	}
	if !reflect.DeepEqual(got, fakeSamples) {
		return fmt.Errorf("pcm = %v, want %v", got, fakeSamples)
	}

	return nil
}

// TestNativeAdapterWarmHelperOutlivesItsFirstContext: Warm loads one keyed
// helper into the cache lifetime, so the warming request's context cannot
// kill it, regional targets reuse it and a second key gets its own process.
func TestNativeAdapterWarmHelperOutlivesItsFirstContext(t *testing.T) {
	for _, adapter := range cacheAdapters() {
		t.Run(adapter.name, func(t *testing.T) {
			assertWarmHelperOutlivesItsFirstContext(t, adapter.open(t))
		})
	}
}

func assertWarmHelperOutlivesItsFirstContext(t *testing.T, h *cacheAdapterHarness) {
	t.Helper()

	warmCtx, cancel := context.WithCancel(t.Context())
	if err := h.warm(warmCtx); err != nil {
		t.Fatalf("Warm: %v", err)
	}
	proc, _ := h.helper()
	if proc == nil {
		t.Fatal("Warm did not cache a helper process")
	}
	cancel()

	select {
	case <-proc.stop:
		t.Fatal("canceling the warm context stopped the provider-owned helper")
	case <-time.After(50 * time.Millisecond):
	}

	if err := h.roundTrip(t.Context(), "live"); err != nil {
		t.Fatalf("request after warm-context cancellation: %v", err)
	}
	if current, _ := h.helper(); current != proc {
		t.Fatal("regional targets did not share the warmed helper")
	}
	if got := h.count(); got != 1 {
		t.Fatalf("warm processes = %d, want 1 (key reused)", got)
	}

	if err := h.altTrip(t.Context()); err != nil {
		t.Fatalf("second-key request: %v", err)
	}
	if got := h.count(); got != 2 {
		t.Fatalf("warm processes = %d, want 2 (one per key)", got)
	}
}

func TestNativeAdapterCloseInterruptsActiveRequest(t *testing.T) {
	for _, adapter := range cacheAdapters() {
		t.Run(adapter.name, func(t *testing.T) {
			h := adapter.open(t)
			if err := h.warm(t.Context()); err != nil {
				t.Fatalf("Warm: %v", err)
			}
			proc, _ := h.helper()
			if proc == nil {
				t.Fatal("Warm did not cache a helper process")
			}

			result := make(chan error, 1)
			go func() { result <- h.roundTrip(context.Background(), fakeHang) }()
			waitStderrMarker(t, proc.stderr, fakeHangReady)

			if err := h.shutdown(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if err := <-result; err == nil {
				t.Fatal("active request succeeded after Close")
			}
			if !proc.finished() {
				t.Fatal("Close returned before the active helper was reaped")
			}
		})
	}
}

func TestNativeAdapterReplacesUnusableHelper(t *testing.T) {
	for _, adapter := range cacheAdapters() {
		t.Run(adapter.name, func(t *testing.T) {
			h := adapter.open(t)
			for _, test := range []struct {
				lose func(t *testing.T, h *cacheAdapterHarness, old *warmProcess)
				name string
			}{
				{name: "canceled hanging request", lose: loseHelperToCanceledHang},
				{name: "natural exit", lose: loseHelperToNaturalExit},
			} {
				t.Run(test.name, func(t *testing.T) {
					if err := h.roundTrip(t.Context(), "warm"); err != nil {
						t.Fatalf("warm request: %v", err)
					}
					old, _ := h.helper()
					test.lose(t, h, old)

					if err := h.roundTrip(t.Context(), "restarted"); err != nil {
						t.Fatalf("request after helper loss: %v", err)
					}
					if current, _ := h.helper(); current == old {
						t.Fatal("unusable helper remained cached")
					}
				})
			}
		})
	}
}

// loseHelperToCanceledHang cancels a request the helper never answers, so the
// adapter must abort and discard the helper.
func loseHelperToCanceledHang(t *testing.T, h *cacheAdapterHarness, _ *warmProcess) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if err := h.roundTrip(ctx, fakeHang); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hanging request error = %v, want deadline exceeded", err)
	}
}

// loseHelperToNaturalExit lets the helper deliver one full reply and exit on
// its own.
func loseHelperToNaturalExit(t *testing.T, h *cacheAdapterHarness, old *warmProcess) {
	t.Helper()

	if err := h.roundTrip(t.Context(), fakeExitAfter); err != nil {
		t.Fatalf("final reply before helper exit: %v", err)
	}
	waitDone(t, old.done)
}

func TestNativeAdapterCanceledWaitKeepsActiveHelper(t *testing.T) {
	for _, adapter := range cacheAdapters() {
		t.Run(adapter.name, func(t *testing.T) {
			assertCanceledWaitKeepsActiveHelper(t, adapter.open(t))
		})
	}
}

func assertCanceledWaitKeepsActiveHelper(t *testing.T, h *cacheAdapterHarness) {
	t.Helper()

	if err := h.roundTrip(t.Context(), "warm"); err != nil {
		t.Fatalf("warm request: %v", err)
	}
	proc, gate := h.helper()
	if proc == nil || gate == nil {
		t.Fatal("warm request did not cache a helper")
	}

	<-gate
	held := true
	defer func() {
		if held {
			gate <- struct{}{}
		}
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err := h.roundTrip(ctx, "queued"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued request error = %v, want deadline exceeded", err)
	}
	if proc.unusable() {
		t.Fatal("canceling a queued request stopped the active helper")
	}

	gate <- struct{}{}
	held = false
	if err := h.roundTrip(t.Context(), "still-warm"); err != nil {
		t.Fatalf("request after queued cancellation: %v", err)
	}
	if current, _ := h.helper(); current != proc {
		t.Fatal("queued cancellation replaced the active helper")
	}
}

// TestNativeAdapterSurfacesHelperFailures: the decode or process diagnostic
// reaches the caller and the next request gets a fresh helper.
func TestNativeAdapterSurfacesHelperFailures(t *testing.T) {
	for _, test := range []struct {
		open    func(t *testing.T) *cacheAdapterHarness
		name    string
		clause  string
		want    string
		bounded bool
	}{
		{name: "mt malformed json", open: newMTCacheHarness, clause: fakeBadJSON, want: "decode native mt response"},
		{name: "mt stderr tail", open: newMTCacheHarness, clause: fakeCrash, want: "fake mt crash"},
		{name: "mt oversized reply", open: newMTCacheHarness, clause: fakeOversized, want: "token too long", bounded: true},
		{name: "tts malformed json", open: newTTSCacheHarness, clause: fakeBadJSON, want: "decode native tts response"},
		{name: "tts corrupt base64", open: newTTSCacheHarness, clause: fakeBadBase64, want: "decode native tts audio"},
		{name: "tts odd pcm", open: newTTSCacheHarness, clause: fakeOddPCM, want: "odd PCM byte count"},
		{name: "tts stderr tail", open: newTTSCacheHarness, clause: fakeCrash, want: "fake tts crash"},
		{name: "tts oversized reply", open: newTTSCacheHarness, clause: fakeOversized, want: "token too long", bounded: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := test.open(t)
			started := time.Now()
			err := h.roundTrip(t.Context(), test.clause)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("corrupt reply error = %v, want %q", err, test.want)
			}
			if elapsed := time.Since(started); test.bounded && elapsed > 2*time.Second {
				t.Fatalf("corrupt reply took %v; helper likely wedged in Wait", elapsed)
			}
			if err := h.roundTrip(t.Context(), "recovered"); err != nil {
				t.Fatalf("request after corrupt reply: %v", err)
			}
		})
	}
}

// TestNativeHelperWriteFailureWaitsForDiagnostic: a broken request pipe
// surfaces the helper's stderr diagnostic within the bounded process stop.
func TestNativeHelperWriteFailureWaitsForDiagnostic(t *testing.T) {
	for _, test := range []struct {
		start  func(t *testing.T) (proc *warmProcess, request func(ctx context.Context) error)
		name   string
		marker string
	}{
		{name: "mt", marker: fakeMTReject, start: startRejectingPairProc},
		{name: "tts", marker: fakeTTSReject, start: startRejectingVoiceProc},
	} {
		t.Run(test.name, func(t *testing.T) {
			proc, request := test.start(t)
			waitStderrMarker(t, proc.stderr, test.marker)

			started := time.Now()
			err := request(t.Context())
			if err == nil || !strings.Contains(err.Error(), test.marker) {
				t.Fatalf("request error = %v, want helper stderr", err)
			}
			if elapsed := time.Since(started); elapsed > 2*time.Second {
				t.Fatalf("request took %v; failed helper was not stopped promptly", elapsed)
			}
		})
	}
}

func startRejectingPairProc(t *testing.T) (proc *warmProcess, request func(ctx context.Context) error) {
	t.Helper()

	pair, err := startPairProc(t.Context(), os.Args[0], "", fakeRejectMT, "en", discardTestLogger())
	if err != nil {
		t.Fatalf("start pair: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := pair.close(); closeErr != nil {
			t.Errorf("close pair: %v", closeErr)
		}
	})

	return pair.warmProcess, func(ctx context.Context) error {
		_, translateErr := pair.translate(ctx, "ciao")

		return translateErr
	}
}

func startRejectingVoiceProc(t *testing.T) (proc *warmProcess, request func(ctx context.Context) error) {
	t.Helper()

	voice, err := startVoiceProc(t.Context(), os.Args[0], "", fakeRejectTTS, 16000, discardTestLogger())
	if err != nil {
		t.Fatalf("start voice: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := voice.close(); closeErr != nil {
			t.Errorf("close voice: %v", closeErr)
		}
	})

	return voice.warmProcess, func(ctx context.Context) error {
		return voice.synth(ctx, "hello", make(chan core.PCM, 1))
	}
}

func TestNativeAdapterRejectsWorkBeforeSpawnAndAfterClose(t *testing.T) {
	for _, adapter := range cacheAdapters() {
		t.Run(adapter.name, func(t *testing.T) {
			h := adapter.open(t)
			canceled, cancel := context.WithCancel(t.Context())
			cancel()
			if err := h.roundTrip(canceled, "x"); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled request error = %v, want context canceled", err)
			}
			if got := h.count(); got != 0 {
				t.Fatalf("warm processes = %d, want none", got)
			}

			if err := h.shutdown(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			err := h.roundTrip(t.Context(), "late")
			if err == nil || !strings.Contains(err.Error(), h.closedMsg) {
				t.Fatalf("request after Close = %v, want %q", err, h.closedMsg)
			}
		})
	}
}
