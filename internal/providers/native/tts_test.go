package native

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/engine"

	"github.com/ubyte-source/prukka/internal/testkit"
)

// speakOne synthesizes one clause and returns its PCM, failing on any error.
func speakOne(t *testing.T, tts *TTS, voice core.Voice, clause string) []int16 {
	t.Helper()

	got, err := speakTurn(t.Context(), tts, "en", voice, clause)
	if err != nil {
		t.Fatalf("speak: %v", err)
	}

	return got
}

func newTestTTS(t *testing.T, cfg *TTSConfig) *TTS {
	t.Helper()

	tts := NewTTS(cfg)
	t.Cleanup(func() {
		if err := tts.Close(); err != nil {
			t.Errorf("close test synthesizer: %v", err)
		}
	})

	return tts
}

func TestTTSSynthesizesClause(t *testing.T) {
	t.Parallel()

	tts := newTestTTS(t, &TTSConfig{Bin: os.Args[0], Rate: 16000})

	got := speakOne(t, tts, core.Voice{ID: fakeVoice}, "Buongiorno.")
	if !reflect.DeepEqual(got, fakeSamples) {
		t.Fatalf("pcm = %v, want %v", got, fakeSamples)
	}
}

func TestTTSSkipsBlankClauses(t *testing.T) {
	tts := newTestTTS(t, &TTSConfig{Bin: os.Args[0], Rate: 16000})
	voice := core.Voice{ID: fakeVoice}

	if got := speakOne(t, tts, voice, " \t\n"); len(got) != 0 {
		t.Fatalf("blank clause produced PCM %v", got)
	}
	if got := speakOne(t, tts, voice, "spoken"); !reflect.DeepEqual(got, fakeSamples) {
		t.Fatalf("PCM after blank clause = %v, want %v", got, fakeSamples)
	}
}

func TestTTSCloseUnblocksFullAudioQueue(t *testing.T) {
	tts := newTestTTS(t, &TTSConfig{Bin: os.Args[0], Rate: 16000})
	voice := core.Voice{ID: fakeVoice}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	text := make(chan string, ttsPCMQueue+1)
	for range cap(text) {
		text <- "queued"
	}
	close(text)

	audio, err := tts.Speak(ctx, "en", voice, text)
	if err != nil {
		t.Fatalf("Speak: %v", err)
	}
	proc := voiceForTest(tts, voice.ID)

	testkit.Eventually(t, 2*time.Second, func() bool {
		return len(audio.Audio()) == ttsPCMQueue && len(text) == 0 && len(proc.responses) == 1
	}, "synthesis did not block behind the full audio queue")

	if err = tts.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertCloseUnblocksAudio(t, audio)
}

func assertCloseUnblocksAudio(t *testing.T, audio *engine.AudioStream) {
	t.Helper()

	result := make(chan error, 1)
	go func() { result <- audio.Err() }()
	select {
	case streamErr := <-result:
		if streamErr == nil {
			t.Fatal("abandoned synthesis succeeded after provider Close")
		}
	case <-time.After(time.Second):
		t.Fatal("provider Close left synthesis blocked on its full audio queue")
	}
	for pcm := range audio.Audio() {
		_ = pcm
	}
}

func TestTTSCancelClosesOutputWhileWaitingForText(t *testing.T) {
	tts := newTestTTS(t, &TTSConfig{Bin: os.Args[0], Rate: 16000})
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	audio, err := tts.Speak(ctx, "en", core.Voice{ID: fakeVoice}, make(chan string))
	if err != nil {
		t.Fatalf("speak: %v", err)
	}
	for range audio.Audio() {
		t.Fatal("idle turn emitted audio")
	}
	if streamErr := audio.Err(); !errors.Is(streamErr, context.DeadlineExceeded) {
		t.Fatalf("stream error = %v, want deadline exceeded", streamErr)
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("context error = %v, want deadline exceeded", ctx.Err())
	}
}

func TestDecodeTTSResponseRequiresExactlyOneVariant(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		line    string
		wantErr bool
	}{
		{name: "audio", line: `{"audio":"AQI="}`},
		{name: "done", line: `{"done":true}`},
		{name: "empty object", line: `{}`, wantErr: true},
		{name: "empty audio", line: `{"audio":""}`, wantErr: true},
		{name: "false done", line: `{"done":false}`, wantErr: true},
		{name: "both", line: `{"audio":"AQI=","done":true}`, wantErr: true},
		{name: "null", line: `null`, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := decodeTTSResponse([]byte(test.line))
			if (err != nil) != test.wantErr {
				t.Fatalf("decodeTTSResponse(%s) error = %v, wantErr %v", test.line, err, test.wantErr)
			}
		})
	}
}

func TestTTSRejectsVoiceLanguageMismatch(t *testing.T) {
	tts := newTestTTS(t, &TTSConfig{Bin: os.Args[0], Rate: 16000})
	voice := core.Voice{ID: fakeVoice, Lang: "it"}

	_, err := tts.Speak(t.Context(), "en", voice, nil)
	if err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("error = %v, want voice language mismatch", err)
	}
	if got := procCount(tts); got != 0 {
		t.Fatalf("warm processes = %d, want none", got)
	}
}

func voiceForTest(tts *TTS, voice string) *voiceProc {
	tts.cache.mu.Lock()
	defer tts.cache.mu.Unlock()

	return tts.cache.procs[voice]
}

func procCount(tts *TTS) int {
	tts.cache.mu.Lock()
	defer tts.cache.mu.Unlock()

	return len(tts.cache.procs)
}

// TestTTSSpawnPathReportsConfiguredBinary: the spawn contract is observable,
// so composition tests can pin which executable a synthesizer will run.
func TestTTSSpawnPathReportsConfiguredBinary(t *testing.T) {
	t.Parallel()

	synth := NewTTS(&TTSConfig{Bin: "/managed/prukka", Rate: 16000})
	t.Cleanup(func() {
		if err := synth.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	})
	if got := synth.SpawnPath(); got != "/managed/prukka" {
		t.Fatalf("SpawnPath = %q", got)
	}
}
