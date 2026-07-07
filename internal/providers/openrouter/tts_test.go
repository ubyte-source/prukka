package openrouter_test

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
)

// ttsWire is the request shape of the audio-output chat contract.
type ttsWire struct {
	Model      string   `json:"model"`
	Modalities []string `json:"modalities"`
	Audio      struct {
		Voice  string `json:"voice"`
		Format string `json:"format"`
	} `json:"audio"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	Stream bool `json:"stream"`
}

// assertTTSRequest checks the contract: mandatory streaming,
// pcm16, dubbing-actor prompt with script tags.
func assertTTSRequest(t *testing.T, r *http.Request) {
	t.Helper()

	var req ttsWire
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Errorf("decode request: %v", err)

		return
	}

	assertTTSEnvelope(t, &req)
	assertTTSPrompt(t, &req)
}

// assertTTSEnvelope checks model, modalities, voice, format and streaming.
func assertTTSEnvelope(t *testing.T, req *ttsWire) {
	t.Helper()

	ok := req.Model == "openai/gpt-audio-mini" && req.Stream &&
		req.Audio.Format == "pcm16" && req.Audio.Voice == "nova" &&
		len(req.Modalities) == 2 && req.Modalities[1] == "audio"

	if !ok {
		t.Errorf("envelope = %+v (audio output requires stream:true)", req)
	}
}

// assertTTSPrompt checks the verbatim dubbing-actor contract.
func assertTTSPrompt(t *testing.T, req *ttsWire) {
	t.Helper()

	system := req.Messages[0].Content
	ok := strings.Contains(system, "EXACTLY as written") &&
		strings.Contains(system, "Italian (it)") &&
		req.Messages[1].Content == "<script>Il ponte è aperto.</script>"

	if !ok {
		t.Errorf("prompt breaks the verbatim contract:\n%s\nuser: %q", system, req.Messages[1].Content)
	}
}

// sseChunk frames one SSE event.
func sseChunk(t *testing.T, payload string) string {
	t.Helper()

	return "data: " + payload + "\n\n"
}

func TestSpeakContract(t *testing.T) {
	t.Parallel()

	// 480 samples of a ramp — 20 ms at 24 kHz — split across two deltas.
	pcm := make([]byte, 960)
	for i := range 480 {
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(int32(i-240)&0xFFFF))
	}

	first := base64.StdEncoding.EncodeToString(pcm[:400])
	second := base64.StdEncoding.EncodeToString(pcm[400:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertTTSRequest(t, r)
		w.Header().Set("Content-Type", "text/event-stream")

		body := sseChunk(t, `{"choices":[{"delta":{"audio":{"data":"`+first+`"}}}]}`) +
			sseChunk(t, `{"choices":[{"delta":{"audio":{"data":"`+second+`"}}}]}`) +
			sseChunk(t, `{"choices":[],"usage":{"cost":0.0002}}`) +
			"data: [DONE]\n\n"

		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write stream: %v", err)
		}
	}))
	defer srv.Close()

	m := &fakeMeter{}

	got, err := newClient(srv, m).ForSession("demo").
		Speak(t.Context(), "Il ponte è aperto.", "it", core.Voice{ID: "nova"})
	if err != nil {
		t.Fatalf("Speak returned error: %v", err)
	}

	assertSpeakResult(t, &got, m)
}

// assertSpeakResult checks the decoded PCM shape and ramp.
func assertSpeakResult(t *testing.T, got *core.PCM, m *fakeMeter) {
	t.Helper()

	ok := got.Rate == 24000 && got.Ch == 1 && len(got.Data) == 480 &&
		got.Data[0] == -240 && got.Data[479] == 239

	if !ok {
		t.Fatalf("PCM = %d samples @%d Hz ×%d (first=%d last=%d), want the 480-sample ramp @24000 ×1",
			len(got.Data), got.Rate, got.Ch, got.Data[0], got.Data[479])
	}

	assertTTSMeter(t, m)
}

// assertTTSMeter checks the metered seconds and converted cost.
func assertTTSMeter(t *testing.T, m *fakeMeter) {
	t.Helper()

	if len(m.calls) != 1 || m.calls[0].kind != "tts" {
		t.Fatalf("meter calls = %+v, want one tts entry", m.calls)
	}

	if units := m.calls[0].units; units < 0.02-1e-9 || units > 0.02+1e-9 {
		t.Fatalf("units = %v, want 0.02 audio seconds", units)
	}

	if diff := m.calls[0].eur - 0.0002*0.9; diff < -1e-12 || diff > 1e-12 {
		t.Fatalf("eur = %v, want cost × conversion", m.calls[0].eur)
	}
}

func TestSpeakRejectsEmptyStream(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		if _, err := w.Write([]byte("data: [DONE]\n\n")); err != nil {
			t.Errorf("write stream: %v", err)
		}
	}))
	defer srv.Close()

	_, err := newClient(srv, &fakeMeter{}).ForSession("demo").
		Speak(t.Context(), "x", "it", core.Voice{})
	if err == nil {
		t.Fatal("Speak accepted an audio-free stream")
	}
}
