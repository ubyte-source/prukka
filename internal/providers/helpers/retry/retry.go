// Package retry decorates the AI ports with bounded, jittered retries on
// transient failures.
package retry

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
)

// Policy bounds the retry loop.
type Policy struct {
	// Sleep waits between attempts; nil selects a context-aware default.
	// It is injectable for tests.
	Sleep func(ctx context.Context, d time.Duration) error
	// Attempts is the total number of tries, including the first.
	Attempts int
	// Base is the first backoff delay; it doubles per attempt.
	Base time.Duration
	// Max caps the backoff delay.
	Max time.Duration
}

// Default returns the policy: three attempts, exponential backoff
// with jitter from 200 ms, capped at 2 s.
func Default() Policy {
	return Policy{Attempts: 3, Base: 200 * time.Millisecond, Max: 2 * time.Second}
}

// STT wraps next with the policy.
func STT(next core.STT, p Policy) core.STT {
	return &sttRetry{next: next, p: p}
}

// MT wraps next with the policy.
func MT(next core.MT, p Policy) core.MT {
	return &mtRetry{next: next, p: p}
}

// TTS wraps next with the policy.
func TTS(next core.TTS, p Policy) core.TTS {
	return &ttsRetry{next: next, p: p}
}

type ttsRetry struct {
	next core.TTS
	p    Policy
}

// Speak implements core.TTS.
func (t *ttsRetry) Speak(ctx context.Context, text string, to core.Lang, v core.Voice) (core.PCM, error) {
	var out core.PCM

	err := t.p.run(ctx, func() error {
		var attemptErr error
		out, attemptErr = t.next.Speak(ctx, text, to, v)

		return attemptErr
	})

	return out, err
}

type sttRetry struct {
	next core.STT
	p    Policy
}

// Transcribe implements core.STT.
func (s *sttRetry) Transcribe(ctx context.Context, u *core.Utterance, hint core.Lang) (core.Transcript, error) {
	var out core.Transcript

	err := s.p.run(ctx, func() error {
		var attemptErr error
		out, attemptErr = s.next.Transcribe(ctx, u, hint)

		return attemptErr
	})

	return out, err
}

type mtRetry struct {
	next core.MT
	p    Policy
}

// Translate implements core.MT.
func (m *mtRetry) Translate(ctx context.Context, t core.Transcript, to core.Lang, o core.MTOpts) (string, error) {
	var out string

	err := m.p.run(ctx, func() error {
		var attemptErr error
		out, attemptErr = m.next.Translate(ctx, t, to, o)

		return attemptErr
	})

	return out, err
}

// run executes op, retrying transient failures with jittered backoff.
func (p Policy) run(ctx context.Context, op func() error) error {
	sleep := p.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}

	delay := p.Base

	for attempt := 1; ; attempt++ {
		err := op()
		if err == nil || !errors.Is(err, core.ErrTransient) || attempt >= p.Attempts {
			return err
		}

		if sleepErr := sleep(ctx, jitter(delay)); sleepErr != nil {
			return errors.Join(err, sleepErr)
		}

		delay = min(delay*2, p.Max)
	}
}

// jitter picks a uniform delay in [d/2, d); crypto/rand costs nothing on
// this cold path.
func jitter(d time.Duration) time.Duration {
	half := int64(d / 2)
	if half <= 0 {
		return d
	}

	n, err := rand.Int(rand.Reader, big.NewInt(half))
	if err != nil {
		return d
	}

	return d/2 + time.Duration(n.Int64())
}

// sleepCtx waits without leaking the timer.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
