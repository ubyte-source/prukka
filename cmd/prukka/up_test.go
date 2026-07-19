package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/control"
	"github.com/ubyte-source/prukka/internal/core/config"
)

// TestDashboardURLCarriesTheTokenWhenMinted: the fragment carries the
// token when minted; without one the read-only dashboard still opens.
func TestDashboardURLCarriesTheTokenWhenMinted(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	if got := dashboardURL("http://127.0.0.1:8080"); got != "http://127.0.0.1:8080/ui/" {
		t.Fatalf("without a token dashboardURL = %q", got)
	}

	token, err := control.LoadOrCreateToken(config.TokenPath())
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}

	got := dashboardURL("http://127.0.0.1:8080")
	if !strings.HasSuffix(got, "#token="+token) {
		t.Fatalf("with a token dashboardURL = %q, want the fragment hand-off", got)
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
