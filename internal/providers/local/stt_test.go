package local_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/providers/local"
)

// TestTranscribe: STT posts the standard OpenAI multipart form (file, model
// and the language hint) to /audio/transcriptions and reads back the text.
func TestTranscribe(t *testing.T) {
	t.Parallel()

	var form string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/transcriptions" {
			t.Fatalf("path = %q, want /audio/transcriptions", r.URL.Path)
		}

		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "multipart/form-data") {
			t.Fatalf("content-type = %q, want multipart/form-data", ct)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read form: %v", err)
		}

		form = string(body)
		reply(t, w, map[string]any{"text": " ciao ", "language": "it"})
	}))
	defer server.Close()

	u := &core.Utterance{Audio: core.PCM{Data: []int16{1, 2}, Rate: 16000, Ch: 1}}

	tr, err := local.New(testConfig(server.URL)).Transcribe(context.Background(), u, "it")
	if err != nil {
		t.Fatalf("Transcribe returned error: %v", err)
	}

	if tr.Text != "ciao" || tr.Lang != "it" {
		t.Fatalf("Transcribe = %+v, want text ciao lang it", tr)
	}

	if !strings.Contains(form, `name="file"`) || !strings.Contains(form, "whisper-1") {
		t.Fatalf("form missing file part or model: %q", form)
	}

	if !strings.Contains(form, `name="language"`) {
		t.Fatal("language hint not forwarded to the transcription form")
	}
}

// TestTranscribeAutoOmitsLanguage: with no hint the form carries no language
// field, leaving detection to the server.
func TestTranscribeAutoOmitsLanguage(t *testing.T) {
	t.Parallel()

	var form string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read form: %v", err)
		}

		form = string(body)
		reply(t, w, map[string]any{"text": "hello", "language": "en"})
	}))
	defer server.Close()

	u := &core.Utterance{Audio: core.PCM{Data: []int16{1, 2}, Rate: 16000, Ch: 1}}

	tr, err := local.New(testConfig(server.URL)).Transcribe(context.Background(), u, core.LangAuto)
	if err != nil {
		t.Fatalf("Transcribe returned error: %v", err)
	}

	if tr.Lang != "en" {
		t.Fatalf("detected language = %q, want the server's en", tr.Lang)
	}

	if strings.Contains(form, `name="language"`) {
		t.Fatal("language field sent despite an auto hint")
	}
}
