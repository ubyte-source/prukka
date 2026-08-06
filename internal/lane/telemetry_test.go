package lane

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/realtime"
	"github.com/ubyte-source/prukka/internal/core/session"
)

type failingTranscriber struct{ err error }

func (f failingTranscriber) Open(context.Context, core.Lang) (realtime.Transcription, error) {
	return nil, f.err
}

// TestStartupObservedTranscriberBracketsTheReadinessHandshake: one warming
// phase, one terminal phase, and the provider's text never in the record.
func TestStartupObservedTranscriberBracketsTheReadinessHandshake(t *testing.T) {
	t.Parallel()

	s := &session.Session{Slug: "handshake", Profile: session.ProfileCall}

	var readyLogs bytes.Buffer
	ready := startupObservedTranscriber{
		Transcriber: emptyTranscriber{},
		startup:     startupObserverForTest(&readyLogs, s, 40*time.Millisecond),
	}
	if _, err := ready.Open(t.Context(), "it"); err != nil {
		t.Fatalf("Open: %v", err)
	}
	entries := decodeStartupLogs(t, readyLogs.Bytes())
	assertStartupPhases(t, entries, "transcription_warming", "transcription_ready")
	if got := entries[1]["phase_duration_ms"]; got != float64(40) {
		t.Fatalf("handshake duration = %v, want 40 ms", got)
	}

	var failedLogs bytes.Buffer
	openErr := errors.New("load /Users/alice/models/ggml.bin: token=engine-secret")
	failing := startupObservedTranscriber{
		Transcriber: failingTranscriber{err: openErr},
		startup:     startupObserverForTest(&failedLogs, s, 40*time.Millisecond),
	}
	if _, err := failing.Open(t.Context(), "it"); !errors.Is(err, openErr) {
		t.Fatalf("Open error = %v, want the provider failure", err)
	}
	entries = decodeStartupLogs(t, failedLogs.Bytes())
	assertStartupPhases(t, entries, "transcription_warming", "transcription_failed")
	assertLogOmits(t, failedLogs.String(), "/Users/alice", "engine-secret")
}

// TestElapsedMillisecondsNeverReportsANegativeDuration: the wall clock these
// phases are stamped from can step backwards.
func TestElapsedMillisecondsNeverReportsANegativeDuration(t *testing.T) {
	t.Parallel()

	started := time.Unix(1_700_000_000, 0)
	if got := elapsedMilliseconds(started.Add(-time.Minute), started); got != 0 {
		t.Fatalf("backward clock = %d ms, want 0", got)
	}
	if got := elapsedMilliseconds(started.Add(250*time.Millisecond), started); got != 250 {
		t.Fatalf("forward clock = %d ms, want 250", got)
	}
}
