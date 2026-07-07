package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseSTTOptions(t *testing.T) {
	t.Parallel()

	// "IT-ch" exercises both lowercasing and base-subtag normalization: whisper
	// -l accepts only "auto" or a bare ISO-639-1 code, so the region is dropped.
	opts, parseErr := parseSTTOptions([]string{
		"--model", "base", "--rate", "24000", "--language", "IT-ch", "--threads", "3",
	})
	if parseErr != nil || opts.model != "base" || opts.rate != 24000 ||
		opts.language != "it" || opts.threads != 3 {
		t.Fatalf("parseSTTOptions = (%+v, %v)", opts, parseErr)
	}
	defaults, defaultErr := parseSTTOptions([]string{"--model", "base"})
	if defaultErr != nil || defaults.threads != 1 {
		t.Fatalf("default threads = (%d, %v), want (1, nil)", defaults.threads, defaultErr)
	}

	for _, args := range [][]string{
		{"--model", "base", "--rate", "not-a-number"},
		{"--model", "base", "--unknown", "value"},
		{"--model", "base", "positional"},
		{"--model", "base", "--language", "../../private"},
		{"--model", "base", "--rate", "1"},
		{"--model", "base", "--rate", "192001"},
		{"--model", "base", "--threads", "0"},
		{"--model", "base", "--threads", "-1"},
		{"--model", "base", "--threads", "65"},
	} {
		if _, invalidErr := parseSTTOptions(args); invalidErr == nil {
			t.Fatalf("parseSTTOptions(%q) accepted invalid arguments", args)
		}
	}
}

func TestIsNonSpeechPlaceholder(t *testing.T) {
	t.Parallel()

	for _, text := range []string{"[BLANK_AUDIO]", "[MUSIC]", "(wind blowing)", "[ Silence ]"} {
		if !isNonSpeechPlaceholder(text) {
			t.Errorf("isNonSpeechPlaceholder(%q) = false, want true", text)
		}
	}
	for _, text := range []string{"Buongiorno a tutti", "welcome [applause] back", "a", ""} {
		if isNonSpeechPlaceholder(text) {
			t.Errorf("isNonSpeechPlaceholder(%q) = true, want false", text)
		}
	}
}

func TestFirstNonAuto(t *testing.T) {
	t.Parallel()

	if got := firstNonAuto("", "auto", "it", "en"); got != "it" {
		t.Fatalf("firstNonAuto = %q, want it", got)
	}
	if got := firstNonAuto("", "auto"); got != "" {
		t.Fatalf("firstNonAuto without concrete tag = %q", got)
	}
}

func TestEnergyEndpointerCutsAfterSpeechAndSilence(t *testing.T) {
	t.Parallel()

	const rate = 1000

	endpoint := &energyEndpointer{rate: rate}
	voiced := filledSamples(300, 20000)
	silence := filledSamples(500, 0)

	if got := endpoint.push(voiced); len(got) != 0 {
		t.Fatalf("speech alone emitted %d segments", len(got))
	}
	segments := endpoint.push(silence)
	if len(segments) != 1 {
		t.Fatalf("speech plus hang emitted %d segments, want 1", len(segments))
	}
	if got := len(segments[0]); got != len(voiced)+len(silence) {
		t.Fatalf("segment samples = %d, want %d", got, len(voiced)+len(silence))
	}
	if tail := endpoint.flush(); tail != nil {
		t.Fatalf("flush after endpoint = %v, want nil", tail)
	}
}

func TestEnergyEndpointerDropsSilentWindow(t *testing.T) {
	t.Parallel()

	const rate = 100

	endpoint := &energyEndpointer{rate: rate}
	if segments := endpoint.push(filledSamples(rate*10, 0)); len(segments) != 0 {
		t.Fatalf("silent window emitted %d segments", len(segments))
	}
	if len(endpoint.buf) >= rate*10 {
		t.Fatal("silent window was not reset at the duration ceiling")
	}
}

func TestS16StreamDecoderPreservesSplitSamples(t *testing.T) {
	t.Parallel()

	want := []int16{-32768, -1, 0, 1, 32767}
	raw := int16ToBytes(want)
	decoder := &s16StreamDecoder{}
	got := make([]int16, 0, len(want))

	for _, part := range [][]byte{raw[:1], raw[1:4], raw[4:9], raw[9:]} {
		got = append(got, decoder.decode(part)...)
	}

	if decoder.pending {
		t.Fatal("decoder retained a byte after an even-length stream")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded samples = %v, want %v", got, want)
	}
}

func TestS16StreamDecoderReportsTrailingByte(t *testing.T) {
	t.Parallel()

	decoder := &s16StreamDecoder{}
	if got := decoder.decode([]byte{0x01}); len(got) != 0 || !decoder.pending {
		t.Fatalf("odd decode = %v, pending=%v", got, decoder.pending)
	}
}

func TestS16StreamDecoderReusesScratchWithoutEscapingEndpointer(t *testing.T) {
	t.Parallel()

	decoder := &s16StreamDecoder{}
	endpoint := &energyEndpointer{rate: 16000}
	endpoint.push(decoder.decode([]byte{1, 0, 2, 0}))
	decoder.decode([]byte{9, 0, 10, 0})
	if !slices.Equal(endpoint.buf, []int16{1, 2}) {
		t.Fatalf("endpointer retained decoder scratch: %v", endpoint.buf)
	}
}

func TestS16StreamDecoderDecodeHasZeroSteadyStateAllocations(t *testing.T) {
	const inputBytes = 8192

	raw := make([]byte, inputBytes)
	decoder := &s16StreamDecoder{}
	decoder.decode(raw) // Grow the reusable scratch outside the measurement.
	allocations := testing.AllocsPerRun(1000, func() {
		if got := len(decoder.decode(raw)); got != inputBytes/2 {
			panic(fmt.Sprintf("decoded %d samples", got))
		}
	})
	if allocations != 0 {
		t.Fatalf("steady-state allocations = %v, want 0", allocations)
	}
}

func BenchmarkS16StreamDecoderDecode(b *testing.B) {
	const inputBytes = 8192

	raw := make([]byte, inputBytes)
	decoder := &s16StreamDecoder{}
	decoded := decoder.decode(raw) // Grow scratch before the timed loop.
	b.SetBytes(inputBytes)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		decoded = decoder.decode(raw)
	}
	runtime.KeepAlive(decoded)
}

func TestRMS(t *testing.T) {
	t.Parallel()

	if got := rms(nil); got != 0 {
		t.Fatalf("rms(nil) = %v", got)
	}
	if got := rms([]int16{16384, -16384}); got < 0.49 || got > 0.51 {
		t.Fatalf("rms half-scale = %v, want about 0.5", got)
	}
}

func TestWhisperTranscribeJSON(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}
		if !bytes.Contains(body, []byte("RIFF")) {
			http.Error(w, "invalid wav", http.StatusBadRequest)

			return
		}
		w.Header().Set("Server", whisperServerName)
		w.Header().Set("Content-Type", contentTypeJSON)
		if _, err := io.WriteString(w, `{"text":"ciao","language":"it"}`); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	text, language, err := whisperTranscribe(
		t.Context(), server.Client(), server.URL, []int16{1, -1}, 16000,
	)
	if err != nil {
		t.Fatalf("whisperTranscribe: %v", err)
	}
	if text != "ciao" || language != "it" {
		t.Fatalf("reply = %q/%q, want ciao/it", text, language)
	}
}

func TestPinnedLanguageWinsOverDetection(t *testing.T) {
	t.Parallel()

	if got := firstNonAuto("it", "en"); got != "it" {
		t.Fatalf("firstNonAuto pinned result = %q, want it", got)
	}
	if got := firstNonAuto("auto", "it"); got != "it" {
		t.Fatalf("firstNonAuto detected result = %q, want it", got)
	}
}

func TestWhisperTranscribePlainTextFallback(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Server", whisperServerName)
		if _, err := io.WriteString(w, "legacy transcript"); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	text, language, err := whisperTranscribe(t.Context(), server.Client(), server.URL, nil, 16000)
	if err != nil {
		t.Fatalf("whisperTranscribe: %v", err)
	}
	if text != "legacy transcript" || language != "" {
		t.Fatalf("reply = %q/%q", text, language)
	}
}

func TestWhisperTranscribePlainJSONLiteralFallback(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Server", whisperServerName)
		w.Header().Set("Content-Type", "text/plain")
		if _, err := io.WriteString(w, "123"); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	text, language, err := whisperTranscribe(t.Context(), server.Client(), server.URL, nil, 16000)
	if err != nil || text != "123" || language != "" {
		t.Fatalf("plain JSON literal reply = %q/%q, %v", text, language, err)
	}
}

func TestWhisperTranscribeAcceptsEmptyJSONTranscript(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Server", whisperServerName)
		if _, err := io.WriteString(w, `{"text":"","language":"it"}`); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	text, language, err := whisperTranscribe(t.Context(), server.Client(), server.URL, nil, 16000)
	if err != nil || text != "" || language != "it" {
		t.Fatalf("empty JSON reply = %q/%q, %v", text, language, err)
	}
}

func TestWhisperTranscribeRejectsInvalidJSONShape(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		body string
	}{
		{name: "missing text", body: `{"error":"model failed"}`},
		{name: "null text", body: `{"text":null}`},
		{name: "wrong text type", body: `{"text":3}`},
		{name: "scalar", body: `"not an object"`},
		{name: "full language name", body: `{"text":"ciao","language":"italian"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Server", whisperServerName)
				w.Header().Set("Content-Type", contentTypeJSON)
				if _, err := io.WriteString(w, test.body); err != nil {
					t.Errorf("write response: %v", err)
				}
			}))
			defer server.Close()

			_, _, err := whisperTranscribe(t.Context(), server.Client(), server.URL, nil, 16000)
			if err == nil || !strings.Contains(err.Error(), "JSON shape") {
				t.Fatalf("body %s error = %v, want JSON shape error", test.body, err)
			}
		})
	}
}

func TestWhisperTranscribeRejectsMalformedDeclaredJSON(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Server", whisperServerName)
		w.Header().Set("Content-Type", contentTypeJSON)
		if _, err := io.WriteString(w, `{`); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	_, _, err := whisperTranscribe(t.Context(), server.Client(), server.URL, nil, 16000)
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("error = %v, want invalid JSON", err)
	}
}

func TestWhisperTranscribeRejectsHTTPError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "failed", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, _, err := whisperTranscribe(t.Context(), server.Client(), server.URL, nil, 16000)
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("error = %v, want HTTP status", err)
	}
}

func TestWhisperTranscribeRejectsGenericSuccessfulResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentTypeJSON)
		if _, err := io.WriteString(w, `{"text":"foreign"}`); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	_, _, err := whisperTranscribe(t.Context(), server.Client(), server.URL, nil, 16000)
	if err == nil || !strings.Contains(err.Error(), "Server header") {
		t.Fatalf("error = %v, want server identity error", err)
	}
}

func TestWhisperHTTPClientRefusesInferenceRedirect(t *testing.T) {
	t.Parallel()

	targetCalled := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetCalled <- struct{}{}
		writeWhisperHealth(w, http.StatusOK, `{"text":"redirected"}`)
	}))
	defer target.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		http.Redirect(w, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	client, transport := newWhisperHTTPClient(time.Second)
	defer transport.CloseIdleConnections()
	_, _, err := whisperTranscribe(t.Context(), client, redirect.URL, nil, 16000)
	if !errors.Is(err, errWhisperRedirect) {
		t.Fatalf("error = %v, want redirect refusal", err)
	}
	select {
	case <-targetCalled:
		t.Fatal("redirect target received the inference request")
	default:
	}
}

func TestWhisperRequestRejectsNonLoopbackBase(t *testing.T) {
	t.Parallel()

	if _, err := whisperRequest(t.Context(), "http://example.com:8080", nil); err == nil {
		t.Fatal("whisperRequest accepted a non-loopback base URL")
	}
}

func FuzzS16StreamDecoder(f *testing.F) {
	f.Add([]byte{0, 128, 255, 127, 1, 0}, uint8(1))
	f.Add([]byte{}, uint8(4))

	f.Fuzz(func(t *testing.T, raw []byte, split uint8) {
		if len(raw)%2 != 0 {
			raw = raw[:len(raw)-1]
		}
		index := 0
		decoder := &s16StreamDecoder{}
		var got []int16
		step := int(split)%7 + 1
		for index < len(raw) {
			end := min(index+step, len(raw))
			got = append(got, decoder.decode(raw[index:end])...)
			index = end
		}

		want := bytesToInt16(raw)
		if decoder.pending || !slices.Equal(got, want) {
			t.Fatalf("decoded %v (pending=%v), want %v", got, decoder.pending, want)
		}
	})
}
