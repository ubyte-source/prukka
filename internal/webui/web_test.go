package webui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/webui"
)

// get issues one test GET with the test's context.
func get(t *testing.T, url string) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatalf("build request %s: %v", url, err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}

	return resp
}

// TestHandlerServesTheEmbeddedApp: the dashboard must ship inside the
// binary — index and the compiled bundle respond from the embedded tree.
func TestHandlerServesTheEmbeddedApp(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(webui.Handler())
	defer server.Close()

	index := get(t, server.URL+"/")
	defer func() {
		if closeErr := index.Body.Close(); closeErr != nil {
			t.Logf("close body: %v", closeErr)
		}
	}()

	if index.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", index.StatusCode)
	}

	if ct := index.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("index Content-Type = %q, want html", ct)
	}

	app := get(t, server.URL+"/app.js")
	defer func() {
		if closeErr := app.Body.Close(); closeErr != nil {
			t.Logf("close body: %v", closeErr)
		}
	}()

	if app.StatusCode != http.StatusOK {
		t.Fatalf("GET /app.js = %d, want 200 (bundle missing from the embed)", app.StatusCode)
	}
}
