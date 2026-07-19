package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/control"
	"github.com/ubyte-source/prukka/internal/paths"
)

// TestDashboardURLCarriesTheTokenWhenMinted: the launch fragment carries the
// token when minted and the display form never does; without a token the
// read-only dashboard still opens.
func TestDashboardURLCarriesTheTokenWhenMinted(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	launch, display := dashboardURL("http://127.0.0.1:8080")
	if launch != "http://127.0.0.1:8080/ui/" || display != launch {
		t.Fatalf("without a token dashboardURL = %q, %q", launch, display)
	}

	token, err := control.LoadOrCreateToken(paths.TokenPath())
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}

	launch, display = dashboardURL("http://127.0.0.1:8080")
	if !strings.HasSuffix(launch, "#token="+token) {
		t.Fatalf("launch URL = %q, want the fragment hand-off", launch)
	}
	if strings.Contains(display, token) {
		t.Fatalf("display URL = %q, want no secret in it", display)
	}
}

// TestOpeningTheDashboardNeverLogsTheToken: a failed open is the one place the
// launch URL meets a logger, and it is reached in normal use — headless Linux,
// SSH sessions and containers are supported hosts with no browser to open. The
// daemon's JSON log is persisted by journald, by a tee, by a scrollback: a
// secret written there is a secret leaked, so the warning must name the URL
// that carries none.
func TestOpeningTheDashboardNeverLogsTheToken(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ShellExecute opens a real browser instead of reporting failure")
	}
	t.Setenv("PRUKKA_STATE", t.TempDir())

	token, err := control.LoadOrCreateToken(paths.TokenPath())
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}

	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer daemon.Close()

	// What a headless host looks like to browser.OpenURL: no opener on PATH.
	t.Setenv("PATH", "")

	var logged bytes.Buffer
	openWhenReady(
		context.Background(),
		strings.TrimPrefix(daemon.URL, "http://"),
		slog.New(slog.NewJSONHandler(&logged, nil)),
	)

	if !strings.Contains(logged.String(), "opening dashboard") {
		t.Fatalf("the failed open went unreported: %q", logged.String())
	}
	if strings.Contains(logged.String(), token) {
		t.Fatalf("the control token reached the log: %s", logged.String())
	}
}

// TestHealthyProbesTheEndpoint: 200 is up, anything else — including a
// dead port — is down.
func TestHealthyProbesTheEndpoint(t *testing.T) {
	t.Parallel()

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer down.Close()

	ctx := context.Background()

	if !healthy(ctx, up.Client(), up.URL) {
		t.Fatal("a 200 endpoint reported unhealthy")
	}

	if healthy(ctx, down.Client(), down.URL) {
		t.Fatal("a 503 endpoint reported healthy")
	}

	dead := up.URL
	up.Close()

	if healthy(ctx, http.DefaultClient, dead) {
		t.Fatal("a closed port reported healthy")
	}
}
