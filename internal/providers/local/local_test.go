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

// TestServerErrorIsTransient: a 5xx maps to ErrTransient so the retry layer
// backs off instead of dropping the utterance permanently.
func TestServerErrorIsTransient(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, err := local.New(testConfig(server.URL)).
		Translate(context.Background(), core.Transcript{Text: "x"}, "en", core.MTOpts{})
	if !errors.Is(err, core.ErrTransient) {
		t.Fatalf("5xx error = %v, want ErrTransient", err)
	}
}

// TestClientErrorIsNotTransient: a 4xx is a caller problem, not a blip, so it
// must not be retried.
func TestClientErrorIsNotTransient(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad model", http.StatusBadRequest)
	}))
	defer server.Close()

	_, err := local.New(testConfig(server.URL)).
		Translate(context.Background(), core.Transcript{Text: "x"}, "en", core.MTOpts{})
	if err == nil || errors.Is(err, core.ErrTransient) {
		t.Fatalf("4xx error = %v, want a non-transient failure", err)
	}
}
