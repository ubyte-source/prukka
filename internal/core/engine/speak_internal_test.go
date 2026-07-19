package engine

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

type stagedTakeSynth struct {
	terminal  error
	delivered chan<- struct{}
	release   <-chan struct{}
	chunks    [][]int16
}

type metricObservation struct {
	kind string
	d    time.Duration
}

type observedMetrics struct {
	calls chan metricObservation
}

func (m observedMetrics) E2ELatency(kind string, d time.Duration) {
	m.calls <- metricObservation{kind: kind, d: d}
}

func assertNoMetric(t *testing.T, calls <-chan metricObservation, failure string) {
	t.Helper()

	select {
	case got := <-calls:
		t.Fatalf("%s: %+v", failure, got)
	default:
	}
}

func requireVoiceMetric(t *testing.T, calls <-chan metricObservation) {
	t.Helper()

	select {
	case got := <-calls:
		if got.kind != "voice" || got.d <= 0 {
			t.Fatalf("committed voice metric = %+v, want positive voice latency", got)
		}
	default:
		t.Fatal("committed voice take emitted no voice latency metric")
	}
}

func (stagedTakeSynth) Close() error { return nil }

func (s stagedTakeSynth) Speak(
	ctx context.Context, _ core.Lang, _ core.Voice, _ <-chan string,
) (*AudioStream, error) {
	audio := make(chan core.PCM)
	result := make(chan error, 1)
	go func() {
		defer close(audio)
		defer close(result)

		for _, data := range s.chunks {
			select {
			case audio <- core.PCM{Data: data, Rate: pipeline.SampleRate, Ch: 1}:
			case <-ctx.Done():
				result <- ctx.Err()

				return
			}
		}
		close(s.delivered)
		select {
		case <-s.release:
			result <- s.terminal
		case <-ctx.Done():
			result <- ctx.Err()
		}
	}()

	return NewAudioStream(audio, result), nil
}

// A slow provider can finish after the live sink has advanced well beyond the
// source schedule. No chunk may escape early: terminal success publishes one
// contiguous take at the floor observed at completion.
func TestSpeakPlacesCompleteTakeAtLatestPlayoutFloor(t *testing.T) {
	t.Parallel()

	delivered := make(chan struct{})
	release := make(chan struct{})
	chunks := [][]int16{{11, 12}, {21, 22, 23}}
	track := pipeline.NewTrack()
	metrics := observedMetrics{calls: make(chan metricObservation, 1)}
	engine := testVoiceEngine(stagedTakeSynth{
		delivered: delivered,
		release:   release,
		chunks:    chunks,
	})
	engine.metrics = metrics

	done := make(chan error, 1)
	go func() { done <- engine.speak(t.Context(), "en", track, testVoiceJob()) }()
	<-delivered

	if _, published := track.Start(); published {
		t.Fatal("voice PCM reached the track before synthesis reported terminal success")
	}
	assertNoMetric(t, metrics.calls, "voice metric reported before transactional commit")

	const floor = 2 * time.Second
	bed := pipeline.NewTrack()
	bed.Append(0, make([]int16, int(floor.Seconds())*pipeline.SampleRate))
	mixer := pipeline.NewMixer(bed, track, math.Inf(-1))
	window := make([]int16, pipeline.SampleRate/10)
	for range int(floor / (100 * time.Millisecond)) {
		if _, status := mixer.NextInto(window); status != pipeline.PullReady {
			t.Fatal("test mixer did not advance the live playout floor")
		}
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("speak: %v", err)
	}
	requireVoiceMetric(t, metrics.calls)

	placedAt, ok := track.Start()
	if !ok || placedAt != floor {
		t.Fatalf("complete take placed at %v (ok=%v), want floor %v", placedAt, ok, floor)
	}
	want := []int16{11, 12, 21, 22, 23}
	got := make([]int16, len(want))
	track.Window(placedAt, got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("complete take = %v, want contiguous chunks %v", got, want)
	}
}

// A terminal provider failure invalidates the entire take. Publishing chunks
// before Err would leak truncated speech to a live device before lane recovery.
func TestSpeakDoesNotPublishPartialTakeOnTerminalFailure(t *testing.T) {
	t.Parallel()

	delivered := make(chan struct{})
	release := make(chan struct{})
	close(release)
	wantErr := errors.New("provider failed after PCM")
	track := pipeline.NewTrack()
	metrics := observedMetrics{calls: make(chan metricObservation, 1)}
	engine := testVoiceEngine(stagedTakeSynth{
		delivered: delivered,
		release:   release,
		chunks:    [][]int16{{31, 32}, {41}},
		terminal:  wantErr,
	})
	engine.metrics = metrics

	err := engine.speak(t.Context(), "en", track, testVoiceJob())
	if !errors.Is(err, wantErr) {
		t.Fatalf("speak error = %v, want terminal provider error", err)
	}
	if _, published := track.Start(); published {
		t.Fatal("failed synthesis published partial PCM")
	}
	assertNoMetric(t, metrics.calls, "failed synthesis emitted a committed voice metric")
}

func testVoiceEngine(synth Synthesizer) *Engine {
	return &Engine{
		log:     slog.New(slog.DiscardHandler),
		metrics: noopMetrics{},
		output: Output{Dub: &Dub{
			Synthesizer: synth,
			Voices:      map[core.Lang]core.Voice{"en": {ID: "voice", Lang: "en"}},
		}},
	}
}

func testVoiceJob() voiceJob {
	return voiceJob{
		endpointAt: time.Now(),
		seg: &core.TranslatedSegment{
			Session: "call", Target: "en", Text: "translated speech", ScheduleAt: 500 * time.Millisecond,
		},
	}
}
