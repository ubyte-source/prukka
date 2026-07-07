package breaker_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/providers/helpers/breaker"
)

// clock is a controllable test clock.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

// scriptedMT fails with the scripted errors in order, then succeeds. It
// records how many times the provider was actually called.
type scriptedMT struct {
	errs  []error
	calls int
}

func (s *scriptedMT) Translate(context.Context, core.Transcript, core.Lang, core.MTOpts) (string, error) {
	s.calls++
	if s.calls <= len(s.errs) {
		return "", s.errs[s.calls-1]
	}

	return "ok", nil
}

func transient(i int) error {
	return fmt.Errorf("%w: failure %d", core.ErrTransient, i)
}

// translate is a small helper that ignores the returned text.
func translate(mt core.MT) error {
	_, err := mt.Translate(context.Background(), core.Transcript{}, "en", core.MTOpts{})

	return err
}

func TestBreakerTripsAndFastFails(t *testing.T) {
	t.Parallel()

	c := &clock{t: time.Unix(0, 0)}
	next := &scriptedMT{errs: []error{transient(1), transient(2), transient(3)}}

	var states []bool

	mt := breaker.MT(next, breaker.Policy{Threshold: 2, Cooldown: 10 * time.Second, Now: c.now},
		func(open bool) { states = append(states, open) })

	// Two transient failures trip the breaker.
	if err := translate(mt); !errors.Is(err, core.ErrTransient) {
		t.Fatalf("first call err = %v, want transient", err)
	}

	if err := translate(mt); !errors.Is(err, core.ErrTransient) {
		t.Fatalf("second call err = %v, want transient", err)
	}

	// The breaker is now open: the next call fast-fails without reaching the
	// provider.
	before := next.calls
	if err := translate(mt); !errors.Is(err, breaker.ErrOpen) {
		t.Fatalf("open call err = %v, want ErrOpen", err)
	}

	if next.calls != before {
		t.Fatalf("provider was called %d times while open, want no new call", next.calls-before)
	}

	if len(states) != 1 || !states[0] {
		t.Fatalf("state transitions = %v, want one open", states)
	}
}

func TestBreakerProbesAndRecovers(t *testing.T) {
	t.Parallel()

	c := &clock{t: time.Unix(0, 0)}
	// Two failures then success.
	next := &scriptedMT{errs: []error{transient(1), transient(2)}}

	var states []bool

	mt := breaker.MT(next, breaker.Policy{Threshold: 2, Cooldown: 10 * time.Second, Now: c.now},
		func(open bool) { states = append(states, open) })

	if err := translate(mt); !errors.Is(err, core.ErrTransient) {
		t.Fatalf("first call err = %v, want transient", err)
	}

	if err := translate(mt); !errors.Is(err, core.ErrTransient) {
		t.Fatalf("second call err = %v, want transient (trips open)", err)
	}

	// Still within cooldown: fast-fail.
	c.t = c.t.Add(5 * time.Second)
	if err := translate(mt); !errors.Is(err, breaker.ErrOpen) {
		t.Fatalf("mid-cooldown err = %v, want ErrOpen", err)
	}

	// Past cooldown: one probe is allowed and succeeds → breaker closes.
	c.t = c.t.Add(6 * time.Second)
	if err := translate(mt); err != nil {
		t.Fatalf("probe err = %v, want success", err)
	}

	if len(states) != 2 || !states[0] || states[1] {
		t.Fatalf("state transitions = %v, want [open close]", states)
	}

	// Fully recovered: calls flow again.
	if err := translate(mt); err != nil {
		t.Fatalf("recovered call err = %v, want success", err)
	}
}

// scriptedSTT and scriptedTTS mirror scriptedMT for the other two ports.
type scriptedSTT struct {
	err   error
	calls int
}

func (s *scriptedSTT) Transcribe(context.Context, *core.Utterance, core.Lang) (core.Transcript, error) {
	s.calls++

	return core.Transcript{Text: "ok"}, s.err
}

type scriptedTTS struct {
	err   error
	calls int
}

func (s *scriptedTTS) Speak(context.Context, string, core.Lang, core.Voice) (core.PCM, error) {
	s.calls++

	return core.PCM{}, s.err
}

func TestSTTBreakerTripsAndFastFails(t *testing.T) {
	t.Parallel()

	c := &clock{t: time.Unix(0, 0)}
	next := &scriptedSTT{err: transient(1)}

	opened := false
	stt := breaker.STT(next, breaker.Policy{Threshold: 1, Cooldown: time.Second, Now: c.now},
		func(open bool) { opened = opened || open })

	u := &core.Utterance{}

	if _, err := stt.Transcribe(context.Background(), u, core.LangAuto); !errors.Is(err, core.ErrTransient) {
		t.Fatalf("first Transcribe err = %v, want transient", err)
	}

	// One failure with Threshold 1 trips it; the next call fast-fails.
	if _, err := stt.Transcribe(context.Background(), u, core.LangAuto); !errors.Is(err, breaker.ErrOpen) {
		t.Fatalf("open Transcribe err = %v, want ErrOpen", err)
	}

	if next.calls != 1 || !opened {
		t.Fatalf("calls=%d opened=%v, want one call and an open transition", next.calls, opened)
	}
}

func TestTTSBreakerTripsAndFastFails(t *testing.T) {
	t.Parallel()

	c := &clock{t: time.Unix(0, 0)}
	next := &scriptedTTS{err: transient(1)}

	tts := breaker.TTS(next, breaker.Policy{Threshold: 1, Cooldown: time.Second, Now: c.now}, nil)

	if _, err := tts.Speak(context.Background(), "x", "en", core.Voice{}); !errors.Is(err, core.ErrTransient) {
		t.Fatalf("first Speak err = %v, want transient", err)
	}

	if _, err := tts.Speak(context.Background(), "x", "en", core.Voice{}); !errors.Is(err, breaker.ErrOpen) {
		t.Fatalf("open Speak err = %v, want ErrOpen", err)
	}

	if next.calls != 1 {
		t.Fatalf("TTS calls = %d, want 1 (breaker fast-failed the second)", next.calls)
	}
}

func TestBreakerIgnoresPermanentErrors(t *testing.T) {
	t.Parallel()

	c := &clock{t: time.Unix(0, 0)}
	// Permanent (non-transient) errors must not trip the breaker.
	permanent := errors.New("bad request")
	next := &scriptedMT{errs: []error{permanent, permanent, permanent}}

	tripped := false
	mt := breaker.MT(next, breaker.Policy{Threshold: 2, Cooldown: time.Second, Now: c.now},
		func(bool) { tripped = true })

	for range 3 {
		if err := translate(mt); !errors.Is(err, permanent) {
			t.Fatalf("err = %v, want the permanent error passed through", err)
		}
	}

	if tripped {
		t.Fatal("permanent errors tripped the breaker")
	}

	if next.calls != 3 {
		t.Fatalf("provider calls = %d, want all 3 (breaker stayed closed)", next.calls)
	}
}
