package speechengine

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWhisperServerArgsForwardThreads(t *testing.T) {
	t.Parallel()

	got := whisperServerArgs("model.bin", "it", 3, "43123", "/prukka-token", false)
	want := []string{
		"-m", "model.bin", "-l", "it", "-t", "3", "--host", "127.0.0.1",
		"--port", "43123", "--request-path", "/prukka-token",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("whisper args = %v, want %v", got, want)
	}
}

func TestWhisperServerArgsUseCancellationSafeBoundedCallDecode(t *testing.T) {
	t.Parallel()

	got := whisperServerArgs("call.bin", "it", 4, "43123", "/token", true)
	for _, sequence := range [][]string{
		{"-bo", "1"}, {"-ac", "512"},
	} {
		if !containsSequence(got, sequence) {
			t.Fatalf("fast whisper args %v do not contain %v", got, sequence)
		}
	}
	if slices.Contains(got, "-nt") {
		t.Fatalf("fast whisper args %v contain cancellation-unsafe -nt", got)
	}
}

func TestWhisperRequestFastDecodeDisablesTemperatureFallback(t *testing.T) {
	t.Parallel()

	request, err := whisperRequest(
		t.Context(), "http://127.0.0.1:43123/token", nil,
		whisperInferenceOptions{fastDecode: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := request.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	if got := request.FormValue("temperature_inc"); got != "0" {
		t.Fatalf("temperature_inc = %q, want 0", got)
	}
}

func TestValidateWhisperTranscriptRejectsDecoderLoops(t *testing.T) {
	t.Parallel()

	phrase := "Hello this is a clear test can you understand every word? "
	if err := validateWhisperTranscript(strings.Repeat(phrase, 4)); !errors.Is(err, errUnsafeWhisperTranscript) {
		t.Fatal("four repeated clauses passed the STT transcript guard")
	}
	if err := validateWhisperTranscript(strings.Repeat(phrase, 3)); err != nil {
		t.Fatalf("ordinary emphatic repetition was rejected: %v", err)
	}
	if err := validateWhisperTranscript("no no no no"); err != nil {
		t.Fatalf("short natural repetition was rejected: %v", err)
	}
	joinedLoop := "Can you un-" + strings.Repeat("u-", 30)
	if err := validateWhisperTranscript(joinedLoop); !errors.Is(err, errUnsafeWhisperTranscript) {
		t.Fatal("punctuation-joined decoder loop passed the STT transcript guard")
	}
}

func TestValidateWhisperTranscriptBoundsFanout(t *testing.T) {
	t.Parallel()

	oversized := strings.Repeat("x", whisperTextMaxBytes+1)
	if err := validateWhisperTranscript(oversized); !errors.Is(err, errUnsafeWhisperTranscript) {
		t.Fatal("oversized STT transcript passed the fanout guard")
	}
}

func containsSequence(values, sequence []string) bool {
	for i := 0; i+len(sequence) <= len(values); i++ {
		if slices.Equal(values[i:i+len(sequence)], sequence) {
			return true
		}
	}

	return false
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

// TestRealWhisperSurvivesSupersededPartial is an opt-in semantic regression for
// the native bundle boundary. Unit tests pin the safe argv, while this test
// proves a superseded partial is allowed to leave the actual whisper.cpp
// server cleanly before the same process decodes its final.
//
// Run with PRUKKA_TEST_ENGINE_BUNDLE pointing at a complete engine bundle.
func TestRealWhisperSurvivesSupersededPartial(t *testing.T) {
	bundle, model, voice := requireRealWhisperBundle(t)
	pcm, rate := prepareWhisperFixture(t, bundle, voice)

	server, base, err := startReadyWhisperServer(bundle, model, "en", 2, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if stopErr := server.stop(); stopErr != nil {
			t.Errorf("stop whisper-server: %v", stopErr)
		}
	})

	client, transport := newWhisperHTTPClient(sttHTTPTimeout)
	t.Cleanup(transport.CloseIdleConnections)

	var output bytes.Buffer
	transcriber := &whisperSegmentTranscriber{
		client: client, out: json.NewEncoder(&output), base: base,
		lang: "en", rate: rate, finalTimeout: 30 * time.Second, fastDecode: true,
	}
	segment := speechSegment{pcm: pcm, endSamples: int64(len(pcm))}
	if err := transcriber.partial(t.Context(), segment, func() bool { return false }); err != nil {
		t.Fatalf("complete superseded partial: %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("superseded partial emitted protocol output: %q", output.String())
	}
	if err := transcriber.transcribe(segment); err != nil {
		t.Fatalf("final inference after superseded partial: %v", err)
	}

	var final transcript
	if err := json.NewDecoder(&output).Decode(&final); err != nil {
		t.Fatalf("decode final transcript: %v", err)
	}
	if final.Text == nil || strings.TrimSpace(*final.Text) == "" {
		t.Fatal("final inference after superseded partial returned no speech")
	}
	if len(*final.Text) > 512 {
		t.Fatalf("final inference expanded %d bytes from two seconds of speech: %q", len(*final.Text), *final.Text)
	}
	t.Logf("final transcript after superseded partial: %q", *final.Text)
}

func requireRealWhisperBundle(t *testing.T) (bundle, model, voice string) {
	t.Helper()

	bundle = os.Getenv("PRUKKA_TEST_ENGINE_BUNDLE")
	if bundle == "" {
		t.Skip("set PRUKKA_TEST_ENGINE_BUNDLE to a complete native engine bundle")
	}
	model = filepath.Join(bundle, "models", "stt", "ggml-tiny-q5_1.bin")
	voice = filepath.Join(bundle, "models", "tts", "en_US-lessac-medium.onnx")
	for _, path := range []string{model, voice, filepath.Join(bundle, "whisper-server")} {
		//nolint:gosec // Every path belongs to the explicitly operator-selected opt-in test bundle.
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("native smoke prerequisite %s: %v", path, err)
		}
	}

	return bundle, model, voice
}

func prepareWhisperFixture(t *testing.T, bundle, voice string) (pcm []int16, rate int) {
	t.Helper()

	proc, err := startPiperProc(bundle, voice, t.TempDir())
	if err != nil {
		t.Fatalf("start Piper speech fixture: %v", err)
	}
	pcm, rate, synthErr := proc.synthesize(
		"Hello, this is a clear test. Can you understand every word?",
	)
	if closeErr := proc.close(); closeErr != nil {
		t.Errorf("close Piper speech fixture: %v", closeErr)
	}
	if synthErr != nil {
		t.Fatalf("synthesize speech fixture: %v", synthErr)
	}
	if rate != 16000 {
		pcm = resampleLinear(pcm, rate, 16000)
		rate = 16000
	}
	if limit := 2 * rate; len(pcm) > limit {
		pcm = pcm[:limit]
	}
	if len(pcm) < rate {
		t.Fatalf("speech fixture is only %d samples at %d Hz", len(pcm), rate)
	}

	return pcm, rate
}
