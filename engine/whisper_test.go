package main

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWhisperServerArgsForwardThreads(t *testing.T) {
	t.Parallel()

	got := whisperServerArgs("model.bin", "it", 3, "43123", "/prukka-token")
	want := []string{
		"-m", "model.bin", "-l", "it", "-t", "3", "--host", "127.0.0.1",
		"--port", "43123", "--request-path", "/prukka-token",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("whisper args = %v, want %v", got, want)
	}
}

func TestWaitReadyRejectsGenericEndpoint(t *testing.T) {
	t.Parallel()

	path := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		path <- request.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	process := &childProcess{done: make(chan struct{})}
	if err := waitReady(server.URL, time.Second, process); err == nil {
		t.Fatal("waitReady accepted a generic HTTP endpoint")
	}
	if got := <-path; got != "/health" {
		t.Fatalf("health path = %q, want /health", got)
	}
}

func TestWaitReadyAcceptsPinnedWhisperHandshake(t *testing.T) {
	t.Parallel()

	path := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		path <- request.URL.Path
		writeWhisperHealth(w, http.StatusOK, whisperHealthReady)
	}))
	defer server.Close()

	process := &childProcess{done: make(chan struct{})}
	base := server.URL + "/prukka-0123456789abcdef0123456789abcdef"
	if err := waitReady(base, time.Second, process); err != nil {
		t.Fatalf("waitReady: %v", err)
	}
	if got := <-path; got != "/prukka-0123456789abcdef0123456789abcdef/health" {
		t.Fatalf("health path = %q", got)
	}
}

func TestWaitReadyReportsExitedProcess(t *testing.T) {
	t.Parallel()

	process := &childProcess{done: make(chan struct{}), waitErr: errors.New("boom")}
	close(process.done)
	err := waitReady("http://127.0.0.1:1", time.Second, process)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("waitReady error = %v", err)
	}
}

func TestWaitReadyReportsExitAfterHandshake(t *testing.T) {
	t.Parallel()

	process := &childProcess{done: make(chan struct{})}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeWhisperHealth(w, http.StatusOK, whisperHealthReady)
		process.waitErr = errors.New("exited after response")
		close(process.done)
	}))
	defer server.Close()

	err := waitReady(server.URL, time.Second, process)
	if err == nil || !strings.Contains(err.Error(), "exited after response") {
		t.Fatalf("waitReady error = %v", err)
	}
}

func TestValidateWhisperHealthIsStrictAndBounded(t *testing.T) {
	t.Parallel()

	tests := []struct {
		body        string
		contentType string
		name        string
		server      string
		status      int
		ready       bool
		wantErr     bool
	}{
		{
			name: "ready", status: http.StatusOK, server: whisperServerName,
			contentType: contentTypeJSON, body: whisperHealthReady, ready: true,
		},
		{
			name: "loading", status: http.StatusServiceUnavailable, server: whisperServerName,
			contentType: contentTypeJSON, body: whisperHealthLoading,
		},
		{
			name: "wrong status", status: http.StatusNoContent, server: whisperServerName,
			contentType: contentTypeJSON, wantErr: true,
		},
		{
			name: "wrong server", status: http.StatusOK, server: "generic",
			contentType: contentTypeJSON, body: whisperHealthReady, wantErr: true,
		},
		{
			name: "wrong content type", status: http.StatusOK, server: whisperServerName,
			contentType: "text/plain", body: whisperHealthReady, wantErr: true,
		},
		{
			name: "extra field", status: http.StatusOK, server: whisperServerName,
			contentType: contentTypeJSON, body: `{"status":"ok","extra":true}`, wantErr: true,
		},
		{
			name: "oversized", status: http.StatusOK, server: whisperServerName,
			contentType: contentTypeJSON, body: strings.Repeat("x", whisperHealthMaxBytes+1), wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			header := make(http.Header)
			header.Set("Server", test.server)
			header.Set("Content-Type", test.contentType)
			response := &http.Response{
				StatusCode: test.status,
				Header:     header,
				Body:       io.NopCloser(strings.NewReader(test.body)),
			}
			ready, err := validateWhisperHealth(response)
			if ready != test.ready || (err != nil) != test.wantErr {
				t.Fatalf("validateWhisperHealth = (%v, %v), want ready=%v error=%v", ready, err, test.ready, test.wantErr)
			}
		})
	}
}

func TestNewWhisperRequestPath(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{}, 32)
	for range 32 {
		path, err := newWhisperRequestPath()
		if err != nil {
			t.Fatal(err)
		}
		token := strings.TrimPrefix(path, "/prukka-")
		if token == path || len(token) != 32 {
			t.Fatalf("request path = %q", path)
		}
		if _, err := hex.DecodeString(token); err != nil {
			t.Fatalf("request path token %q: %v", token, err)
		}
		if _, duplicate := seen[path]; duplicate {
			t.Fatalf("duplicate request path %q", path)
		}
		seen[path] = struct{}{}
	}
}

func writeWhisperHealth(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Server", whisperServerName)
	w.Header().Set("Content-Type", contentTypeJSON)
	w.WriteHeader(status)
	if _, err := io.WriteString(w, body); err != nil {
		panic(err)
	}
}

func TestChildProcessStopWaitsAndIsConcurrentSafe(t *testing.T) {
	root := t.TempDir()
	symlinkTestExecutable(t, root)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := writer.Close(); closeErr != nil {
			t.Errorf("close child liveness pipe: %v", closeErr)
		}
	}()

	cmd := fakeChildProcessCommand(root)
	cmd.Env = append(os.Environ(), "PRUKKA_CHILD_PROCESS_HELPER=1")
	cmd.Stdin = reader
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err = reader.Close(); err != nil {
		t.Fatal(err)
	}

	process := &childProcess{cmd: cmd, done: make(chan struct{})}
	go func() {
		process.waitErr = cmd.Wait()
		close(process.done)
	}()

	stopped := make(chan error, 2)
	go func() { stopped <- process.stop() }()
	go func() { stopped <- process.stop() }()
	for range 2 {
		if stopErr := <-stopped; stopErr != nil {
			t.Fatalf("stop: %v", stopErr)
		}
	}
	select {
	case <-process.done:
	default:
		t.Fatal("stop returned before Wait completed")
	}
}

func fakeChildProcessCommand(root string) *exec.Cmd {
	var cmd *exec.Cmd
	if runtime.GOOS == goosWindows {
		cmd = exec.CommandContext(
			context.Background(), `.\fake-piper.exe`, "-test.run=^TestChildProcessHelper$",
		)
	} else {
		cmd = exec.CommandContext(
			context.Background(), "./fake-piper", "-test.run=^TestChildProcessHelper$",
		)
	}
	cmd.Dir = root

	return cmd
}

func TestChildProcessHelper(_ *testing.T) {
	if os.Getenv("PRUKKA_CHILD_PROCESS_HELPER") != "1" {
		return
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}
