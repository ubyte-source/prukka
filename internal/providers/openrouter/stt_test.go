package openrouter_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTranscribeContract(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertTranscriptionRequest(t, r)
		w.Header().Set("Content-Type", "application/json")

		reply := `{"text":" Ciao a tutti ","usage":{"seconds":1.0,"cost":0.0002}}`
		if _, err := w.Write([]byte(reply)); err != nil {
			t.Errorf("write reply: %v", err)
		}
	}))
	defer srv.Close()

	m := &fakeMeter{}

	got, err := newClient(srv, m).ForSession("demo").Transcribe(t.Context(), utterance(), "it-CH")
	if err != nil {
		t.Fatalf("Transcribe returned error: %v", err)
	}

	if got.Text != "Ciao a tutti" {
		t.Fatalf("Text = %q, want trimmed transcript", got.Text)
	}

	if got.Lang != "it-CH" {
		t.Fatalf("Lang = %q, want the hint when the reply names none", got.Lang)
	}

	if got.Span != [2]time.Duration{2 * time.Second, 3 * time.Second} {
		t.Fatalf("Span = %v, want [2s 3s]", got.Span)
	}

	assertSTTMeterCall(t, m)
}

func TestTranscribeRejectsEmptyAudio(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("request must not reach the server")
	}))
	defer srv.Close()

	u := utterance()
	u.Audio.Data = nil

	if _, err := newClient(srv, &fakeMeter{}).ForSession("demo").Transcribe(t.Context(), u, "it"); err == nil {
		t.Fatal("Transcribe accepted empty audio")
	}
}
