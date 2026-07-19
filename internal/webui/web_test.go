package webui_test

import (
	"io"
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
	if cache := index.Header.Get("Cache-Control"); cache != "no-store" {
		t.Fatalf("index Cache-Control = %q, want no-store", cache)
	}

	if ct := index.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("index Content-Type = %q, want html", ct)
	}
	indexBody, err := io.ReadAll(index.Body)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if !strings.Contains(string(indexBody), `name="prukka-api-base" content="same-origin"`) {
		t.Fatal("embedded index does not select the same-origin API")
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
	if cache := app.Header.Get("Cache-Control"); cache != "no-store" {
		t.Fatalf("app Cache-Control = %q, want no-store", cache)
	}
}

// TestHandlerRewritesIndexForEveryMethod: the file server answers POST
// like GET, so the index rewrite must not be method-gated — a same-origin
// form POST would otherwise render the hosted index, whose API base
// points the dashboard at the viewer's machine instead of the daemon.
func TestHandlerRewritesIndexForEveryMethod(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(webui.Handler())
	defer server.Close()

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		for _, path := range []string{"/", "/index.html"} {
			body := fetchBody(t, method, server.URL+path)
			if !strings.Contains(body, `name="prukka-api-base" content="same-origin"`) {
				t.Fatalf("%s %s did not serve the rewritten index", method, path)
			}
			if strings.Contains(body, `content="http://127.0.0.1:8080"`) {
				t.Fatalf("%s %s served the hosted index", method, path)
			}
		}
	}
}

// fetchBody issues one request with the test's context and returns the
// body of the expected 200 response.
func fetchBody(t *testing.T, method, url string) string {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, url, http.NoBody)
	if err != nil {
		t.Fatalf("build request %s %s: %v", method, url, err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	body, readErr := io.ReadAll(resp.Body)
	if closeErr := resp.Body.Close(); closeErr != nil {
		t.Logf("close body: %v", closeErr)
	}
	if readErr != nil {
		t.Fatalf("read %s %s: %v", method, url, readErr)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s = %d, want 200", method, url, resp.StatusCode)
	}

	return string(body)
}
