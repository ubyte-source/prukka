package local_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/providers/local"
)

// TestSpeak: TTS posts to /audio/speech, forwards the voice id and decodes
// the raw pcm reply at the configured rate.
func TestSpeak(t *testing.T) {
	t.Parallel()

	var gotVoice string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/speech" {
			t.Fatalf("path = %q, want /audio/speech", r.URL.Path)
		}

		gotVoice = voiceField(t, r)
		writeRaw(t, w, s16le(10, 20, 30))
	}))
	defer server.Close()

	pcm, err := local.New(testConfig(server.URL)).
		Speak(context.Background(), "hi", "en", core.Voice{ID: "onyx"})
	if err != nil {
		t.Fatalf("Speak returned error: %v", err)
	}

	if gotVoice != "onyx" {
		t.Fatalf("voice id not forwarded: %q", gotVoice)
	}

	if len(pcm.Data) != 3 || pcm.Data[2] != 30 || pcm.Rate != 24000 {
		t.Fatalf("decoded audio = %+v, want 3 samples @ 24000", pcm)
	}
}

// TestSpeakDefaultsVoice: with no voice id the request carries the configured
// preset, so the server never has to guess.
func TestSpeakDefaultsVoice(t *testing.T) {
	t.Parallel()

	var gotVoice string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotVoice = voiceField(t, r)
		writeRaw(t, w, s16le(1))
	}))
	defer server.Close()

	if _, err := local.New(testConfig(server.URL)).
		Speak(context.Background(), "hi", "en", core.Voice{}); err != nil {
		t.Fatalf("Speak returned error: %v", err)
	}

	if gotVoice != "alloy" {
		t.Fatalf("default voice = %q, want alloy", gotVoice)
	}
}
