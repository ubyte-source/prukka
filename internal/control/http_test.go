package control

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestSensitiveRESTReadsRequireTokenWithoutLeakingPaths(t *testing.T) {
	t.Parallel()

	tempPath := filepath.Join(t.TempDir(), "models", "speech.bin")
	homePath := filepath.Join(string(filepath.Separator), "home", "operator", ".config", "prukka")
	diagnostic := tempPath + " " + homePath

	for _, path := range []string{"/api/v1/config", "/api/v1/devices", "/api/v1/doctor"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			calls := 0
			handler := requireControlToken("expected-token", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				if _, err := w.Write([]byte(diagnostic)); err != nil {
					t.Errorf("write sensitive fixture: %v", err)
				}
			}))

			for _, method := range []string{http.MethodGet, http.MethodHead} {
				assertSensitiveReadDenied(t, handler, method, path, "", tempPath, homePath)
				assertSensitiveReadDenied(
					t, handler, method, path, "Bearer wrong-token", tempPath, homePath,
				)
			}
			if calls != 0 {
				t.Fatalf("unauthorized %s reached handler %d times", path, calls)
			}

			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody)
			request.Header.Set("Authorization", "Bearer expected-token")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusOK || response.Body.String() != diagnostic || calls != 1 {
				t.Fatalf("authorized %s = (%d, %q, calls %d), want (200, sensitive body, 1)",
					path, response.Code, response.Body.String(), calls)
			}
		})
	}
}

func assertSensitiveReadDenied(
	t *testing.T,
	handler http.Handler,
	method, path, authorization, tempPath, homePath string,
) {
	t.Helper()

	request := httptest.NewRequestWithContext(t.Context(), method, path, http.NoBody)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("%s %s with authorization %q = %d, want 401", method, path, authorization, response.Code)
	}
	if strings.Contains(response.Body.String(), tempPath) || strings.Contains(response.Body.String(), homePath) {
		t.Fatalf("unauthorized %s leaked a local path: %q", path, response.Body.String())
	}
}

func TestControlAPIBoundsRequestBodies(t *testing.T) {
	t.Parallel()

	var readErr error
	handler := controlAPIHandler("expected-token", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
		var tooLarge *http.MaxBytesError
		if errors.As(readErr, &tooLarge) {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)

			return
		}
	}))
	body := strings.NewReader(strings.Repeat("x", controlBodyMaxBytes+1))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/sessions", body)
	request.Header.Set("Authorization", "Bearer expected-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	var tooLarge *http.MaxBytesError
	if !errors.As(readErr, &tooLarge) || response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body = (error %v, status %d), want MaxBytesError/413", readErr, response.Code)
	}
}

// fakeDocs serves one fixed caption document.
type fakeDocs struct{ body []byte }

func (f fakeDocs) Document(session, lang string) ([]byte, bool) {
	if session == "demo" && lang == "en" && f.body != nil {
		return f.body, true
	}

	return nil, false
}

func TestMediaPathParsesSessionLangLeaf(t *testing.T) {
	t.Parallel()

	cases := []struct {
		path             string
		slug, lang, leaf string
		ok               bool
	}{
		{"/demo/en/subs.vtt", "demo", "en", "subs.vtt", true},
		{"/demo/en/audio.ts", "demo", "en", "audio.ts", true},
		{"/demo/en", "", "", "", false},
		{"/demo/en/x/y", "", "", "", false},
		{"//en/subs.vtt", "", "", "", false},
		{"/demo//subs.vtt", "", "", "", false},
	}

	for _, tc := range cases {
		slug, lang, leaf, ok := mediaPath(tc.path)
		if slug != tc.slug || lang != tc.lang || leaf != tc.leaf || ok != tc.ok {
			t.Fatalf("mediaPath(%q) = (%q,%q,%q,%v), want (%q,%q,%q,%v)",
				tc.path, slug, lang, leaf, ok, tc.slug, tc.lang, tc.leaf, tc.ok)
		}
	}
}

func TestHLSPathSplitsSlugAndRest(t *testing.T) {
	t.Parallel()

	cases := []struct {
		path       string
		slug, rest string
		ok         bool
	}{
		{"/demo/master.m3u8", "demo", "master.m3u8", true},
		{"/demo/en/index.m3u8", "demo", "en/index.m3u8", true},
		{"/demo", "", "", false},
	}

	for _, tc := range cases {
		slug, rest, ok := hlsPath(tc.path)
		if slug != tc.slug || rest != tc.rest || ok != tc.ok {
			t.Fatalf("hlsPath(%q) = (%q,%q,%v), want (%q,%q,%v)",
				tc.path, slug, rest, ok, tc.slug, tc.rest, tc.ok)
		}
	}
}

// TestHostGuardLoopbackBind: on a loopback bind only loopback Host headers
// pass — the DNS-rebinding defense.
func TestHostGuardLoopbackBind(t *testing.T) {
	t.Parallel()

	guarded := hostGuard("127.0.0.1:8080", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		host string
		want int
	}{
		{"127.0.0.1:8080", http.StatusOK},
		{"localhost:8080", http.StatusOK},
		{"Localhost", http.StatusOK},
		{"[::1]:8080", http.StatusOK},
		{"attacker.example:8080", http.StatusMisdirectedRequest},
		{"192.168.1.10:8080", http.StatusMisdirectedRequest},
	}

	for _, tc := range cases {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", http.NoBody)
		r.Host = tc.host
		response := httptest.NewRecorder()
		guarded.ServeHTTP(response, r)

		if response.Code != tc.want {
			t.Fatalf("Host %q: status = %d, want %d", tc.host, response.Code, tc.want)
		}
	}
}

// TestHostGuardWideBind pins the opt-out: a non-loopback bind is an
// explicit operator choice, so any Host passes untouched.
func TestHostGuardWideBind(t *testing.T) {
	t.Parallel()

	guarded := hostGuard("0.0.0.0:8080", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", http.NoBody)
	r.Host = "stream.example.org"
	response := httptest.NewRecorder()
	guarded.ServeHTTP(response, r)

	if response.Code != http.StatusOK {
		t.Fatalf("wide bind refused Host %q: status = %d", r.Host, response.Code)
	}
}

// TestSecurityHeaders pins the browser hardening applied to every response.
func TestSecurityHeaders(t *testing.T) {
	t.Parallel()

	handler := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/config", http.NoBody)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	assertHeaderContains(t, response.Header(), "Content-Security-Policy", "frame-ancestors 'none'")
	assertHeaderContains(t, response.Header(), "Permissions-Policy", "microphone=()")
	assertHeaderContains(t, response.Header(), "Referrer-Policy", "no-referrer")
	assertHeaderContains(t, response.Header(), "X-Content-Type-Options", "nosniff")
	assertHeaderContains(t, response.Header(), "X-Frame-Options", "DENY")
	assertHeaderContains(t, response.Header(), "Cache-Control", "no-store")
}

func assertHeaderContains(t *testing.T, headers http.Header, name, want string) {
	t.Helper()

	if got := headers.Get(name); !strings.Contains(got, want) {
		t.Fatalf("%s = %q, want it to contain %q", name, got, want)
	}
}

// TestCORSMiddleware: same-origin and the configured external origin pass,
// preflights are answered, and a foreign origin cannot trigger the handler.
func TestCORSMiddleware(t *testing.T) {
	t.Parallel()

	var calls int
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusTeapot)
	})
	handler := corsMiddleware("https://prukka.ubyte.it", next)

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sessions", http.NoBody)
	request.Header.Set("Origin", "https://prukka.ubyte.it")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "https://prukka.ubyte.it" {
		t.Fatalf("allowed origin got %q", got)
	}

	request = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sessions", http.NoBody)
	request.Header.Set("Origin", "https://evil.example")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("foreign origin status = %d, want 403", response.Code)
	}
	if calls != 1 {
		t.Fatalf("foreign origin reached handler; calls = %d, want 1", calls)
	}

	request = httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/sessions", http.NoBody)
	request.Header.Set("Origin", "http://example.com")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusTeapot {
		t.Fatalf("same-origin request status = %d, want downstream status", response.Code)
	}

	request = httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/api/v1/sessions", http.NoBody)
	request.Header.Set("Origin", "https://prukka.ubyte.it")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code == http.StatusTeapot {
		t.Fatal("preflight fell through to the next handler")
	}
	// Every method the gateway binds must survive the preflight; PUT
	// carries UpdateConfig for the hosted dashboard.
	methods := response.Header().Get("Access-Control-Allow-Methods")
	for _, method := range []string{
		http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
	} {
		if !strings.Contains(methods, method) {
			t.Fatalf("preflight methods %q lack %s", methods, method)
		}
	}
}

func TestCORSMiddlewareRejectsForeignOriginWhenExternalCORSDisabled(t *testing.T) {
	t.Parallel()

	called := false
	handler := corsMiddleware("", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/devices", http.NoBody)
	request.Header.Set("Origin", "https://evil.example")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden || called {
		t.Fatalf("foreign request = (status %d, called %v), want (403, false)", response.Code, called)
	}
}

func TestCORSMiddlewareRejectsOriginlessCrossSiteAPIOnly(t *testing.T) {
	t.Parallel()

	calls := 0
	handler := corsMiddleware("", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}))

	api := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/doctor", http.NoBody)
	api.Header.Set("Sec-Fetch-Site", "cross-site")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, api)
	if response.Code != http.StatusForbidden || calls != 0 {
		t.Fatalf("cross-site API = (status %d, calls %d), want (403, 0)", response.Code, calls)
	}

	media := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/demo/en/audio.ts", http.NoBody)
	media.Header.Set("Sec-Fetch-Site", "cross-site")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, media)
	if response.Code != http.StatusNoContent || calls != 1 {
		t.Fatalf("cross-site media = (status %d, calls %d), want (204, 1)", response.Code, calls)
	}
}

// failingCloseTree serves one segment from a file whose Close always fails.
type failingCloseTree struct{ body []byte }

func (failingCloseTree) MasterPlaylist(string) ([]byte, bool) { return nil, false }

func (f failingCloseTree) Open(_, _ string) (io.ReadSeekCloser, bool) {
	return failingCloseFile{bytes.NewReader(f.body)}, true
}

type failingCloseFile struct{ *bytes.Reader }

func (failingCloseFile) Close() error { return errors.New("stale handle") }

// TestServeHLSSurvivesMediaCloseFailure: a failing close after the body
// went out must never disturb the committed response.
func TestServeHLSSurvivesMediaCloseFailure(t *testing.T) {
	t.Parallel()

	handler := rootHandler(DataPlane{Docs: fakeDocs{}, Media: failingCloseTree{body: []byte("segment")}})
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/demo/it/seg-1.ts", http.NoBody)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Body.String(); got != "segment" {
		t.Fatalf("body = %q, want the full segment", got)
	}
}

// TestRootHandlerRoutesTheDataPlane: known pairs serve, unknown 404,
// everything else — non-GETs included — lands on the dashboard.
func TestRootHandlerRoutesTheDataPlane(t *testing.T) {
	t.Parallel()

	handler := rootHandler(DataPlane{Docs: fakeDocs{body: []byte("WEBVTT\n")}, Media: emptyTree{}})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/demo/en/subs.vtt", http.NoBody))

	if response.Code != http.StatusOK || response.Body.String() != "WEBVTT\n" {
		t.Fatalf("known pair = (%d, %q), want the document", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ghost/en/subs.vtt", http.NoBody))

	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown pair = %d, want 404", response.Code)
	}

	// Three segments with a non-legacy leaf are an HLS path served by the
	// media tree; an empty tree answers 404.
	response = httptest.NewRecorder()
	handler.ServeHTTP(response,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/demo/en/index.m3u8", http.NoBody))

	if response.Code != http.StatusNotFound {
		t.Fatalf("HLS path on an empty tree = %d, want 404", response.Code)
	}

	for _, request := range []*http.Request{
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody),
		httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/demo/en/subs.vtt", http.NoBody),
	} {
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		if response.Code != http.StatusTemporaryRedirect ||
			response.Header().Get("Location") != "/ui/" {
			t.Fatalf("%s %s = (%d, %q), want the dashboard redirect",
				request.Method, request.URL.Path, response.Code, response.Header().Get("Location"))
		}
	}
}

// emptyTree is a media tree with no sessions.
type emptyTree struct{}

func (emptyTree) MasterPlaylist(string) ([]byte, bool)          { return nil, false }
func (emptyTree) Open(string, string) (io.ReadSeekCloser, bool) { return nil, false }
