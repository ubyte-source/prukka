package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

// voiceJob is a captioned take handed from the translate stage to the voice
// stage; endpointAt is when its source clause committed, for the voice metric.
type voiceJob struct {
	endpointAt time.Time
	seg        *core.TranslatedSegment
}

// speak synthesizes one take transactionally onto its target timeline. PCM is
// kept private until the provider reports terminal success, then one Append
// hands the complete take to the sink — a delayed broadcast track or a live
// call queue that bounds its own backlog. No partial speech is ever published.
func (e *Engine) speak(ctx context.Context, target core.Lang, track VoiceSink, job voiceJob) error {
	clauses := make(chan string, 1)
	clauses <- job.seg.Text
	close(clauses)

	synthCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	audio, err := e.output.Dub.Synthesizer.Speak(synthCtx, target, e.output.Dub.Voices[target], clauses)
	if err != nil {
		return fmt.Errorf("synthesize %s: %w", target, err)
	}

	take, peak, err := e.collectVoiceTake(audio)
	if err != nil {
		return fmt.Errorf("synthesize %s: %w", target, err)
	}
	placedAt := track.Append(job.seg.ScheduleAt, take)
	latency := time.Since(job.endpointAt)
	e.metrics.E2ELatency("voice", latency)

	e.log.Info("voice take synthesized",
		"session", job.seg.Session, "target", target,
		"ms", latency.Milliseconds(),
		"samples", len(take), "peak_s16", peak,
		"scheduled_ms", job.seg.ScheduleAt.Milliseconds(),
		"placed_ms", placedAt.Milliseconds())

	return nil
}

// collectVoiceTake owns provider PCM until terminal success. core.PCM buffers
// may be pooled, so append copies each chunk before the provider can reuse it.
func (e *Engine) collectVoiceTake(audio *AudioStream) (take []int16, peak int, err error) {
	if audio == nil || audio.Audio() == nil {
		return nil, 0, errors.New("synthesis returned no audio stream")
	}

	// Seed four seconds of reference audio: typical clause takes fit without
	// a single regrowth, where zero capacity pays the full append-doubling
	// ladder (~15 reallocations, ~0.5 MB copied) on every take.
	take = make([]int16, 0, 4*core.SampleRate)
	for chunk := range audio.Audio() {
		if len(chunk.Data) == 0 {
			continue
		}
		if len(chunk.Data) > maxVoiceTakeSamples-len(take) {
			return nil, 0, fmt.Errorf("voice take exceeds %d samples", maxVoiceTakeSamples)
		}

		take = append(take, chunk.Data...)
		peak = max(peak, pipeline.PeakS16(chunk.Data))
	}
	if streamErr := audio.Err(); streamErr != nil {
		return nil, 0, streamErr
	}
	if len(take) == 0 {
		return nil, 0, errors.New("synthesis returned no PCM")
	}

	return take, peak, nil
}
