package retry_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/providers/helpers/retry"
)

// scriptedMT fails with the scripted errors, then succeeds.
type scriptedMT struct {
	errs  []error
	calls int
}

// Translate implements core.MT.
func (s *scriptedMT) Translate(context.Context, core.Transcript, core.Lang, core.MTOpts) (string, error) {
	s.calls++
	if s.calls <= len(s.errs) {
		return "", s.errs[s.calls-1]
	}

	return "ok", nil
}

// scriptedSTT mirrors scriptedMT for the STT port.
type scriptedSTT struct {
	errs  []error
	calls int
}

// Transcribe implements core.STT.
func (s *scriptedSTT) Transcribe(context.Context, *core.Utterance, core.Lang) (core.Transcript, error) {
	s.calls++
	if s.calls <= len(s.errs) {
		return core.Transcript{}, s.errs[s.calls-1]
	}

	return core.Transcript{Text: "ok"}, nil
}

// policy returns a test policy recording sleeps instead of waiting.
func policy(slept *[]time.Duration) retry.Policy {
	p := retry.Default()
	p.Sleep = func(_ context.Context, d time.Duration) error {
		*slept = append(*slept, d)

		return nil
	}

	return p
}

// transient builds a wrapped transient failure.
func transient(i int) error {
	return fmt.Errorf("%w: attempt %d", core.ErrTransient, i)
}

func TestMTRetriesTransientFailures(t *testing.T) {
	t.Parallel()

	var slept []time.Duration

	next := &scriptedMT{errs: []error{transient(1), transient(2)}}

	out, err := retry.MT(next, policy(&slept)).Translate(t.Context(), core.Transcript{}, "en", core.MTOpts{})
	if err != nil {
		t.Fatalf("Translate returned error: %v", err)
	}

	if out != "ok" || next.calls != 3 || len(slept) != 2 {
		t.Fatalf("out=%q calls=%d sleeps=%v, want success on third try", out, next.calls, slept)
	}

	// Jitter keeps each delay within [base/2, cap).
	for i, d := range slept {
		if d < 100*time.Millisecond || d >= 2*time.Second {
			t.Fatalf("sleep %d = %v, outside jitter bounds", i, d)
		}
	}
}

func TestPermanentErrorsAreNotRetried(t *testing.T) {
	t.Parallel()

	var slept []time.Duration

	next := &scriptedMT{errs: []error{errors.New("model not found")}}

	wrapped := retry.MT(next, policy(&slept))
	if _, err := wrapped.Translate(t.Context(), core.Transcript{}, "en", core.MTOpts{}); err == nil {
		t.Fatal("Translate succeeded, want permanent error")
	}

	if next.calls != 1 || len(slept) != 0 {
		t.Fatalf("calls=%d sleeps=%v, want a single attempt", next.calls, slept)
	}
}

func TestAttemptsAreBounded(t *testing.T) {
	t.Parallel()

	var slept []time.Duration

	next := &scriptedSTT{errs: []error{transient(1), transient(2), transient(3), transient(4)}}
	u := &core.Utterance{}

	_, err := retry.STT(next, policy(&slept)).Transcribe(t.Context(), u, core.LangAuto)
	if !errors.Is(err, core.ErrTransient) {
		t.Fatalf("error = %v, want the final transient failure", err)
	}

	if next.calls != 3 {
		t.Fatalf("calls = %d, want exactly Attempts", next.calls)
	}
}

func TestCancellationStopsTheLoop(t *testing.T) {
	t.Parallel()

	p := retry.Default()
	p.Sleep = func(context.Context, time.Duration) error {
		return context.Canceled
	}

	next := &scriptedMT{errs: []error{transient(1), transient(2)}}

	_, err := retry.MT(next, p).Translate(t.Context(), core.Transcript{}, "en", core.MTOpts{})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, core.ErrTransient) {
		t.Fatalf("error = %v, want both the attempt error and the cancellation", err)
	}

	if next.calls != 1 {
		t.Fatalf("calls = %d, want no attempt after cancellation", next.calls)
	}
}

// scriptedTTS mirrors scriptedMT for the TTS port.
type scriptedTTS struct {
	errs  []error
	calls int
}

// Speak implements core.TTS.
func (s *scriptedTTS) Speak(context.Context, string, core.Lang, core.Voice) (core.PCM, error) {
	s.calls++
	if s.calls <= len(s.errs) {
		return core.PCM{}, s.errs[s.calls-1]
	}

	return core.PCM{Data: []int16{1}, Rate: 16000, Ch: 1}, nil
}

// TestTTSRetriesWithTheRealSleeper: one transient failure through real
// (tiny) delays covers the jittered timer path.
func TestTTSRetriesWithTheRealSleeper(t *testing.T) {
	t.Parallel()

	p := retry.Default()
	p.Base = time.Millisecond
	p.Max = 2 * time.Millisecond

	tts := &scriptedTTS{errs: []error{transient(1)}}

	out, err := retry.TTS(tts, p).Speak(t.Context(), "hi", "en", core.Voice{ID: "onyx"})
	if err != nil {
		t.Fatalf("Speak returned error: %v", err)
	}

	if len(out.Data) != 1 || tts.calls != 2 {
		t.Fatalf("Speak = %d samples after %d calls, want 1 sample on the 2nd call", len(out.Data), tts.calls)
	}
}

// TestSleepIsCancelable: a context canceled mid-backoff must end the wait
// with the context's error, not sleep on.
func TestSleepIsCancelable(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	p := retry.Default()
	p.Base = time.Hour // only a canceled context can end this wait quickly
	p.Max = time.Hour

	tts := &scriptedTTS{errs: []error{transient(1), transient(2)}}

	_, err := retry.TTS(tts, p).Speak(ctx, "hi", "en", core.Voice{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Speak error = %v, want context.Canceled from the backoff sleep", err)
	}
}
