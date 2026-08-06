package realtime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/realtime"
)

type echoTranscriber struct{}

func (t echoTranscriber) Open(ctx context.Context, hint core.Lang) (realtime.Transcription, error) {
	s := &echoTranscription{events: make(chan realtime.Transcript, 4), hint: hint}
	go s.run(ctx)

	return s, nil
}

type echoTranscription struct {
	events chan realtime.Transcript
	hint   core.Lang
	pushed int
	closed bool
}

func (s *echoTranscription) Push(_ context.Context, p core.PCM) error {
	s.pushed += len(p.Data)

	return nil
}

func (s *echoTranscription) Events() <-chan realtime.Transcript { return s.events }

func (s *echoTranscription) Err() error { return nil }

func (s *echoTranscription) CloseSend() error {
	s.closed = true

	return nil
}

func (s *echoTranscription) Close() error { return s.CloseSend() }

func (s *echoTranscription) run(ctx context.Context) {
	defer close(s.events)

	select {
	case s.events <- realtime.Transcript{Text: "ciao", Lang: s.hint, Stable: true, Final: true}:
	case <-ctx.Done():
	}
}

type upperSynth struct{}

func (upperSynth) Close() error { return nil }

func (upperSynth) Speak(
	ctx context.Context, _ core.Lang, _ core.Voice, text <-chan string,
) (*realtime.AudioStream, error) {
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

	return realtime.NewAudioStream(out, result), nil
}

var (
	_ realtime.Transcriber = echoTranscriber{}
	_ realtime.Synthesizer = upperSynth{}
)

func TestTranscriptionRoundTrip(t *testing.T) {
	t.Parallel()

	sess, err := echoTranscriber{}.Open(t.Context(), core.Lang("it"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if pushErr := sess.Push(t.Context(), core.PCM{Data: make([]int16, 320), Rate: 16000, Ch: 1}); pushErr != nil {
		t.Fatalf("push: %v", pushErr)
	}

	if closeErr := sess.CloseSend(); closeErr != nil {
		t.Fatalf("close send: %v", closeErr)
	}

	var got realtime.Transcript
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

func TestAudioStreamErrCachesOneTerminalResult(t *testing.T) {
	t.Parallel()

	want := errors.New("synthesis failed")
	result := make(chan error, 1)
	result <- want
	stream := realtime.NewAudioStream(make(chan core.PCM), result)

	const readers = 8
	seen := make(chan error, readers)
	start := make(chan struct{})
	for range readers {
		go func() {
			<-start
			seen <- stream.Err()
		}()
	}
	close(start)
	for range readers {
		if err := <-seen; !errors.Is(err, want) {
			t.Fatalf("Err = %v, want the one cached %v", err, want)
		}
	}
}

func TestAudioStreamErrRejectsAProviderWithNoTerminalResult(t *testing.T) {
	t.Parallel()

	stream := realtime.NewAudioStream(make(chan core.PCM), nil)
	first, second := stream.Err(), stream.Err()
	if first == nil || !errors.Is(second, first) {
		t.Fatalf("Err = %v then %v, want one cached missing-terminal-result failure", first, second)
	}
}
