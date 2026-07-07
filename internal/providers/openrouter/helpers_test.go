package openrouter_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/providers/openrouter"
)

// meterCall records one Add invocation.
type meterCall struct {
	session, kind string
	units, eur    float64
}

// fakeMeter captures usage reports for assertions.
type fakeMeter struct {
	calls []meterCall
}

// Add implements core.Meter.
func (m *fakeMeter) Add(session, kind string, units, eur float64) {
	m.calls = append(m.calls, meterCall{session: session, kind: kind, units: units, eur: eur})
}

// newClient wires a client against a test server.
func newClient(srv *httptest.Server, m core.Meter) *openrouter.Client {
	return openrouter.New(&openrouter.Config{
		Endpoint: openrouter.Endpoint{BaseURL: srv.URL, Key: "test-key", Timeout: 5 * time.Second},
		Models: openrouter.Models{
			STT:         "openai/whisper-large-v3",
			MT:          "google/gemini-2.5-flash",
			TTS:         "openai/gpt-audio-mini",
			Temperature: 0.2,
		},
		EURPerUSD: 0.9,
	}, m)
}

// utterance builds one second of audio at PTS 2 s.
func utterance() *core.Utterance {
	return &core.Utterance{
		Session: "demo",
		Track:   "main",
		Audio:   core.PCM{Data: make([]int16, 16000), Rate: 16000, Ch: 1, PTS: 2 * time.Second},
		Final:   true,
	}
}

// assertTranscriptionRequest checks the JSON transcription contract: a
// base64 WAV inside input_audio.
func assertTranscriptionRequest(t *testing.T, r *http.Request) {
	t.Helper()

	if r.URL.Path != "/audio/transcriptions" {
		t.Errorf("path = %q", r.URL.Path)
	}

	if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
		t.Errorf("authorization = %q", got)
	}

	var req struct {
		Model      string `json:"model"`
		Language   string `json:"language"`
		InputAudio struct {
			Data   string `json:"data"`
			Format string `json:"format"`
		} `json:"input_audio"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Errorf("decode request: %v", err)

		return
	}

	if req.Model != "openai/whisper-large-v3" || req.InputAudio.Format != "wav" {
		t.Errorf("request envelope = %+v", req)
	}

	if req.Language != "it" {
		t.Errorf("language = %q, want bare base tag", req.Language)
	}

	wav, decodeErr := base64.StdEncoding.DecodeString(req.InputAudio.Data)
	if decodeErr != nil || !strings.HasPrefix(string(wav), "RIFF") {
		t.Errorf("input_audio.data is not a base64 WAV (err %v)", decodeErr)
	}
}

// assertSTTMeterCall checks the usage reported for the transcription.
func assertSTTMeterCall(t *testing.T, m *fakeMeter) {
	t.Helper()

	if len(m.calls) != 1 {
		t.Fatalf("meter calls = %+v, want exactly one", m.calls)
	}

	call := m.calls[0]
	if call.session != "demo" || call.kind != "stt" || call.units != 1 {
		t.Fatalf("meter call = %+v, want demo/stt/1s", call)
	}

	// Real provider cost times the configured conversion, float-tolerant.
	if diff := call.eur - 0.0002*0.9; diff < -1e-12 || diff > 1e-12 {
		t.Fatalf("meter eur = %v, want ≈0.00018", call.eur)
	}
}

// assertChatRequest checks the chat-completions contract.
func assertChatRequest(t *testing.T, r *http.Request) {
	t.Helper()

	if r.URL.Path != "/chat/completions" {
		t.Errorf("path = %q", r.URL.Path)
	}

	var req struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Usage struct {
			Include bool `json:"include"`
		} `json:"usage"`
		Temperature float64 `json:"temperature"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Errorf("decode request: %v", err)

		return
	}

	if req.Model != "google/gemini-2.5-flash" || req.Temperature != 0.2 || !req.Usage.Include {
		t.Errorf("request envelope = %+v", req)
	}

	system := req.Messages[0].Content

	for _, want := range []string{
		"from Italian (it) into English (en)",
		"between 85% and 115%",
		"Mandatory terminology: CPU = CPU; prukka = Prukka.",
		"previous line one",
	} {
		if !strings.Contains(system, want) {
			t.Errorf("system prompt misses %q:\n%s", want, system)
		}
	}

	if req.Messages[1].Content != "ciao mondo" {
		t.Errorf("user content = %q", req.Messages[1].Content)
	}
}
