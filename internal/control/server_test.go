package control_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/control"
	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/core/session"
)

// fakeCaptions, fakeStreams and fakeMedia are one-session data-plane stubs:
// caption bytes, one TS write and a fixed HLS master, all keyed on demo/en.
type fakeCaptions struct{}

func (fakeCaptions) Document(slug, lang string) ([]byte, bool) {
	if slug == "demo" && lang == "en" {
		return []byte("WEBVTT\n"), true
	}

	return nil, false
}

type fakeStreams struct{}

func (fakeStreams) ServeTS(_ context.Context, w io.Writer, slug, lang string) bool {
	if slug != "demo" || lang != "en" {
		return false
	}

	_, err := w.Write([]byte{0x47})

	return err == nil
}

type fakeMedia struct{}

func (fakeMedia) MasterPlaylist(slug string) ([]byte, bool) {
	if slug == "demo" {
		return []byte("#EXTM3U\n"), true
	}

	return nil, false
}

func (fakeMedia) Open(string, string) (io.ReadSeekCloser, bool) { return nil, false }

// shortStateDir builds a state dir short enough for a UNIX socket path
// (macOS caps sun_path at 104 bytes; t.TempDir names overflow it).
func shortStateDir(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "pk")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}

	t.Cleanup(func() {
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			t.Errorf("clean state dir: %v", rmErr)
		}
	})

	return dir
}

// freePort reserves and releases a loopback port for the HTTP listener.
func freePort(t *testing.T) string {
	t.Helper()

	var lc net.ListenConfig

	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}

	addr := l.Addr().String()
	if closeErr := l.Close(); closeErr != nil {
		t.Fatalf("release port: %v", closeErr)
	}

	return addr
}

// startServer boots the full control plane as the daemon wires it and
// waits until it answers.
func startServer(t *testing.T) (base, token string, stop func() error) {
	t.Helper()

	state := shortStateDir(t)
	t.Setenv("PRUKKA_STATE", state)

	cfg := config.Default()
	cfg.Daemon.HTTP = freePort(t)

	store := session.NewStore()
	server := control.NewServer(cfg, store, newTestService(t, store, &stubPusher{}),
		control.DataPlane{Docs: fakeCaptions{}, Streams: fakeStreams{}, Media: fakeMedia{}},
		nil, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() { done <- server.Run(ctx) }()

	base = "http://" + cfg.Daemon.HTTP
	waitHealthy(t, base)

	raw, err := os.ReadFile(filepath.Clean(filepath.Join(state, "control.token")))
	if err != nil {
		t.Fatalf("read minted token: %v", err)
	}

	return base, strings.TrimSpace(string(raw)), func() error {
		cancel()

		select {
		case runErr := <-done:
			return runErr
		case <-time.After(15 * time.Second):
			return errors.New("server did not stop")
		}
	}
}

func TestRunAfterBindClosesListenersWhenTheHookFails(t *testing.T) {
	state := shortStateDir(t)
	t.Setenv("PRUKKA_STATE", state)
	cfg := config.Default()
	cfg.Daemon.HTTP = freePort(t)
	store := session.NewStore()
	server := control.NewServer(cfg, store, newTestService(t, store, &stubPusher{}),
		control.DataPlane{Docs: fakeCaptions{}, Streams: fakeStreams{}, Media: fakeMedia{}},
		nil, slog.New(slog.DiscardHandler))

	want := errors.New("startup cleanup failed")
	if err := server.RunAfterBind(t.Context(), func() error { return want }); !errors.Is(err, want) {
		t.Fatalf("RunAfterBind error = %v, want hook failure", err)
	}

	var lc net.ListenConfig
	listener, err := lc.Listen(t.Context(), "tcp", cfg.Daemon.HTTP)
	if err != nil {
		t.Fatalf("HTTP listener leaked after hook failure: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close probe listener: %v", err)
	}
}

// waitHealthy polls /healthz until the server answers.
func waitHealthy(t *testing.T, base string) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if do(t, http.MethodGet, base+"/healthz", "", "") == http.StatusOK {
			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatal("server never became healthy")
}

// do sends one request and returns the status, draining and closing the
// body; connection errors report 0 so health polling can retry.
func do(t *testing.T, method, url, token, body string) int {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build %s %s: %v", method, url, err)
	}

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	reply, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}

	defer func() {
		if closeErr := reply.Body.Close(); closeErr != nil {
			t.Errorf("close reply body: %v", closeErr)
		}
	}()

	if _, drainErr := io.Copy(io.Discard, reply.Body); drainErr != nil {
		t.Errorf("drain reply body: %v", drainErr)
	}

	return reply.StatusCode
}

// getJSON fetches one open read endpoint and decodes its reply.
func getJSON(t *testing.T, url string, out any) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatalf("build GET %s: %v", url, err)
	}

	reply, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}

	defer func() {
		if closeErr := reply.Body.Close(); closeErr != nil {
			t.Errorf("close reply body: %v", closeErr)
		}
	}()

	if reply.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", url, reply.StatusCode)
	}

	if decodeErr := json.NewDecoder(reply.Body).Decode(out); decodeErr != nil {
		t.Fatalf("decode %s: %v", url, decodeErr)
	}
}

func writeJSON(t *testing.T, method, url, token, body string, out any) int {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build %s %s: %v", method, url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	reply, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() {
		if closeErr := reply.Body.Close(); closeErr != nil {
			t.Errorf("close reply body: %v", closeErr)
		}
	}()

	if reply.StatusCode == http.StatusOK {
		if decodeErr := json.NewDecoder(reply.Body).Decode(out); decodeErr != nil {
			t.Fatalf("decode %s %s: %v", method, url, decodeErr)
		}
	} else if _, drainErr := io.Copy(io.Discard, reply.Body); drainErr != nil {
		t.Errorf("drain reply body: %v", drainErr)
	}

	return reply.StatusCode
}

// TestServerServesTheWholeSurface drives every HTTP-facing seam of the
// real control plane over the wire.
func TestServerServesTheWholeSurface(t *testing.T) {
	base, token, stop := startServer(t)

	assertGatewayReads(t, base, token)
	assertSessionLifecycle(t, base, token)
	assertDataPlane(t, base)
	assertSSEDelivers(t, base, "demo")

	if removed := do(t, http.MethodDelete, base+"/api/v1/sessions/demo", token, ""); removed != http.StatusOK {
		t.Fatalf("delete = %d, want 200", removed)
	}

	// A canceled context is a clean shutdown, not an error.
	if err := stop(); err != nil {
		t.Fatalf("Run returned error on shutdown: %v", err)
	}
}

// assertGatewayReads exercises public reads and the protected local-machine
// inventory, configuration and Doctor routes.
func assertGatewayReads(t *testing.T, base, token string) {
	t.Helper()

	for _, path := range []string{"/api/v1/languages", "/api/v1/stats"} {
		if got := do(t, http.MethodGet, base+path, "", ""); got != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, got)
		}
	}
	for _, path := range []string{"/api/v1/config", "/api/v1/devices", "/api/v1/doctor"} {
		if got := do(t, http.MethodGet, base+path, "", ""); got != http.StatusUnauthorized {
			t.Fatalf("tokenless GET %s = %d, want 401", path, got)
		}
		if got := do(t, http.MethodGet, base+path, token, ""); got != http.StatusOK {
			t.Fatalf("authenticated GET %s = %d, want 200", path, got)
		}
	}
}

// assertSessionLifecycle drives create → list → patch over REST with the
// minted token, and pins the tokenless rejection.
func assertSessionLifecycle(t *testing.T, base, token string) {
	t.Helper()

	if denied := do(t, http.MethodPost, base+"/api/v1/sessions", "", `{}`); denied != http.StatusUnauthorized {
		t.Fatalf("tokenless write = %d, want 401", denied)
	}

	assertCreateSessionREST(t, base, token)

	var listed struct {
		Sessions []struct {
			Slug        string  `json:"slug"`
			Status      string  `json:"status"`
			SourceURL   *string `json:"sourceUrl"`
			SourceLabel string  `json:"sourceLabel"`
		} `json:"sessions"`
	}

	getJSON(t, base+"/api/v1/sessions", &listed)

	if len(listed.Sessions) != 1 || listed.Sessions[0].Slug != "demo" ||
		listed.Sessions[0].Status != "starting" || nonEmpty(listed.Sessions[0].SourceURL) ||
		listed.Sessions[0].SourceLabel != "file://[local]" {
		t.Fatalf("sessions = %+v, want [demo]", listed.Sessions)
	}

	assertUpdateSessionREST(t, base, token)
}

func assertCreateSessionREST(t *testing.T, base, token string) {
	t.Helper()

	var created struct {
		Session struct {
			SourceURL   *string `json:"sourceUrl"`
			SourceLabel string  `json:"sourceLabel"`
			Status      string  `json:"status"`
		} `json:"session"`
	}
	createdStatus := writeJSON(t, http.MethodPost, base+"/api/v1/sessions", token,
		`{"slug":"demo","profile":"broadcast","sourceUrl":"file:///tmp/x.wav","langs":["it","en"]}`,
		&created)
	if createdStatus != http.StatusOK {
		t.Fatalf("create = %d, want 200", createdStatus)
	}
	if created.Session.Status != "starting" || nonEmpty(created.Session.SourceURL) ||
		created.Session.SourceLabel != "file://[local]" {
		t.Fatalf("created runtime/source = %+v, want starting and a public label", created.Session)
	}
}

func nonEmpty(value *string) bool {
	return value != nil && *value != ""
}

func assertUpdateSessionREST(t *testing.T, base, token string) {
	t.Helper()

	var patched struct {
		Session struct {
			Status string `json:"status"`
		} `json:"session"`
	}
	patchedStatus := writeJSON(t, http.MethodPatch, base+"/api/v1/sessions/demo", token,
		`{"addLangs":["fr"]}`, &patched)
	if patchedStatus != http.StatusOK {
		t.Fatalf("patch = %d, want 200", patchedStatus)
	}
	if patched.Session.Status != "starting" {
		t.Fatalf("patched status = %q, want starting", patched.Session.Status)
	}
}

// assertDataPlane exercises captions, live audio, the HLS master and the
// honest 404 for an unknown session.
func assertDataPlane(t *testing.T, base string) {
	t.Helper()

	for path, want := range map[string]int{
		"/demo/en/subs.vtt":  http.StatusOK,
		"/demo/en/audio.ts":  http.StatusOK,
		"/demo/master.m3u8":  http.StatusOK,
		"/ghost/en/subs.vtt": http.StatusNotFound,
	} {
		if got := do(t, http.MethodGet, base+path, "", ""); got != want {
			t.Fatalf("GET %s = %d, want %d", path, got, want)
		}
	}
}

// assertSSEDelivers connects to the event stream and reads the snapshot the
// server sends first — proof the SSE bridge is live end to end.
func assertSSEDelivers(t *testing.T, base, slug string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/events", http.NoBody)
	if err != nil {
		t.Fatalf("build sse request: %v", err)
	}

	reply, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open sse stream: %v", err)
	}

	defer func() {
		if closeErr := reply.Body.Close(); closeErr != nil && ctx.Err() == nil {
			t.Errorf("close sse body: %v", closeErr)
		}
	}()

	if ct := reply.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("sse content-type = %q", ct)
	}

	scanner := bufio.NewScanner(reply.Body)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), slug) {
			return
		}
	}

	t.Fatalf("sse stream ended without the %s snapshot: %v", slug, scanner.Err())
}
