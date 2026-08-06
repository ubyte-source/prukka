package lane

import (
	"context"
	"log/slog"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/realtime"
	"github.com/ubyte-source/prukka/internal/core/session"
)

// startupObserver emits a bounded lifecycle: source URLs, model paths,
// provider errors and voice IDs must never enter these records.
type startupObserver struct {
	started time.Time
	log     *slog.Logger
	now     func() time.Time
	profile session.Profile
	slug    string
}

func newStartupObserver(log *slog.Logger, s *session.Session) *startupObserver {
	return &startupObserver{
		log:     log,
		slug:    s.Slug,
		profile: s.Profile,
		started: time.Now(),
		now:     time.Now,
	}
}

func (o *startupObserver) begin(phase string, attrs ...slog.Attr) time.Time {
	if o == nil {
		return time.Time{}
	}

	now := o.now()
	o.emit(now, phase, attrs...)

	return now
}

func (o *startupObserver) complete(phase string, phaseStarted time.Time) {
	if o == nil {
		return
	}

	now := o.now()
	o.emit(now, phase, slog.Int64("phase_duration_ms", elapsedMilliseconds(now, phaseStarted)))
}

func (o *startupObserver) emit(now time.Time, phase string, attrs ...slog.Attr) {
	if o.log == nil {
		return
	}

	base := make([]slog.Attr, 0, 4+len(attrs))
	base = append(base,
		slog.String("session", o.slug),
		slog.String("profile", string(o.profile)),
		slog.String("phase", phase),
		slog.Int64("startup_duration_ms", elapsedMilliseconds(now, o.started)),
	)
	o.log.LogAttrs(context.Background(), slog.LevelInfo, "lane startup", append(base, attrs...)...)
}

func elapsedMilliseconds(now, started time.Time) int64 {
	if elapsed := now.Sub(started).Milliseconds(); elapsed > 0 {
		return elapsed
	}

	return 0
}

// startupObservedTranscriber brackets the native STT readiness handshake; an
// error is represented only by its phase, never by the provider's message.
type startupObservedTranscriber struct {
	realtime.Transcriber

	startup *startupObserver
}

func (t startupObservedTranscriber) Open(
	ctx context.Context, source core.Lang,
) (realtime.Transcription, error) {
	warmStarted := t.startup.begin("transcription_warming")
	transcription, err := t.Transcriber.Open(ctx, source)
	if err != nil {
		t.startup.complete("transcription_failed", warmStarted)

		return nil, err
	}
	t.startup.complete("transcription_ready", warmStarted)

	return transcription, nil
}
