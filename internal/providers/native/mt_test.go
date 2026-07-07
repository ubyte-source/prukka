package native

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/engine"
)

// translateOne renders one source text from the fake language to a target.
func translateOne(t *testing.T, mt *MT, text string, to core.Lang) string {
	t.Helper()

	got, err := mt.Translate(t.Context(), engine.Segment{Text: text, Lang: fakeLang}, to)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	return got
}

func newTestMT(t *testing.T, cfg *MTConfig) *MT {
	t.Helper()

	mt := NewMT(cfg)
	t.Cleanup(func() {
		if err := mt.Close(); err != nil {
			t.Errorf("close test translator: %v", err)
		}
	})

	return mt
}

func TestMTTranslates(t *testing.T) {
	t.Parallel()

	mt := newTestMT(t, &MTConfig{Bin: os.Args[0]})

	if got := translateOne(t, mt, "ciao", core.Lang("en")); got != "mt:ciao" {
		t.Fatalf("translation = %q, want %q", got, "mt:ciao")
	}
}

func TestMTEnforcesDeclaredPairs(t *testing.T) {
	t.Parallel()

	mt := newTestMT(t, &MTConfig{Bin: filepath.Join(t.TempDir(), "missing"), Pairs: []engine.LanguagePair{
		{From: "it", To: "en"},
	}})
	if !mt.Supports("it-IT", "en-US") || mt.Supports("en", "it") {
		t.Fatal("Supports did not apply the directed base-language capability")
	}
	_, err := mt.Translate(t.Context(), engine.Segment{Text: "hello", Lang: "en"}, "it")
	if err == nil || !strings.Contains(err.Error(), "model unavailable for en to it") {
		t.Fatalf("unsupported Translate error = %v", err)
	}
}

func TestMTReusesWarmProcessPerPair(t *testing.T) {
	t.Parallel()

	mt := newTestMT(t, &MTConfig{Bin: os.Args[0]})

	translateOne(t, mt, "uno", core.Lang("en"))
	translateOne(t, mt, "due", core.Lang("en"))

	if got := pairCount(mt); got != 1 {
		t.Fatalf("warm processes = %d, want 1 (pair reused)", got)
	}
}

func TestMTWarmProcessOutlivesFirstCallContext(t *testing.T) {
	mt := newTestMT(t, &MTConfig{Bin: os.Args[0]})
	ctx, cancel := context.WithCancel(t.Context())

	got, err := mt.Translate(ctx, engine.Segment{Text: "first", Lang: fakeLang}, "en-US")
	if err != nil || got != "mt:first" {
		t.Fatalf("first translation = %q, %v", got, err)
	}
	proc := mt.procs[pairKey(fakeLang, "en")]
	cancel()

	select {
	case <-proc.stop:
		t.Fatal("finishing the first target stopped the provider-owned helper")
	case <-time.After(50 * time.Millisecond):
	}

	if got = translateOne(t, mt, "second", "en"); got != "mt:second" {
		t.Fatalf("second translation = %q", got)
	}
	if current := mt.procs[pairKey(fakeLang, "en")]; current != proc {
		t.Fatal("regional targets did not share the provider-owned helper")
	}
}

func TestMTCloseReapsHelpersAndRejectsNewWork(t *testing.T) {
	wantCleanup := errors.New("translation tree cleanup failed")
	mt := NewMT(&MTConfig{Bin: os.Args[0]})
	t.Cleanup(func() {
		if err := mt.Close(); err != nil && !errors.Is(err, wantCleanup) {
			t.Errorf("close test translator: %v", err)
		}
	})
	translateOne(t, mt, "warm", "en")

	mt.mu.Lock()
	proc := mt.procs[fakeLang+">en"]
	mt.mu.Unlock()
	if proc == nil {
		t.Fatal("translation helper was not cached")
	}
	proc.tree = &failingCloseTree{processTree: proc.tree, err: wantCleanup}

	closed := make(chan error, 2)
	go func() { closed <- mt.Close() }()
	go func() { closed <- mt.Close() }()
	for range 2 {
		if err := <-closed; !errors.Is(err, wantCleanup) {
			t.Fatalf("Close = %v, want cleanup failure", err)
		}
	}
	select {
	case <-proc.done:
	default:
		t.Fatal("Close returned before the translation helper was reaped")
	}

	_, err := mt.Translate(t.Context(), engine.Segment{Text: "late", Lang: fakeLang}, "en")
	if err == nil || !strings.Contains(err.Error(), "translator is closed") {
		t.Fatalf("Translate after Close = %v, want closed error", err)
	}
}

func TestMTDiscardReapsWithoutHoldingLock(t *testing.T) {
	warm, entered, release := blockedWarmProcess()
	defer release()
	proc := &pairProc{warmProcess: warm}
	mt := newTestMT(t, &MTConfig{})
	key := pairKey(fakeLang, "en")
	mt.procs[key] = proc

	discarded := make(chan error, 1)
	go func() { discarded <- mt.discard(fakeLang, "en", proc) }()
	<-entered

	// The helper is deleted from the map under the lock, but its shutdown runs
	// unlocked, so a concurrent Close and any other pair keep making progress.
	if !mt.mu.TryLock() {
		t.Fatal("discard held the provider lock across the process reap")
	}
	mt.mu.Unlock()

	closed := make(chan error, 1)
	go func() { closed <- mt.Close() }()
	if err := <-closed; err != nil {
		t.Fatalf("Close: %v", err)
	}

	release()
	if err := <-discarded; err != nil {
		t.Fatalf("discard: %v", err)
	}
}

func TestMTCloseInterruptsActiveRequest(t *testing.T) {
	mt := newTestMT(t, &MTConfig{Bin: os.Args[0]})
	proc, err := mt.pair(t.Context(), fakeLang, "en")
	if err != nil {
		t.Fatalf("start pair: %v", err)
	}

	result := make(chan error, 1)
	go func() {
		_, translateErr := mt.Translate(
			context.Background(), engine.Segment{Text: fakeHang, Lang: fakeLang}, "en",
		)
		result <- translateErr
	}()
	waitStderrMarker(t, proc.stderr, fakeHangReady)

	if err = mt.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if translateErr := <-result; translateErr == nil {
		t.Fatal("active translation succeeded after Close")
	}
	select {
	case <-proc.done:
	default:
		t.Fatal("Close returned before the active helper was reaped")
	}
}

func TestMTDistinctPairsGetDistinctProcesses(t *testing.T) {
	t.Parallel()

	mt := newTestMT(t, &MTConfig{Bin: os.Args[0]})

	translateOne(t, mt, "uno", core.Lang("en"))
	translateOne(t, mt, "due", core.Lang("de"))

	if got := pairCount(mt); got != 2 {
		t.Fatalf("warm processes = %d, want 2 (one per pair)", got)
	}
}

func TestMTHonorsContextOnReusedProcess(t *testing.T) {
	mt := newTestMT(t, &MTConfig{Bin: os.Args[0]})
	translateOne(t, mt, "warm", core.Lang("en"))

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	_, err := mt.Translate(ctx, engine.Segment{Text: fakeHang, Lang: fakeLang}, "en")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hanging translation error = %v, want deadline exceeded", err)
	}

	if got := translateOne(t, mt, "restarted", "en"); got != "mt:restarted" {
		t.Fatalf("translation after cancellation = %q, want restarted helper reply", got)
	}
}

func TestMTCanceledWaitDoesNotAbortActiveProcess(t *testing.T) {
	mt := newTestMT(t, &MTConfig{Bin: os.Args[0]})
	proc, err := mt.pair(t.Context(), fakeLang, "en")
	if err != nil {
		t.Fatalf("start pair: %v", err)
	}
	t.Cleanup(proc.abort)

	<-proc.gate
	held := true
	defer func() {
		if held {
			proc.gate <- struct{}{}
		}
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	_, err = mt.Translate(ctx, engine.Segment{Text: "queued", Lang: fakeLang}, "en")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued translation error = %v, want deadline exceeded", err)
	}
	if proc.unusable() {
		t.Fatal("canceling a queued translation stopped the active helper")
	}

	proc.gate <- struct{}{}
	held = false
	if got := translateOne(t, mt, "still-warm", "en"); got != "mt:still-warm" {
		t.Fatalf("translation after queued cancellation = %q", got)
	}
}

func TestMTReplacesNaturallyExitedProcess(t *testing.T) {
	mt := newTestMT(t, &MTConfig{Bin: os.Args[0]})
	proc, err := mt.pair(t.Context(), fakeLang, "en")
	if err != nil {
		t.Fatalf("start pair: %v", err)
	}

	if got := translateOne(t, mt, fakeExitAfter, "en"); got != "mt:"+fakeExitAfter {
		t.Fatalf("last translation = %q", got)
	}
	waitDone(t, proc.done)

	if got := translateOne(t, mt, "new-process", "en"); got != "mt:new-process" {
		t.Fatalf("replacement translation = %q", got)
	}

	mt.mu.Lock()
	replacement := mt.procs[fakeLang+">en"]
	mt.mu.Unlock()
	if replacement == proc {
		t.Fatal("exited helper remained cached")
	}
}

func TestMTReportsProtocolAndProcessDetails(t *testing.T) {
	t.Run("malformed JSON", func(t *testing.T) {
		mt := newTestMT(t, &MTConfig{Bin: os.Args[0]})
		_, err := mt.Translate(t.Context(), engine.Segment{Text: fakeBadJSON, Lang: fakeLang}, "en")
		if err == nil || !strings.Contains(err.Error(), "decode native mt response") {
			t.Fatalf("error = %v, want response decode context", err)
		}
	})

	t.Run("stderr tail", func(t *testing.T) {
		mt := newTestMT(t, &MTConfig{Bin: os.Args[0]})
		_, err := mt.Translate(t.Context(), engine.Segment{Text: fakeCrash, Lang: fakeLang}, "en")
		if err == nil || !strings.Contains(err.Error(), "fake mt crash") {
			t.Fatalf("error = %v, want helper stderr", err)
		}
	})

	t.Run("oversized response from live helper", func(t *testing.T) {
		mt := newTestMT(t, &MTConfig{Bin: os.Args[0]})
		started := time.Now()
		_, err := mt.Translate(t.Context(), engine.Segment{Text: fakeOversized, Lang: fakeLang}, "en")
		if err == nil || !strings.Contains(err.Error(), "token too long") {
			t.Fatalf("error = %v, want bounded scanner failure", err)
		}
		if elapsed := time.Since(started); elapsed > 2*time.Second {
			t.Fatalf("oversized response took %v; helper likely wedged in Wait", elapsed)
		}
	})
}

func TestMTWriteFailureWaitsForHelperDiagnostic(t *testing.T) {
	proc, err := startPairProc(
		t.Context(), os.Args[0], fakeRejectMT, "en", discardTestLogger(),
	)
	if err != nil {
		t.Fatalf("start pair: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := proc.close(); closeErr != nil {
			t.Errorf("close pair: %v", closeErr)
		}
	})
	waitStderrMarker(t, proc.stderr, fakeMTReject)

	started := time.Now()
	_, err = proc.translate(t.Context(), "ciao")
	if err == nil || !strings.Contains(err.Error(), fakeMTReject) {
		t.Fatalf("translate error = %v, want helper stderr", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("translate took %v; failed helper was not stopped promptly", elapsed)
	}
}

func TestDecodeMTResponseRequiresTextField(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		line    string
		want    string
		wantErr bool
	}{
		{name: "text", line: `{"text":"hello"}`, want: "hello"},
		{name: "empty text", line: `{"text":""}`},
		{name: "missing", line: `{}`, wantErr: true},
		{name: "null text", line: `{"text":null}`, wantErr: true},
		{name: "wrong type", line: `{"text":3}`, wantErr: true},
		{name: "null response", line: `null`, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := decodeMTResponse([]byte(test.line))
			if (err != nil) != test.wantErr {
				t.Fatalf("decodeMTResponse(%s) error = %v, wantErr %v", test.line, err, test.wantErr)
			}
			if err == nil && got.Text != test.want {
				t.Fatalf("decodeMTResponse(%s) text = %q, want %q", test.line, got.Text, test.want)
			}
		})
	}
}

func TestMTRejectsCanceledContextBeforeSpawn(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	mt := newTestMT(t, &MTConfig{Bin: os.Args[0]})
	_, err := mt.Translate(ctx, engine.Segment{Text: "x", Lang: fakeLang}, "en")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if got := pairCount(mt); got != 0 {
		t.Fatalf("warm processes = %d, want none", got)
	}
}

func pairCount(mt *MT) int {
	mt.mu.Lock()
	defer mt.mu.Unlock()

	return len(mt.procs)
}
