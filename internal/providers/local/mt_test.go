package local_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/providers/local"
)

// TestTranslate: MT posts a chat-completions request with the configured
// model and reads the translation from the first choice.
func TestTranslate(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q, want /chat/completions", r.URL.Path)
		}

		if body := decodeReq(t, r); body["model"] != "llama3.1" {
			t.Fatalf("model = %v, want llama3.1", body["model"])
		}

		reply(t, w, map[string]any{
			"choices": []any{
				map[string]any{"message": map[string]any{"role": "assistant", "content": " hello "}},
			},
		})
	}))
	defer server.Close()

	out, err := local.New(testConfig(server.URL)).
		Translate(context.Background(), core.Transcript{Text: "ciao", Lang: "it"}, "en", core.MTOpts{})
	if err != nil || out != "hello" {
		t.Fatalf("Translate = (%q, %v), want (hello, nil)", out, err)
	}
}

// TestTranslateEmptyChoicesIsTransient: a reply with no choices is a routing
// hiccup the retry layer should ride out, not a permanent failure.
func TestTranslateEmptyChoicesIsTransient(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reply(t, w, map[string]any{"choices": []any{}})
	}))
	defer server.Close()

	_, err := local.New(testConfig(server.URL)).
		Translate(context.Background(), core.Transcript{Text: "ciao"}, "en", core.MTOpts{})
	if !errors.Is(err, core.ErrTransient) {
		t.Fatalf("empty choices error = %v, want ErrTransient", err)
	}
}
