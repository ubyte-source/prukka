package control

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
)

func TestSensitiveRESTReadsRequireTokenWithoutLeakingPaths(t *testing.T) {
	t.Parallel()

	tempPath := filepath.Join(t.TempDir(), "models", "speech.bin")
	homePath := filepath.Join(string(filepath.Separator), "home", "operator", ".config", "prukka")
	diagnostic := tempPath + " " + homePath

	for _, path := range []string{
		"/api/v1/config", "/api/v1/devices", "/api/v1/doctor", "/api/v1/engine",
	} {
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

// TestOpenRESTReadsSkipTheTokenWhileMutationsDoNot pins the boundary
// SECURITY.md publishes: narrowing or widening sensitiveControlRead has to
// move the policy text with it.
func TestOpenRESTReadsSkipTheTokenWhileMutationsDoNot(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/api/v1/sessions", "/api/v1/stats", "/api/v1/languages"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			reached := 0
			handler := requireControlToken("expected-token", http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
				reached++
			}))

			for _, method := range []string{http.MethodGet, http.MethodHead} {
				request := httptest.NewRequestWithContext(t.Context(), method, path, http.NoBody)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)

				if response.Code != http.StatusOK {
					t.Fatalf("tokenless %s %s = %d, want 200", method, path, response.Code)
				}
			}
			if reached != 2 {
				t.Fatalf("tokenless reads reached handler %d times, want 2", reached)
			}

			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, http.NoBody)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusUnauthorized || reached != 2 {
				t.Fatalf("tokenless POST %s = (%d, reached %d), want (401, 2)",
					path, response.Code, reached)
			}
		})
	}
}

// TestEveryGatewayReadRouteIsClassified derives the REST read surface from the
// proto descriptors, so a new route fails here instead of defaulting to
// unauthenticated. Both slices stay hand-written: ranging over the production
// list would make the check agree with whatever it was handed.
func TestEveryGatewayReadRouteIsClassified(t *testing.T) {
	t.Parallel()

	sensitive := []string{"/api/v1/config", "/api/v1/devices", "/api/v1/doctor", "/api/v1/engine"}
	public := []string{"/api/v1/languages", "/api/v1/sessions", "/api/v1/stats"}

	classified := slices.Sorted(slices.Values(slices.Concat(sensitive, public)))
	if annotated := gatewayReadPaths(t); !slices.Equal(annotated, classified) {
		t.Fatalf("annotated GET routes %v, classified %v — every read belongs to exactly one set",
			annotated, classified)
	}

	for _, path := range sensitive {
		if !sensitiveControlRead(path) {
			t.Errorf("GET %s is answered without a control token", path)
		}
	}
	for _, path := range public {
		if sensitiveControlRead(path) {
			t.Errorf("GET %s now demands a token; SECURITY.md publishes it as an open read", path)
		}
	}
}

// gatewayReadPaths reads every google.api.http GET annotation off the v1
// service descriptor: grpc-gateway keeps its compiled patterns unexported.
func gatewayReadPaths(t *testing.T) []string {
	t.Helper()

	service := v1.File_prukka_v1_control_proto.Services().ByName("Control")
	if service == nil {
		t.Fatal("prukka.v1.Control service descriptor is missing")
	}

	methods := service.Methods()
	paths := make([]string, 0, methods.Len())
	for i := range methods.Len() {
		options, ok := methods.Get(i).Options().(*descriptorpb.MethodOptions)
		if !ok {
			continue
		}
		rule, ok := proto.GetExtension(options, annotations.E_Http).(*annotations.HttpRule)
		if !ok || rule.GetGet() == "" {
			continue
		}
		paths = append(paths, rule.GetGet())
	}

	return slices.Sorted(slices.Values(paths))
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

type fakeDocs struct{ body []byte }

func (f fakeDocs) Document(slug, lang string) ([]byte, bool) {
	if slug == "demo" && lang == "en" && f.body != nil {
		return f.body, true
	}

	return nil, false
}

// recordingTree reports which (slug, rest) pair the router resolved.
type recordingTree struct{ slug, rest string }

func (*recordingTree) MasterPlaylist(string) ([]byte, bool) { return nil, false }

func (t *recordingTree) Open(slug, rest string) (io.ReadSeekCloser, bool) {
	t.slug, t.rest = slug, rest

	return nil, false
}

func TestSessionTreeRoutingSplitsSlugFromRendition(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		path, slug, rest string
	}{
		{"/demo/master.m3u8", "", ""}, // the master never reaches Open
		{"/demo/en/index.m3u8", "demo", "en/index.m3u8"},
		{"/demo/en/audio/seg00001.ts", "demo", "en/audio/seg00001.ts"},
		{"/demo/en/subs/seg00000.vtt", "demo", "en/subs/seg00000.vtt"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()

			tree := &recordingTree{}
			handler := rootHandler(DataPlane{Docs: fakeDocs{}, Media: tree})
			handler.ServeHTTP(httptest.NewRecorder(),
				httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, http.NoBody))

			if tree.slug != tc.slug || tree.rest != tc.rest {
				t.Fatalf("GET %s opened (%q, %q), want (%q, %q)", tc.path, tree.slug, tree.rest, tc.slug, tc.rest)
			}
		})
	}
}

func TestNonMediaPathsUnderASlugStillReachTheDashboard(t *testing.T) {
	t.Parallel()

	handler := rootHandler(DataPlane{Docs: fakeDocs{}, Media: emptyTree{}})

	for _, path := range []string{"/demo/index.html", "/demo/en/", "/demo/"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response,
			httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody))

		if response.Code != http.StatusTemporaryRedirect || response.Header().Get("Location") != "/ui/" {
			t.Fatalf("GET %s = (%d, %q), want the dashboard redirect",
				path, response.Code, response.Header().Get("Location"))
		}
	}
}

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
	// Every method the gateway binds must survive the preflight.
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

type failingCloseTree struct{ body []byte }

func (failingCloseTree) MasterPlaylist(string) ([]byte, bool) { return nil, false }

func (f failingCloseTree) Open(_, _ string) (io.ReadSeekCloser, bool) {
	return failingCloseFile{bytes.NewReader(f.body)}, true
}

type failingCloseFile struct{ *bytes.Reader }

func (failingCloseFile) Close() error { return errors.New("stale handle") }

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

type emptyTree struct{}

func (emptyTree) MasterPlaylist(string) ([]byte, bool)          { return nil, false }
func (emptyTree) Open(string, string) (io.ReadSeekCloser, bool) { return nil, false }

// oneSessionTree serves a fixed master playlist and segment under its session.
type oneSessionTree struct{ master, segment []byte }

func (t oneSessionTree) MasterPlaylist(slug string) ([]byte, bool) {
	return t.master, slug == "demo"
}

func (t oneSessionTree) Open(slug, _ string) (io.ReadSeekCloser, bool) {
	if slug != "demo" {
		return nil, false
	}

	return nopCloser{bytes.NewReader(t.segment)}, true
}

type nopCloser struct{ *bytes.Reader }

func (nopCloser) Close() error { return nil }

// countingStreams records every ServeTS call, so a probe that starts a live
// playout is visible.
type countingStreams struct{ calls int }

func (s *countingStreams) ServeTS(_ context.Context, w io.Writer, slug, lang string) bool {
	s.calls++
	if slug != "demo" || lang != "en" {
		return false
	}
	if _, err := w.Write([]byte("transport-stream")); err != nil {
		return false
	}

	return true
}

// TestHEADServesDataPlaneResourcesInsteadOfRedirecting: RFC 9110 §9.3.2 makes
// HEAD identical to GET but for the body, and HLS players, CDN origins and
// uptime monitors all probe that way.
func TestHEADServesDataPlaneResourcesInsteadOfRedirecting(t *testing.T) {
	t.Parallel()

	handler := rootHandler(DataPlane{
		Docs:    fakeDocs{body: []byte("WEBVTT\n")},
		Streams: &countingStreams{},
		Media:   oneSessionTree{master: []byte("#EXTM3U\n"), segment: []byte("segment-bytes")},
	})

	for _, tc := range []struct {
		path        string
		contentType string
		length      string
	}{
		{"/demo/en/subs.vtt", "text/vtt; charset=utf-8", "7"},
		{"/demo/master.m3u8", "application/vnd.apple.mpegurl", "8"},
		{"/demo/en/seg00001.ts", "video/mp2t", "13"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()

			response := httptest.NewRecorder()
			handler.ServeHTTP(response,
				httptest.NewRequestWithContext(t.Context(), http.MethodHead, tc.path, http.NoBody))

			if response.Code != http.StatusOK {
				t.Fatalf("HEAD %s = (%d, Location %q), want 200 with the resource headers",
					tc.path, response.Code, response.Header().Get("Location"))
			}
			if got := response.Header().Get("Content-Type"); got != tc.contentType {
				t.Fatalf("HEAD %s Content-Type = %q, want %q", tc.path, got, tc.contentType)
			}
			if got := response.Header().Get("Content-Length"); got != tc.length {
				t.Fatalf("HEAD %s Content-Length = %q, want %q", tc.path, got, tc.length)
			}
		})
	}
}

func TestHEADAudioAnswersFromHeadersWithoutOpeningAStream(t *testing.T) {
	t.Parallel()

	streams := &countingStreams{}
	handler := rootHandler(DataPlane{Docs: fakeDocs{}, Streams: streams, Media: emptyTree{}})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response,
		httptest.NewRequestWithContext(t.Context(), http.MethodHead, "/demo/en/audio.ts", http.NoBody))

	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "video/mp2t" {
		t.Fatalf("HEAD audio = (%d, %q, Location %q), want 200 video/mp2t",
			response.Code, response.Header().Get("Content-Type"), response.Header().Get("Location"))
	}
	if streams.calls != 0 {
		t.Fatalf("HEAD audio entered ServeTS %d times, want 0", streams.calls)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/demo/en/audio.ts", http.NoBody))

	if response.Code != http.StatusOK || streams.calls != 1 {
		t.Fatalf("GET audio = (%d, calls %d), want (200, 1)", response.Code, streams.calls)
	}
}
