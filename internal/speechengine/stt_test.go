package speechengine

import (
	"bytes"
	"context"
	"encoding/json"
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
		"--model", "base", "--protocol-version", "2",
		"--rate", "24000", "--language", "IT-ch", "--threads", "3",
		"--silence-hang", "160ms", "--max-window", "2s", "--min-speech", "120ms",
		"--partial-stride", "250ms", "--fast-decode",
	})
	if parseErr != nil {
		t.Fatalf("parseSTTOptions = (%+v, %v)", opts, parseErr)
	}
	want := sttOptions{
		model: "base", language: "it", rate: 24000, threads: 3, protocol: 2,
		tuning: sttTuning{
			silenceHang: 160 * time.Millisecond, maxWindow: 2 * time.Second,
			minSpeech: 120 * time.Millisecond, partialStride: 250 * time.Millisecond,
		},
		fastDecode: true,
	}
	if opts != want {
		t.Fatalf("parseSTTOptions = %+v, want %+v", opts, want)
	}
	defaults, defaultErr := parseSTTOptions([]string{
		"--model", "base", "--protocol-version", "2",
	})
	if defaultErr != nil || defaults.threads != 1 {
		t.Fatalf("default threads = (%d, %v), want (1, nil)", defaults.threads, defaultErr)
	}

	for _, tail := range [][]string{
		{"--rate", "not-a-number"},
		{"--unknown", "value"},
		{"positional"},
		{"--language", "../../private"},
		{"--rate", "1"},
		{"--rate", "192001"},
		{"--threads", "0"},
		{"--threads", "-1"},
		{"--threads", "65"},
		{"--silence-hang", "1ms"},
		{"--partial-stride", "6s", "--max-window", "5s"},
		{"--min-speech", "6s", "--max-window", "5s"},
	} {
		args := append([]string{"--model", "base", "--protocol-version", "2"}, tail...)
		if _, invalidErr := parseSTTOptions(args); invalidErr == nil {
			t.Fatalf("parseSTTOptions(%q) accepted invalid arguments", args)
		}
	}
	for _, args := range [][]string{
		{"--model", "base"},
		{"--model", "base", "--protocol-version", "1"},
		{"--model", "base", "--protocol-version", "3"},
	} {
		_, err := parseSTTOptions(args)
		if err == nil || !strings.Contains(err.Error(), "protocol-version must be 2") {
			t.Fatalf("protocol args %q error = %v", args, err)
		}
	}
}

func TestFinalTimeoutTracksTheProfileWindow(t *testing.T) {
	t.Parallel()

	if got := finalTimeoutFor(defaultSTTTuning()); got != 30*time.Second {
		t.Fatalf("broadcast final timeout = %v, want 30s", got)
	}
	call := defaultSTTTuning()
	call.maxWindow = 5 * time.Second
	if got := finalTimeoutFor(call); got != 30*time.Second {
		t.Fatalf("call final timeout = %v, want 30s", got)
	}
	extended := defaultSTTTuning()
	extended.maxWindow = 11 * time.Second
	if got := finalTimeoutFor(extended); got != 33*time.Second {
		t.Fatalf("extended-window final timeout = %v, want 33s", got)
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
	if got := firstNonAuto("it", "en"); got != "it" {
		t.Fatalf("firstNonAuto pinned result = %q, want it", got)
	}
	if got := firstNonAuto("auto", "it"); got != "it" {
		t.Fatalf("firstNonAuto detected result = %q, want it", got)
	}
	if got := firstNonAuto("", "auto"); got != "" {
		t.Fatalf("firstNonAuto without concrete tag = %q", got)
	}
}

func TestEnergyEndpointerCutsAfterSpeechAndSilence(t *testing.T) {
	t.Parallel()

	const rate = 1000

	endpoint := &energyEndpointer{rate: rate, tuning: defaultSTTTuning()}
	voiced := filledSamples(300, 20000)
	// Exactly the silence hang: the segment cuts on the frame that completes
	// it, so the whole tail lands inside this segment.
	silence := filledSamples(int(sttSilenceHang*rate/time.Second), 0)

	if got := endpoint.push(voiced); len(got) != 0 {
		t.Fatalf("speech alone emitted %d segments", len(got))
	}
	segments := endpoint.push(silence)
	if len(segments) != 1 {
		t.Fatalf("speech plus hang emitted %d segments, want 1", len(segments))
	}
	if got := len(segments[0].pcm); got != len(voiced)+len(silence) {
		t.Fatalf("segment samples = %d, want %d", got, len(voiced)+len(silence))
	}
	if got := segments[0].endSamples; got != int64(len(voiced)+len(silence)) {
		t.Fatalf("segment end_samples = %d, want %d", got, len(voiced)+len(silence))
	}
	if tail := endpoint.flush(); tail.pcm != nil {
		t.Fatalf("flush after endpoint = %v, want nil", tail)
	}
}

func TestEnergyEndpointerDropsSilentWindow(t *testing.T) {
	t.Parallel()

	const rate = 100

	endpoint := &energyEndpointer{rate: rate, tuning: defaultSTTTuning()}
	if segments := endpoint.push(filledSamples(rate*10, 0)); len(segments) != 0 {
		t.Fatalf("silent window emitted %d segments", len(segments))
	}
	if wantMax := int(sttPreRoll * rate / time.Second); len(endpoint.buf) > wantMax {
		t.Fatalf("silent pre-roll samples = %d, want at most %d", len(endpoint.buf), wantMax)
	}
}

func TestEnergyEndpointerDropsShortNoiseBurstAtCeiling(t *testing.T) {
	t.Parallel()

	const rate = 1000
	tuning := sttTuning{
		silenceHang: 160 * time.Millisecond, maxWindow: 2 * time.Second,
		minSpeech: 120 * time.Millisecond, partialStride: 250 * time.Millisecond,
	}
	endpoint := &energyEndpointer{rate: rate, tuning: tuning}
	click := filledSamples(20, 20000)
	silence := filledSamples(2*rate, 0)
	if segments := endpoint.push(append(click, silence...)); len(segments) != 0 {
		t.Fatalf("short noise burst emitted %d segments", len(segments))
	}
	if endpoint.sawSpeech {
		t.Fatal("discarded noise burst leaked speech state")
	}
}

func TestEnergyEndpointerDropsShortNoiseBurstAtEOF(t *testing.T) {
	t.Parallel()

	tuning := defaultSTTTuning()
	tuning.minSpeech = 120 * time.Millisecond
	endpoint := &energyEndpointer{rate: 1000, tuning: tuning}
	endpoint.push(filledSamples(20, 20000))
	if segment := endpoint.flush(); segment.pcm != nil {
		t.Fatalf("short EOF noise emitted %d samples", len(segment.pcm))
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
	endpoint := &energyEndpointer{rate: 16000, tuning: defaultSTTTuning()}
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

func TestWhisperTranscribePlainTextFallback(t *testing.T) {
	t.Parallel()

	server := newWhisperStub(t, "", "legacy transcript")

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

	server := newWhisperStub(t, "text/plain", "123")

	text, language, err := whisperTranscribe(t.Context(), server.Client(), server.URL, nil, 16000)
	if err != nil || text != "123" || language != "" {
		t.Fatalf("plain JSON literal reply = %q/%q, %v", text, language, err)
	}
}

func TestWhisperTranscribeAcceptsEmptyJSONTranscript(t *testing.T) {
	t.Parallel()

	server := newWhisperStub(t, "", `{"text":"","language":"it"}`)

	text, language, err := whisperTranscribe(t.Context(), server.Client(), server.URL, nil, 16000)
	if err != nil || text != "" || language != "it" {
		t.Fatalf("empty JSON reply = %q/%q, %v", text, language, err)
	}
}

func TestUnsafeFinalTranscriptIsDroppedWithoutKillingSTTLane(t *testing.T) {
	t.Parallel()

	phrase := "Hello this is a clear test can you understand every word? "
	server := newWhisperStub(t, contentTypeJSON,
		fmt.Sprintf(`{"text":%q,"language":"it"}`, strings.Repeat(phrase, 4)))

	var output bytes.Buffer
	transcriber := whisperSegmentTranscriber{
		client: server.Client(), out: json.NewEncoder(&output), base: server.URL,
		lang: "it", rate: 16000, finalTimeout: finalTimeoutFor(defaultSTTTuning()),
	}
	if err := transcriber.transcribe(speechSegment{pcm: []int16{1, -1}, endSamples: 32000}); err != nil {
		t.Fatalf("unsafe final terminated STT lane: %v", err)
	}

	var got transcript
	if err := json.NewDecoder(&output).Decode(&got); err != nil {
		t.Fatalf("decode safe replacement final: %v", err)
	}
	if !got.Final || got.Text == nil || *got.Text != "" || got.EndSamples != 32000 {
		t.Fatalf("safe replacement final = %+v, want empty final at sample 32000", got)
	}
}

func TestUnsafePartialTranscriptIsDroppedWithoutProtocolOutput(t *testing.T) {
	t.Parallel()

	phrase := "Hello this is a clear test can you understand every word? "
	server := newWhisperStub(t, contentTypeJSON,
		fmt.Sprintf(`{"text":%q,"language":"it"}`, strings.Repeat(phrase, 4)))

	var output bytes.Buffer
	transcriber := whisperSegmentTranscriber{
		client: server.Client(), out: json.NewEncoder(&output), base: server.URL,
		lang: "it", rate: 16000,
	}
	if err := transcriber.partial(
		t.Context(), speechSegment{pcm: []int16{1, -1}, endSamples: 16000}, func() bool { return true },
	); err != nil {
		t.Fatalf("unsafe partial terminated STT lane: %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("unsafe partial emitted protocol data: %q", output.String())
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

			server := newWhisperStub(t, contentTypeJSON, test.body)

			_, _, err := whisperTranscribe(t.Context(), server.Client(), server.URL, nil, 16000)
			if err == nil || !strings.Contains(err.Error(), "JSON shape") {
				t.Fatalf("body %s error = %v, want JSON shape error", test.body, err)
			}
		})
	}
}

func TestWhisperTranscribeRejectsMalformedDeclaredJSON(t *testing.T) {
	t.Parallel()

	server := newWhisperStub(t, contentTypeJSON, `{`)

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

// TestPartialPacerPacesByStride: a partial dispatches only after a full
// stride of new audio, and a second one cannot start while the first is in
// flight — a slow CPU stretches the pace instead of queueing inferences.
func TestPartialPacerPacesByStride(t *testing.T) {
	t.Parallel()

	const rate = 16000
	stride := int(sttPartialStride * time.Duration(rate) / time.Second)
	started := make(chan speechSegment, 2)
	release := make(chan struct{})
	pacer := &partialPacer{rate: rate, stride: sttPartialStride, partialTimeout: time.Minute, run: func(
		_ context.Context, segment speechSegment, _ func() bool,
	) error {
		started <- segment
		<-release

		return nil
	}}

	pacer.observe(make([]int16, stride-1), true, int64(stride-1))
	select {
	case <-started:
		t.Fatal("partial dispatched below the stride")
	default:
	}

	pacer.observe(make([]int16, stride), false, int64(stride))
	select {
	case <-started:
		t.Fatal("partial dispatched without speech")
	default:
	}

	pacer.observe(make([]int16, stride), true, int64(stride))
	first := <-started
	if len(first.pcm) != stride || first.endSamples != int64(stride) {
		t.Fatalf("snapshot = (%d samples, end %d), want (%d, %d)",
			len(first.pcm), first.endSamples, stride, stride)
	}

	pacer.observe(make([]int16, 3*stride), true, int64(3*stride))
	select {
	case <-started:
		t.Fatal("second partial dispatched while the first was in flight")
	default:
	}

	close(release)
	pacer.drain()
}

// TestPartialPacerCutStalesWithoutCancelingNativeInference: an endpoint must
// suppress a late partial without disconnecting the reused whisper server.
func TestPartialPacerCutStalesWithoutCancelingNativeInference(t *testing.T) {
	t.Parallel()

	const rate = 16000
	stride := int(sttPartialStride * time.Duration(rate) / time.Second)
	probes := make(chan struct {
		ctx  context.Context
		live func() bool
	}, 1)
	release := make(chan struct{})
	result := make(chan error, 1)
	pacer := &partialPacer{rate: rate, stride: sttPartialStride, partialTimeout: time.Minute, run: func(
		ctx context.Context, _ speechSegment, live func() bool,
	) error {
		probes <- struct {
			ctx  context.Context
			live func() bool
		}{ctx: ctx, live: live}
		select {
		case <-ctx.Done():
			result <- ctx.Err()

			return ctx.Err()
		case <-release:
			result <- nil

			return nil
		}
	}}

	pacer.observe(make([]int16, stride), true, int64(stride))
	probe := <-probes
	if !probe.live() {
		t.Fatal("gate reported stale before any cut")
	}

	pacer.cut()
	if probe.live() {
		t.Fatal("gate still live after the segment cut")
	}
	if err := probe.ctx.Err(); err != nil {
		t.Fatalf("cut canceled the reused native inference: %v", err)
	}
	select {
	case err := <-result:
		t.Fatalf("cut terminated the reused native inference: %v", err)
	default:
	}

	close(release)
	pacer.drain()
	if err := <-result; err != nil {
		t.Fatalf("stale inference did not finish cleanly: %v", err)
	}

	if pacer.dispatched != 0 {
		t.Fatalf("dispatched = %d after cut, want 0", pacer.dispatched)
	}
}

func TestPartialPacerShutdownCancelsInFlightInference(t *testing.T) {
	t.Parallel()

	const rate = 16000
	stride := int(sttPartialStride * time.Duration(rate) / time.Second)
	started := make(chan func() bool, 1)
	result := make(chan error, 1)
	pacer := &partialPacer{rate: rate, stride: sttPartialStride, partialTimeout: time.Minute, run: func(
		ctx context.Context, _ speechSegment, live func() bool,
	) error {
		started <- live
		<-ctx.Done()
		result <- ctx.Err()

		return ctx.Err()
	}}

	pacer.observe(make([]int16, stride), true, int64(stride))
	live := <-started
	pacer.shutdown()
	if live() {
		t.Fatal("shutdown left the in-flight partial live")
	}
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown inference error = %v, want context canceled", err)
	}
}

func TestDiscardedNoiseWindowCutsPartialEpoch(t *testing.T) {
	t.Parallel()

	const rate = 1000
	tuning := sttTuning{
		silenceHang: 160 * time.Millisecond, maxWindow: 2 * time.Second,
		minSpeech: 120 * time.Millisecond, partialStride: 250 * time.Millisecond,
	}
	endpoint := &energyEndpointer{rate: rate, tuning: tuning}
	generation := endpoint.generation
	started := make(chan int, 2)
	stale := make(chan bool, 1)
	releaseFirst := make(chan struct{})
	run := 0
	pacer := &partialPacer{
		rate: rate, stride: tuning.partialStride, partialTimeout: time.Minute,
		run: func(_ context.Context, _ speechSegment, live func() bool) error {
			run++
			started <- run
			if run == 1 {
				<-releaseFirst
				stale <- live()
			}

			return nil
		},
	}

	// A click marks the window as speech and can earn a partial before it is
	// rejected for failing the minimum-speech threshold at the 2 s ceiling.
	endpoint.push(append(filledSamples(20, 20000), filledSamples(230, 0)...))
	syncPartialBoundary(endpoint, pacer, &generation)
	buf, sawSpeech := endpoint.live()
	pacer.observe(buf, sawSpeech, endpoint.totalSamples)
	if got := <-started; got != 1 {
		t.Fatalf("first partial run = %d", got)
	}

	endpoint.push(filledSamples(1750, 0))
	syncPartialBoundary(endpoint, pacer, &generation)
	close(releaseFirst)
	pacer.drain()
	if live := <-stale; live {
		t.Fatal("discarded noise partial remained live")
	}
	if pacer.dispatched != 0 {
		t.Fatalf("dispatched after discarded window = %d, want 0", pacer.dispatched)
	}

	// The next real turn earns its first partial at one fresh stride, rather
	// than inheriting the discarded window's cadence.
	endpoint.push(filledSamples(250, 20000))
	syncPartialBoundary(endpoint, pacer, &generation)
	buf, sawSpeech = endpoint.live()
	pacer.observe(buf, sawSpeech, endpoint.totalSamples)
	if got := <-started; got != 2 {
		t.Fatalf("next-turn partial run = %d", got)
	}
	pacer.drain()
}

func TestSegmentWorkerKeepsCaptureSideUnblocked(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	first := true
	worker := newInterruptingSegmentWorker(func(speechSegment) error {
		if first {
			first = false
			close(started)
			<-release
		}

		return nil
	}, nil)

	if err := worker.submit(speechSegment{pcm: []int16{1}, endSamples: 1}); err != nil {
		t.Fatal(err)
	}
	<-started

	queued := make(chan error, 1)
	go func() { queued <- worker.submit(speechSegment{pcm: []int16{2}, endSamples: 2}) }()
	select {
	case err := <-queued:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("second endpoint blocked behind active final inference")
	}

	close(release)
	if err := worker.close(); err != nil {
		t.Fatal(err)
	}
}

func TestQueuedFinalsBlockLaterPartialInference(t *testing.T) {
	t.Parallel()

	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	releaseSecond := make(chan struct{})
	runner := &stagedSegmentRunner{
		started: [2]chan struct{}{firstStarted, secondStarted},
		release: [2]<-chan struct{}{releaseFirst, releaseSecond},
	}
	worker := newInterruptingSegmentWorker(runner.run, nil)
	mustSubmitSegment(t, worker, 1)
	<-firstStarted
	mustSubmitSegment(t, worker, 2)

	partialStarted := make(chan speechSegment, 1)
	pacer := &partialPacer{
		rate: 1000, stride: 20 * time.Millisecond, partialTimeout: time.Minute, blocked: worker.hasPending,
		run: func(_ context.Context, segment speechSegment, _ func() bool) error {
			partialStarted <- segment

			return nil
		},
	}
	buf := make([]int16, 20)
	pacer.observe(buf, true, 20)
	assertNoPartial(t, partialStarted, "later partial overtook the queued finals")

	close(releaseFirst)
	<-secondStarted
	pacer.observe(buf, true, 20)
	assertNoPartial(t, partialStarted, "later partial overtook the active second final")

	close(releaseSecond)
	waitForFinalBacklog(t, worker)
	pacer.observe(buf, true, 20)
	assertPartialEndSamples(t, partialStarted, 20)
	pacer.drain()
	closeSegmentWorker(t, worker)
}

type stagedSegmentRunner struct {
	started [2]chan struct{}
	release [2]<-chan struct{}
	next    int
}

func (r *stagedSegmentRunner) run(speechSegment) error {
	stage := r.next
	r.next++
	close(r.started[stage])
	<-r.release[stage]

	return nil
}

func mustSubmitSegment(t *testing.T, worker *segmentWorker, endSamples int64) {
	t.Helper()

	if err := worker.submit(speechSegment{pcm: []int16{1}, endSamples: endSamples}); err != nil {
		t.Fatal(err)
	}
}

func assertNoPartial(t *testing.T, started <-chan speechSegment, message string) {
	t.Helper()

	select {
	case <-started:
		t.Fatal(message)
	default:
	}
}

func waitForFinalBacklog(t *testing.T, worker *segmentWorker) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for worker.hasPending() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if worker.hasPending() {
		t.Fatal("final backlog did not drain")
	}
}

func assertPartialEndSamples(t *testing.T, started <-chan speechSegment, want int64) {
	t.Helper()

	select {
	case got := <-started:
		if got.endSamples != want {
			t.Fatalf("partial end_samples = %d, want %d", got.endSamples, want)
		}
	case <-time.After(time.Second):
		t.Fatal("partial did not resume after the final backlog drained")
	}
}

func closeSegmentWorker(t *testing.T, worker *segmentWorker) {
	t.Helper()

	if err := worker.close(); err != nil {
		t.Fatal(err)
	}
}

func TestSegmentWorkerFailureInterruptsHeldOpenInput(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("final inference failed")
	reader, writer := io.Pipe()
	t.Cleanup(func() {
		if closeErr := writer.Close(); closeErr != nil {
			t.Errorf("close pipe writer: %v", closeErr)
		}
	})
	worker := newInterruptingSegmentWorker(func(speechSegment) error { return wantErr }, func() {
		_ = reader.CloseWithError(wantErr)
	})

	if err := worker.submit(speechSegment{pcm: []int16{1}, endSamples: 1}); err != nil && !errors.Is(err, wantErr) {
		t.Fatalf("submit: %v", err)
	}

	readDone := make(chan error, 1)
	go func() {
		var one [1]byte
		_, err := reader.Read(one[:])
		readDone <- err
	}()

	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("held-open read completed without an interrupt error")
		}
	case <-time.After(time.Second):
		t.Fatal("worker failure did not interrupt held-open input")
	}
	if err := worker.close(); !errors.Is(err, wantErr) {
		t.Fatalf("worker close error = %v, want %v", err, wantErr)
	}
}

// TestTranscriptWireShape pins protocol v2: a partial line carries "partial",
// a final line "text"+"final", and both carry the exact end_samples boundary.
func TestTranscriptWireShape(t *testing.T) {
	t.Parallel()

	var ready bytes.Buffer
	if err := writeSTTReady(&ready); err != nil {
		t.Fatal(err)
	}
	if got := ready.String(); got != "{\"ready\":true}\n" {
		t.Fatalf("ready wire = %q", got)
	}

	partialText := "ciao a"
	partial, err := json.Marshal(transcript{Partial: &partialText, Language: "it", EndSamples: 320})
	if err != nil {
		t.Fatalf("marshal partial: %v", err)
	}
	if string(partial) != `{"partial":"ciao a","language":"it","end_samples":320}` {
		t.Fatalf("partial wire = %s", partial)
	}

	finalText := "ciao a tutti"
	final, err := json.Marshal(transcript{
		Text: &finalText, Language: "it", Final: true, EndSamples: 640,
	})
	if err != nil {
		t.Fatalf("marshal final: %v", err)
	}
	if string(final) != `{"text":"ciao a tutti","language":"it","final":true,"end_samples":640}` {
		t.Fatalf("final wire = %s", final)
	}
}
