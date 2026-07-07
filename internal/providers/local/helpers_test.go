package local_test

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/ubyte-source/prukka/internal/providers/local"
)

// testConfig points all three stages at one test server; the handler routes
// by path, exactly as three real OpenAI-compatible servers would.
func testConfig(url string) *local.Config {
	return &local.Config{
		Endpoint: local.Endpoint{STT: url, MT: url, TTS: url},
		Models: local.Models{
			STT: "whisper-1", MT: "llama3.1", TTS: "tts-1",
			Voice: "alloy", Format: "pcm", Rate: 24000, Temperature: 0.2,
		},
	}
}

// s16le renders samples as little-endian s16, the raw PCM wire form.
func s16le(samples ...int16) []byte {
	raw := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(raw[i*2:], uint16(int32(s)&0xFFFF))
	}

	return raw
}

// reply writes a JSON body, failing the test on a write error.
func reply(t *testing.T, w http.ResponseWriter, v map[string]any) {
	t.Helper()

	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode reply: %v", err)
	}
}

// writeRaw writes raw bytes, failing the test on a write error.
func writeRaw(t *testing.T, w http.ResponseWriter, b []byte) {
	t.Helper()

	if _, err := w.Write(b); err != nil {
		t.Fatalf("write reply: %v", err)
	}
}

// decodeReq reads a JSON request body into a map for assertions.
func decodeReq(t *testing.T, r *http.Request) map[string]any {
	t.Helper()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	return m
}

// voiceField reads the string voice id out of a JSON request body.
func voiceField(t *testing.T, r *http.Request) string {
	t.Helper()

	voice, ok := decodeReq(t, r)["voice"].(string)
	if !ok {
		t.Fatal("voice field missing or not a string")
	}

	return voice
}
