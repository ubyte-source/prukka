package control

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

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

// TestCORSMiddleware: only the configured origin gets CORS, preflights
// answered, everyone else nothing.
func TestCORSMiddleware(t *testing.T) {
	t.Parallel()

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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

	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("foreign origin was granted %q", got)
	}

	request = httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/api/v1/sessions", http.NoBody)
	request.Header.Set("Origin", "https://prukka.ubyte.it")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code == http.StatusTeapot {
		t.Fatal("preflight fell through to the next handler")
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
