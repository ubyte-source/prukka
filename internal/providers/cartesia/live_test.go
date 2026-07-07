package cartesia_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
	"github.com/ubyte-source/prukka/internal/providers/cartesia"
	"github.com/ubyte-source/prukka/internal/secret"
)

// liveKey gates the paid conformance test behind PRUKKA_LIVE_CARTESIA=1;
// the key resolves in-process from the keychain, never a shell.
func liveKey(t *testing.T) string {
	t.Helper()

	if os.Getenv("PRUKKA_LIVE_CARTESIA") == "" {
		t.Skip("live Cartesia conformance is opt-in: run hack/live-cartesia.sh")
	}

	key, err := secret.Resolve(config.Default().Providers.Cartesia.Key)
	if err != nil || key == "" {
		t.Skipf("no Cartesia key in the keychain (run `prukka key set cartesia`): %v", err)
	}

	return key
}

// spokenReference loads the real-speech reference the runner script
// prepared (raw s16le mono 16 kHz); tests exec nothing.
func spokenReference(t *testing.T) []int16 {
	t.Helper()

	path := os.Getenv("PRUKKA_LIVE_REFERENCE")
	if path == "" {
		t.Skip("no spoken reference: run hack/live-cartesia.sh")
	}

	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read reference: %v", err)
	}

	samples := make([]int16, len(data)/2)
	if _, decErr := pipeline.DecodeS16LE(samples, data); decErr != nil {
		t.Fatalf("decode reference: %v", decErr)
	}

	if len(samples) < 3*16000 {
		t.Fatalf("reference is %d samples, want ≥3 s of speech", len(samples))
	}

	return samples
}

// stockVoice is a public Cartesia preset ("Skylar") for validating
// synthesis on plans without cloning.
const stockVoice = "db6b0ed5-d5d3-463d-ae85-518a07d3c2b4"

// liveClient builds the production client on the shipped defaults, so the
// conformance run validates exactly what an installation would use.
func liveClient(key string) *cartesia.Client {
	defaults := config.Default().Providers.Cartesia

	return cartesia.New(&cartesia.Config{
		BaseURL: defaults.BaseURL,
		Key:     key,
		Model:   defaults.Model,
		Timeout: 60 * time.Second,
		Rate:    defaults.Rate,
	})
}

// assertLiveAudio checks that a take is real audio in the engine's format:
// the configured rate, mono, at least min samples and audibly non-silent.
func assertLiveAudio(t *testing.T, pcm core.PCM, minSamples int) {
	t.Helper()

	want := config.Default().Providers.Cartesia.Rate
	if pcm.Rate != want || pcm.Ch != 1 {
		t.Fatalf("output format = %d Hz ×%d, want %d mono", pcm.Rate, pcm.Ch, want)
	}

	if len(pcm.Data) < minSamples {
		t.Fatalf("output = %d samples, want ≥%d", len(pcm.Data), minSamples)
	}

	var peak int16
	for _, s := range pcm.Data {
		if s > peak {
			peak = s
		}
	}

	if peak < 500 {
		t.Fatalf("output peak = %d, want live audio, not silence", peak)
	}
}

// TestLiveSpeakPresetVoice validates auth, version and the /tts/bytes
// contract against the real API on any plan.
func TestLiveSpeakPresetVoice(t *testing.T) {
	client := liveClient(liveKey(t))

	pcm, err := client.Speak(t.Context(),
		"Hello from the live conformance test.", "en", core.Voice{ID: stockVoice})
	if err != nil {
		t.Fatalf("Speak (preset voice) returned error: %v", err)
	}

	// A short sentence at 24 kHz is at least a second of real audio.
	assertLiveAudio(t, pcm, config.Default().Providers.Cartesia.Rate)
}

// TestLiveCloneAndSpeak validates the full timbre path live; a plan gate
// skips, anything else is a real failure.
func TestLiveCloneAndSpeak(t *testing.T) {
	key := liveKey(t)
	ref := spokenReference(t)
	client := liveClient(key)

	voice := core.Voice{ID: "prukka-live-conformance", Ref: ref}

	pcm, err := client.Speak(t.Context(),
		"Hello from the live conformance test.", "en", voice)
	if err != nil {
		if strings.Contains(err.Error(), "plan_upgrade_required") {
			t.Skipf("cloning is not in this Cartesia plan: %v", err)
		}

		t.Fatalf("Speak (clone + synthesize) returned error: %v", err)
	}

	assertLiveAudio(t, pcm, config.Default().Providers.Cartesia.Rate)

	// A second take reuses the cached clone: still valid audio, no re-clone.
	again, err := client.Speak(t.Context(), "Second take in the same voice.", "en", voice)
	if err != nil {
		t.Fatalf("Speak (cached clone) returned error: %v", err)
	}

	assertLiveAudio(t, again, config.Default().Providers.Cartesia.Rate/2)
}
