package pipeline_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
	"github.com/ubyte-source/prukka/internal/dispatch"
)

// fakeFrames replays prebuilt chunks and then reports EOF.
type fakeFrames struct {
	chunks []core.PCM
	next   int
}

// Next implements core.Frames.
func (f *fakeFrames) Next(ctx context.Context) (core.PCM, error) {
	if ctx.Err() != nil {
		return core.PCM{}, ctx.Err()
	}

	if f.next >= len(f.chunks) {
		return core.PCM{}, io.EOF
	}

	chunk := f.chunks[f.next]
	f.next++

	return chunk, nil
}

// speechThenSilence builds alternating one-second speech/silence chunks so
// the VAD endpoints one utterance per speech block.
func speechThenSilence(blocks int) *fakeFrames {
	chunks := make([]core.PCM, 0, blocks*2)

	for i := range blocks {
		on := frame(3000, 0)
		off := frame(0, 0)

		speech := core.PCM{Rate: pipeline.SampleRate, Ch: 1, PTS: time.Duration(2*i) * time.Second}
		silence := core.PCM{Rate: pipeline.SampleRate, Ch: 1, PTS: time.Duration(2*i+1) * time.Second}

		for range 50 { // 50 × 20 ms = 1 s
			speech.Data = append(speech.Data, on.Data...)
			silence.Data = append(silence.Data, off.Data...)
		}

		chunks = append(chunks, speech, silence)
	}

	return &fakeFrames{chunks: chunks}
}

// laneSTT transcribes utterances as numbered phrases; failAt injects one
// transient failure.
type laneSTT struct {
	calls  int
	failAt int
}

// Transcribe implements core.STT.
func (s *laneSTT) Transcribe(_ context.Context, u *core.Utterance, _ core.Lang) (core.Transcript, error) {
	s.calls++
	if s.calls == s.failAt {
		return core.Transcript{}, fmt.Errorf("%w: injected", core.ErrTransient)
	}

	dur := time.Duration(len(u.Audio.Data)) * time.Second / pipeline.SampleRate

	return core.Transcript{
		Text: fmt.Sprintf("frase %d", s.calls),
		Lang: "it",
		Span: [2]time.Duration{u.Audio.PTS, u.Audio.PTS + dur},
	}, nil
}

// laneMT translates by tagging the target and records context windows.
type laneMT struct {
	contexts [][]string
}

// Translate implements core.MT.
func (m *laneMT) Translate(_ context.Context, t core.Transcript, to core.Lang, o core.MTOpts) (string, error) {
	m.contexts = append(m.contexts, o.Context)

	return "[" + string(to) + "] " + t.Text, nil
}

// captureSink collects delivered segments.
type captureSink struct {
	segs []core.TranslatedSegment
}

// Append implements pipeline.Sink.
func (c *captureSink) Append(seg *core.TranslatedSegment) {
	c.segs = append(c.segs, *seg)
}

// sineSpeech builds 1 s of tone at f0 plus 1 s of silence: one utterance
// with a known pitch.
func sineSpeech(f0 float64) *fakeFrames {
	speech := core.PCM{Rate: pipeline.SampleRate, Ch: 1}
	for i := range pipeline.SampleRate {
		speech.Data = append(speech.Data,
			int16(6000*math.Sin(2*math.Pi*f0*float64(i)/float64(pipeline.SampleRate))))
	}

	silence := core.PCM{
		Data: make([]int16, pipeline.SampleRate),
		Rate: pipeline.SampleRate, Ch: 1, PTS: time.Second,
	}

	return &fakeFrames{chunks: []core.PCM{speech, silence}}
}

// sineTTS returns a one-second 200 Hz take at 24 kHz — a synthesized voice
// with a known fundamental for register-matching assertions.
type sineTTS struct{}

// Speak implements core.TTS.
func (sineTTS) Speak(context.Context, string, core.Lang, core.Voice) (core.PCM, error) {
	take := core.PCM{Rate: 24000, Ch: 1}
	for i := range 24000 {
		take.Data = append(take.Data, int16(10000*math.Sin(2*math.Pi*200*float64(i)/24000)))
	}

	return take, nil
}

// TestLaneMatchesRegisterWhenAdapting: a 180 Hz speaker over a 200 Hz take
// must reach the shaper with ≈0.9 pitch.
func TestLaneMatchesRegisterWhenAdapting(t *testing.T) {
	t.Parallel()

	shaper := &halfShaper{}

	lane := pipeline.NewCaptions(&pipeline.CaptionConfig{
		Stream:    pipeline.Stream{Session: "demo", Track: "main", Source: "it", Delay: time.Second},
		Providers: pipeline.Providers{STT: &laneSTT{}, MT: &laneMT{}, VAD: pipeline.NewEnergyVAD(pipeline.BroadcastVAD())},
		Output: pipeline.Output{
			Sinks: map[core.Lang]pipeline.Sink{"en": &captureSink{}},
			Dub: &pipeline.Dub{
				TTS:        sineTTS{},
				Shaper:     shaper,
				Tracks:     map[core.Lang]*pipeline.Track{"en": pipeline.NewTrack()},
				AutoVoices: bank(),
				AdaptPitch: true,
			},
		},
	}, slog.New(slog.DiscardHandler))

	if err := lane.Run(t.Context(), sineSpeech(180)); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if len(shaper.pitches) != 1 {
		t.Fatalf("Shape calls = %d, want 1", len(shaper.pitches))
	}

	if got := shaper.pitches[0]; got < 0.85 || got > 0.95 {
		t.Fatalf("pitch factor = %.3f, want ≈0.9 (180 Hz speaker over a 200 Hz take)", got)
	}
}

// runLane executes a lane over the given source with it→{it,en} captions.
func runLane(t *testing.T, frames core.Frames, stt core.STT, mt core.MT) (it, en *captureSink) {
	t.Helper()

	it = &captureSink{}
	en = &captureSink{}

	lane := pipeline.NewCaptions(&pipeline.CaptionConfig{
		Stream:    pipeline.Stream{Session: "demo", Track: "main", Source: "it", Delay: 8 * time.Second},
		Providers: pipeline.Providers{STT: stt, MT: mt, VAD: pipeline.NewEnergyVAD(pipeline.BroadcastVAD())},
		Output:    pipeline.Output{Sinks: map[core.Lang]pipeline.Sink{"it": it, "en": en}},
	}, slog.New(slog.DiscardHandler))

	if err := lane.Run(t.Context(), frames); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	return it, en
}

func TestCaptionsEndToEnd(t *testing.T) {
	t.Parallel()

	mt := &laneMT{}
	it, en := runLane(t, speechThenSilence(2), &laneSTT{}, mt)

	if len(it.segs) != 2 || len(en.segs) != 2 {
		t.Fatalf("segments it=%d en=%d, want 2 each", len(it.segs), len(en.segs))
	}

	// Same-language captions bypass MT: the transcript text goes straight
	// to the sink and only the English lane pays for translation.
	if it.segs[0].Text != "frase 1" {
		t.Fatalf("it text = %q, want raw transcript", it.segs[0].Text)
	}

	if en.segs[0].Text != "[en] frase 1" || en.segs[1].Text != "[en] frase 2" {
		t.Fatalf("en texts = %q, %q", en.segs[0].Text, en.segs[1].Text)
	}

	if len(mt.contexts) != 2 {
		t.Fatalf("MT calls = %d, want 2 (it lane must not call MT)", len(mt.contexts))
	}

	assertSegmentIdentity(t, &en.segs[0])
}

// assertSegmentIdentity checks the metadata of the first English segment.
func assertSegmentIdentity(t *testing.T, first *core.TranslatedSegment) {
	t.Helper()

	if first.Target != "en" || first.Session != "demo" || first.Track != "main" {
		t.Fatalf("segment identity = %+v", first)
	}

	if first.ScheduleAt != 8*time.Second {
		t.Fatalf("ScheduleAt = %v, want source PTS 0 + delay 8s", first.ScheduleAt)
	}

	if first.Duration < time.Second || first.Duration > 2*time.Second {
		t.Fatalf("Duration = %v, want the utterance span", first.Duration)
	}
}

func TestCaptionsCarryContextWindow(t *testing.T) {
	t.Parallel()

	mt := &laneMT{}
	runLane(t, speechThenSilence(3), &laneSTT{}, mt)

	if len(mt.contexts) != 3 {
		t.Fatalf("MT calls = %d, want 3", len(mt.contexts))
	}

	if len(mt.contexts[0]) != 0 {
		t.Fatalf("first context = %v, want empty", mt.contexts[0])
	}

	if len(mt.contexts[2]) != 2 || mt.contexts[2][0] != "frase 1" || mt.contexts[2][1] != "frase 2" {
		t.Fatalf("third context = %v, want the previous two lines", mt.contexts[2])
	}
}

func TestCaptionsSurviveProviderFailure(t *testing.T) {
	t.Parallel()

	it, en := runLane(t, speechThenSilence(2), &laneSTT{failAt: 1}, &laneMT{})

	if len(it.segs) != 1 || len(en.segs) != 1 {
		t.Fatalf("segments it=%d en=%d, want the lane to survive the failed utterance", len(it.segs), len(en.segs))
	}

	if en.segs[0].Text != "[en] frase 2" {
		t.Fatalf("surviving caption = %q, want the second utterance", en.segs[0].Text)
	}
}

func TestCaptionsThroughSharedDispatcher(t *testing.T) {
	t.Parallel()

	// Route both targets through a shared bounded pool: captions must still
	// be delivered, and per-target order must survive the async workers.
	pool := dispatch.New(4, 32)
	defer pool.Close()

	it := &captureSink{}
	en := &captureSink{}

	lane := pipeline.NewCaptions(&pipeline.CaptionConfig{
		Stream:    pipeline.Stream{Session: "demo", Track: "main", Source: "it", Delay: 8 * time.Second},
		Providers: pipeline.Providers{STT: &laneSTT{}, MT: &laneMT{}, VAD: pipeline.NewEnergyVAD(pipeline.BroadcastVAD())},
		Output:    pipeline.Output{Sinks: map[core.Lang]pipeline.Sink{"it": it, "en": en}},
		Policy:    pipeline.Policy{Dispatch: pool},
	}, slog.New(slog.DiscardHandler))

	if err := lane.Run(t.Context(), speechThenSilence(3)); err != nil {
		t.Fatalf("Run through dispatcher: %v", err)
	}

	if len(en.segs) != 3 {
		t.Fatalf("en segments = %d, want 3 via the shared pool", len(en.segs))
	}

	for i, seg := range en.segs {
		if want := fmt.Sprintf("[en] frase %d", i+1); seg.Text != want {
			t.Fatalf("en.segs[%d] = %q, want %q (order must survive the pool)", i, seg.Text, want)
		}
	}
}

// blockingFrames blocks until its context dies.
type blockingFrames struct{}

// Next implements core.Frames.
func (blockingFrames) Next(ctx context.Context) (core.PCM, error) {
	<-ctx.Done()

	return core.PCM{}, ctx.Err()
}

func TestCaptionsStopOnCancellation(t *testing.T) {
	t.Parallel()

	lane := pipeline.NewCaptions(&pipeline.CaptionConfig{
		Stream:    pipeline.Stream{Session: "demo", Source: "it"},
		Providers: pipeline.Providers{STT: &laneSTT{}, MT: &laneMT{}, VAD: pipeline.NewEnergyVAD(pipeline.CallVAD())},
		Output:    pipeline.Output{Sinks: map[core.Lang]pipeline.Sink{"en": &captureSink{}}},
	}, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() { done <- lane.Run(ctx, blockingFrames{}) }()

	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run returned nil, want the cancellation error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}

// stubBudget gates stages by fixed booleans.
type stubBudget struct{ stt, mt, tts bool }

func (b stubBudget) AllowSTT(string) bool { return b.stt }
func (b stubBudget) AllowMT(string) bool  { return b.mt }
func (b stubBudget) AllowTTS(string) bool { return b.tts }

// runBudgetLane runs an it→{it,en} lane with dubbing and the given budget.
func runBudgetLane(t *testing.T, budget pipeline.Budget) (it, en *captureSink, tts *fixedTTS) {
	t.Helper()

	it = &captureSink{}
	en = &captureSink{}
	tts = &fixedTTS{}
	track := pipeline.NewTrack()

	lane := pipeline.NewCaptions(&pipeline.CaptionConfig{
		Stream:    pipeline.Stream{Session: "demo", Track: "main", Source: "it", Delay: 8 * time.Second},
		Providers: pipeline.Providers{STT: &laneSTT{}, MT: &laneMT{}, VAD: pipeline.NewEnergyVAD(pipeline.BroadcastVAD())},
		Output: pipeline.Output{
			Sinks: map[core.Lang]pipeline.Sink{"it": it, "en": en},
			Dub: &pipeline.Dub{
				TTS:    tts,
				Shaper: &halfShaper{},
				Tracks: map[core.Lang]*pipeline.Track{"en": track},
			},
		},
		Policy: pipeline.Policy{Budget: budget},
	}, slog.New(slog.DiscardHandler))

	if err := lane.Run(t.Context(), speechThenSilence(1)); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	return it, en, tts
}

func TestBudgetPausesDubbingFirst(t *testing.T) {
	t.Parallel()

	// TTS off, everything else on: captions flow, no dub take.
	it, en, tts := runBudgetLane(t, stubBudget{stt: true, mt: true, tts: false})

	if len(it.segs) != 1 || len(en.segs) != 1 {
		t.Fatalf("captions it=%d en=%d, want 1 each even with dubbing paused", len(it.segs), len(en.segs))
	}

	if tts.calls != 0 {
		t.Fatalf("TTS called %d times, want 0 while over budget", tts.calls)
	}
}

func TestBudgetPausesTranslationNext(t *testing.T) {
	t.Parallel()

	// MT off: the source-language caption still flows (free), the
	// translated one does not, and dubbing never runs without text.
	it, en, tts := runBudgetLane(t, stubBudget{stt: true, mt: false, tts: true})

	if len(it.segs) != 1 {
		t.Fatalf("it captions = %d, want 1 (same-language is free)", len(it.segs))
	}

	if len(en.segs) != 0 {
		t.Fatalf("en captions = %d, want 0 (translation paused)", len(en.segs))
	}

	if tts.calls != 0 {
		t.Fatalf("TTS called %d times, want 0 (no translated text to voice)", tts.calls)
	}
}

func TestBudgetHardStopDropsEverything(t *testing.T) {
	t.Parallel()

	// STT off: nothing is transcribed, so no captions at all.
	it, en, tts := runBudgetLane(t, stubBudget{stt: false, mt: false, tts: false})

	if len(it.segs) != 0 || len(en.segs) != 0 || tts.calls != 0 {
		t.Fatalf("hard stop produced output: it=%d en=%d tts=%d", len(it.segs), len(en.segs), tts.calls)
	}
}
