package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestIsLoopback(t *testing.T) {
	t.Parallel()

	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:6060", true},
		{"localhost:6060", true},
		{"[::1]:6060", true},
		{"0.0.0.0:6060", false},
		{"192.168.1.10:6060", false},
		{"example.com:6060", false},
		{"127.0.0.1", false}, // no port
		{"", false},
	}

	for _, c := range cases {
		if got := isLoopback(c.addr); got != c.want {
			t.Errorf("isLoopback(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

func TestServePprofEmptyAddrIsNoop(t *testing.T) {
	t.Parallel()

	if err := servePprof(context.Background(), "", slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("servePprof with empty addr = %v, want nil (disabled)", err)
	}
}

func TestServePprofRejectsNonLoopback(t *testing.T) {
	t.Parallel()

	err := servePprof(context.Background(), "0.0.0.0:6060", slog.New(slog.DiscardHandler))
	if err == nil {
		t.Fatal("servePprof accepted a non-loopback address; profiling must stay local")
	}
}

func TestServePprofReportsBindFailure(t *testing.T) {
	t.Parallel()

	var listenConfig net.ListenConfig

	listener, err := listenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy loopback address: %v", err)
	}
	defer func() {
		if closeErr := listener.Close(); closeErr != nil {
			t.Errorf("release occupied loopback address: %v", closeErr)
		}
	}()

	err = servePprof(t.Context(), listener.Addr().String(), slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "bind pprof server") {
		t.Fatalf("servePprof on occupied address = %v, want explicit bind failure", err)
	}
}

func TestServePprofServesThenStopsOnCancel(t *testing.T) {
	t.Parallel()

	addr := freeLoopbackAddr(t)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- servePprof(ctx, addr, slog.New(slog.DiscardHandler)) }()

	// The profiling index must answer while the server is up.
	if !waitForOK(t, "http://"+addr+"/debug/pprof/") {
		cancel()
		t.Fatal("pprof server never served /debug/pprof/")
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("servePprof returned %v after cancel, want clean shutdown", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("servePprof did not stop after ctx cancel")
	}
}

// freeLoopbackAddr reserves and releases a loopback port, returning its
// address for the server under test to bind.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()

	var lc net.ListenConfig

	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}

	addr := l.Addr().String()

	if closeErr := l.Close(); closeErr != nil {
		t.Fatalf("release loopback port: %v", closeErr)
	}

	return addr
}

// waitForOK polls url until it answers 2xx or a short budget elapses.
func waitForOK(t *testing.T, url string) bool {
	t.Helper()

	client := &http.Client{Timeout: 200 * time.Millisecond}

	for range 50 {
		req, reqErr := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
		if reqErr != nil {
			t.Fatalf("build request: %v", reqErr)
		}

		resp, err := client.Do(req)
		if err != nil {
			time.Sleep(20 * time.Millisecond)

			continue
		}

		ok := resp.StatusCode == http.StatusOK
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Errorf("close pprof response body: %v", closeErr)
		}

		if ok {
			return true
		}

		time.Sleep(20 * time.Millisecond)
	}

	return false
}
