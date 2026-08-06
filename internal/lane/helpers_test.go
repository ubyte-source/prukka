package lane

// Fixtures shared by more than one test file.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/realtime"
	"github.com/ubyte-source/prukka/internal/core/session"
)

type scriptedFrames struct {
	closeErr error
	results  []frameResult
	closed   int
}

type frameResult struct {
	err   error
	frame core.PCM
}

func (f *scriptedFrames) Next(context.Context) (core.PCM, error) {
	result := f.results[0]
	f.results = f.results[1:]

	return result.frame, result.err
}

func (f *scriptedFrames) Close() error {
	f.closed++

	return f.closeErr
}

// recordingIngress hands out one prepared source and announces the open.
type recordingIngress struct {
	frames core.Frames
	opened chan struct{}
}

func (i recordingIngress) Open(context.Context, core.SourceSpec) (core.Frames, error) {
	close(i.opened)

	return i.frames, nil
}

type emptyTranscriber struct{}

func (emptyTranscriber) Open(context.Context, core.Lang) (realtime.Transcription, error) {
	return newEmptyTranscription(), nil
}

type emptyTranscription struct {
	events chan realtime.Transcript
	once   sync.Once
}

func newEmptyTranscription() *emptyTranscription {
	return &emptyTranscription{events: make(chan realtime.Transcript)}
}

func (*emptyTranscription) Push(context.Context, core.PCM) error { return nil }

func (t *emptyTranscription) Events() <-chan realtime.Transcript { return t.events }

func (*emptyTranscription) Err() error { return nil }

func (t *emptyTranscription) CloseSend() error {
	t.close()

	return nil
}

func (t *emptyTranscription) Close() error {
	t.close()

	return nil
}

func (t *emptyTranscription) close() { t.once.Do(func() { close(t.events) }) }

type steppingClock struct {
	now  time.Time
	step time.Duration
	mu   sync.Mutex
}

func (c *steppingClock) tick() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(c.step)

	return c.now
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

func startupObserverForTest(
	output io.Writer, s *session.Session, step time.Duration,
) *startupObserver {
	clock := &steppingClock{now: time.Unix(1_700_000_000, 0), step: step}

	return &startupObserver{
		log:     slog.New(slog.NewJSONHandler(output, nil)),
		slug:    s.Slug,
		profile: s.Profile,
		started: clock.now,
		now:     clock.tick,
	}
}

func decodeStartupLogs(t *testing.T, raw []byte) []map[string]any {
	t.Helper()

	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	entries := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("decode startup log %q: %v", line, err)
		}
		entries = append(entries, entry)
	}

	return entries
}

func assertStartupPhases(t *testing.T, entries []map[string]any, want ...string) {
	t.Helper()

	if len(entries) != len(want) {
		t.Fatalf("startup logs = %v, want %d phases", entries, len(want))
	}
	for i, phase := range want {
		if got := entries[i]["phase"]; got != phase {
			t.Fatalf("startup phase[%d] = %v, want %q", i, got, phase)
		}
	}
}

func assertLogOmits(t *testing.T, log string, secrets ...string) {
	t.Helper()

	for _, secret := range secrets {
		if strings.Contains(log, secret) {
			t.Fatalf("startup log exposes %q: %s", secret, log)
		}
	}
}
