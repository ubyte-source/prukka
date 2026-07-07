package engine_test

import (
	"context"
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/engine"
)

// The port contracts must be implementable and usable exactly as the engine
// drives them: a Transcription pushed audio and drained finals, a Synthesizer
// fed clauses and drained audio. These fakes double as the reference usage.

type echoTranscriber struct{}

func (t echoTranscriber) Open(ctx context.Context, hint core.Lang) (engine.Transcription, error) {
	s := &echoTranscription{events: make(chan engine.Transcript, 4), hint: hint}
	go s.run(ctx)

	return s, nil
}

type echoTranscription struct {
	events chan engine.Transcript
	hint   core.Lang
	pushed int
	closed bool
}

func (s *echoTranscription) Push(p core.PCM) error {
	s.pushed += len(p.Data)

	return nil
}

func (s *echoTranscription) Events() <-chan engine.Transcript { return s.events }

func (s *echoTranscription) Err() error { return nil }

func (s *echoTranscription) CloseSend() error {
	s.closed = true

	return nil
}

func (s *echoTranscription) Close() error { return s.CloseSend() }

func (s *echoTranscription) run(ctx context.Context) {
	defer close(s.events)

	select {
	case s.events <- engine.Transcript{Text: "ciao", Lang: s.hint, Stable: true, Final: true}:
	case <-ctx.Done():
	}
}

type upperSynth struct{}

func (upperSynth) Close() error { return nil }

func (upperSynth) Speak(
	ctx context.Context, _ core.Lang, _ core.Voice, text <-chan string,
) (*engine.AudioStream, error) {
	out := make(chan core.PCM, 4)
	result := make(chan error, 1)

	go func() {
		for {
			select {
			case clause, ok := <-text:
				if !ok {
					result <- nil
					close(result)
					close(out)

					return
				}

				select {
				case out <- core.PCM{Data: make([]int16, len(clause)), Rate: 16000, Ch: 1}:
				case <-ctx.Done():
					result <- ctx.Err()
					close(result)
					close(out)

					return
				}
			case <-ctx.Done():
				result <- ctx.Err()
				close(result)
				close(out)

				return
			}
		}
	}()

	return engine.NewAudioStream(out, result), nil
}

var (
	_ engine.Transcriber = echoTranscriber{}
	_ engine.Synthesizer = upperSynth{}
)

func TestTranscriptionRoundTrip(t *testing.T) {
	t.Parallel()

	sess, err := echoTranscriber{}.Open(t.Context(), core.Lang("it"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if pushErr := sess.Push(core.PCM{Data: make([]int16, 320), Rate: 16000, Ch: 1}); pushErr != nil {
		t.Fatalf("push: %v", pushErr)
	}

	if closeErr := sess.CloseSend(); closeErr != nil {
		t.Fatalf("close send: %v", closeErr)
	}

	var got engine.Transcript
	for ev := range sess.Events() {
		got = ev
	}
	if closeErr := sess.Close(); closeErr != nil {
		t.Fatalf("close: %v", closeErr)
	}

	if !got.Final || got.Text != "ciao" || got.Lang != core.Lang("it") {
		t.Fatalf("event = %+v, want final it/ciao", got)
	}
}

func TestSynthesizerStreamsPerClause(t *testing.T) {
	t.Parallel()

	text := make(chan string, 2)
	text <- "due"
	text <- "quattro!"
	close(text)

	audio, err := upperSynth{}.Speak(t.Context(), core.Lang("en"), core.Voice{ID: "v"}, text)
	if err != nil {
		t.Fatalf("speak: %v", err)
	}

	var chunks int
	var samples int
	for chunk := range audio.Audio() {
		chunks++
		samples += len(chunk.Data)
	}
	if err := audio.Err(); err != nil {
		t.Fatalf("audio stream: %v", err)
	}

	if chunks != 2 || samples != len("due")+len("quattro!") {
		t.Fatalf("chunks=%d samples=%d, want 2 chunks and per-clause samples", chunks, samples)
	}
}
