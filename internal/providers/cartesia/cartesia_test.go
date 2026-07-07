package cartesia_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/providers/cartesia"
)

// s16le renders samples as little-endian s16, Cartesia's raw pcm output form.
func s16le(samples ...int16) []byte {
	raw := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(raw[i*2:], uint16(int32(s)&0xFFFF))
	}

	return raw
}

// writeRaw writes raw bytes, failing the test on a write error.
func writeRaw(t *testing.T, w http.ResponseWriter, b []byte) {
	t.Helper()

	if _, err := w.Write(b); err != nil {
		t.Fatalf("write reply: %v", err)
	}
}

// cloneID replies to /voices/clone with the given created voice id.
func cloneID(t *testing.T, w http.ResponseWriter, id string) {
	t.Helper()

	if err := json.NewEncoder(w).Encode(map[string]any{"id": id}); err != nil {
		t.Fatalf("encode clone reply: %v", err)
	}
}

// voiceOf reads the voice id out of a /tts/bytes request body.
func voiceOf(t *testing.T, r *http.Request) string {
	t.Helper()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read tts body: %v", err)
	}

	var req struct {
		Voice struct {
			Mode string `json:"mode"`
			ID   string `json:"id"`
		} `json:"voice"`
	}

	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode tts body: %v", err)
	}

	return req.Voice.ID
}

func testConfig(url string) *cartesia.Config {
	return &cartesia.Config{BaseURL: url, Key: "sk_car_test", Model: "sonic-3", Rate: 24000}
}

// TestSpeakClonesThenSynthesizes: one clone, then synthesis in that voice
// at the configured rate.
func TestSpeakClonesThenSynthesizes(t *testing.T) {
	t.Parallel()

	var synthVoice string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/voices/clone":
			cloneID(t, w, "voice-abc")
		case "/tts/bytes":
			synthVoice = voiceOf(t, r)
			writeRaw(t, w, s16le(5, 6, 7))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	v := core.Voice{ID: "speaker-0", Ref: []int16{1, 2, 3, 4}}

	pcm, err := cartesia.New(testConfig(server.URL)).Speak(context.Background(), "hello", "en", v)
	if err != nil {
		t.Fatalf("Speak returned error: %v", err)
	}

	if synthVoice != "voice-abc" {
		t.Fatalf("synthesis used voice %q, want the cloned voice-abc", synthVoice)
	}

	if len(pcm.Data) != 3 || pcm.Data[2] != 7 || pcm.Rate != 24000 {
		t.Fatalf("decoded audio = %+v, want 3 samples @ 24000", pcm)
	}
}

// TestCloneCachedPerSpeaker: the same speaker is cloned exactly once, however
// many takes they get — the clone is the expensive, one-time step.
func TestCloneCachedPerSpeaker(t *testing.T) {
	t.Parallel()

	var clones atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/voices/clone":
			clones.Add(1)
			cloneID(t, w, "voice-abc")
		case "/tts/bytes":
			writeRaw(t, w, s16le(1))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client := cartesia.New(testConfig(server.URL))
	v := core.Voice{ID: "speaker-0", Ref: []int16{1, 2, 3, 4}}

	for range 3 {
		if _, err := client.Speak(context.Background(), "line", "en", v); err != nil {
			t.Fatalf("Speak returned error: %v", err)
		}
	}

	if got := clones.Load(); got != 1 {
		t.Fatalf("cloned %d times, want exactly 1", got)
	}
}

// TestSpeakWithoutReferenceUsesVoiceID: no reference means the voice id is an
// existing Cartesia voice — used directly, with no clone call.
func TestSpeakWithoutReferenceUsesVoiceID(t *testing.T) {
	t.Parallel()

	var synthVoice string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/voices/clone" {
			t.Fatal("cloned despite no reference")
		}

		synthVoice = voiceOf(t, r)
		writeRaw(t, w, s16le(1))
	}))
	defer server.Close()

	v := core.Voice{ID: "preexisting-voice"}

	if _, err := cartesia.New(testConfig(server.URL)).Speak(context.Background(), "hi", "en", v); err != nil {
		t.Fatalf("Speak returned error: %v", err)
	}

	if synthVoice != "preexisting-voice" {
		t.Fatalf("synthesis used voice %q, want preexisting-voice", synthVoice)
	}
}

// TestRequestsAreAuthenticatedAndVersioned: every call carries the bearer key
// and the pinned API version, or Cartesia rejects it.
func TestRequestsAreAuthenticatedAndVersioned(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk_car_test" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}

		if r.Header.Get("Cartesia-Version") == "" {
			t.Fatal("missing Cartesia-Version header")
		}

		writeRaw(t, w, s16le(1))
	}))
	defer server.Close()

	if _, err := cartesia.New(testConfig(server.URL)).
		Speak(context.Background(), "hi", "en", core.Voice{ID: "v"}); err != nil {
		t.Fatalf("Speak returned error: %v", err)
	}
}

// TestServerErrorIsTransient: a 5xx maps to ErrTransient so the retry layer
// backs off instead of dropping the take.
func TestServerErrorIsTransient(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "busy", http.StatusBadGateway)
	}))
	defer server.Close()

	_, err := cartesia.New(testConfig(server.URL)).
		Speak(context.Background(), "hi", "en", core.Voice{ID: "v"})
	if !errors.Is(err, core.ErrTransient) {
		t.Fatalf("5xx error = %v, want ErrTransient", err)
	}
}
