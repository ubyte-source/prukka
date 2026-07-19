package speechengine

import (
	"bytes"
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
	"sync"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/nativewire"
)

func TestParseSTTOptions(t *testing.T) {
	t.Parallel()

	// "IT-ch" exercises both lowercasing and base-subtag normalization: whisper
	// -l accepts only "auto" or a bare ISO-639-1 code, so the region is dropped.
	opts, parseErr := parseSTTOptions([]string{
		"--model", "base", "--protocol-version", "2",
		"--rate", "24000", "--language", "IT-ch", "--threads", "3",
		"--silence-hang", "160ms", "--max-window", "2s", "--min-speech", "120ms",
		"--fast-decode",
	})
	if parseErr != nil {
		t.Fatalf("parseSTTOptions = (%+v, %v)", opts, parseErr)
	}
	want := sttOptions{
		model: "base", language: "it", rate: 24000, threads: 3, protocol: 2,
		tuning: nativewire.STTTuning{
			SilenceHang: 160 * time.Millisecond, MaxWindow: 2 * time.Second,
			MinSpeech: 120 * time.Millisecond,
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
	call.MaxWindow = 5 * time.Second
	if got := finalTimeoutFor(call); got != 30*time.Second {
		t.Fatalf("call final timeout = %v, want 30s", got)
	}
	extended := defaultSTTTuning()
	extended.MaxWindow = 11 * time.Second
	if got := finalTimeoutFor(extended); got != 33*time.Second {
		t.Fatalf("extended-window final timeout = %v, want 33s", got)
	}
}

func TestNonSpeechOnly(t *testing.T) {
	t.Parallel()

	for _, text := range []string{
		"[BLANK_AUDIO]", "[MUSIC]", "(wind blowing)", "[ Silence ]", "[music] (rain)",
	} {
		if !nonSpeechOnly(text) {
			t.Errorf("nonSpeechOnly(%q) = false, want true", text)
		}
	}
	// A window bracket-bookended by stings still carries the sentence between
	// them: blanking it silently loses committed speech.
	for _, text := range []string{
		"Buongiorno a tutti", "welcome [applause] back", "a", "",
		"[Music] Buongiorno a tutti [Music]", "(applause) benvenuti (applause)", "[incomplete",
	} {
		if nonSpeechOnly(text) {
			t.Errorf("nonSpeechOnly(%q) = true, want false", text)
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
	tuning := nativewire.STTTuning{
		SilenceHang: 160 * time.Millisecond, MaxWindow: 2 * time.Second,
		MinSpeech: 120 * time.Millisecond,
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
	tuning.MinSpeech = 120 * time.Millisecond
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
		t.Context(), server.Client(), server.URL, []int16{1, -1}, 16000, whisperInferenceOptions{},
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

	text, language, err := whisperTranscribe(
		t.Context(), server.Client(), server.URL, nil, 16000, whisperInferenceOptions{},
	)
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

	text, language, err := whisperTranscribe(
		t.Context(), server.Client(), server.URL, nil, 16000, whisperInferenceOptions{},
	)
	if err != nil || text != "123" || language != "" {
		t.Fatalf("plain JSON literal reply = %q/%q, %v", text, language, err)
	}
}

func TestWhisperTranscribeAcceptsEmptyJSONTranscript(t *testing.T) {
	t.Parallel()

	server := newWhisperStub(t, "", `{"text":"","language":"it"}`)

	text, language, err := whisperTranscribe(
		t.Context(), server.Client(), server.URL, nil, 16000, whisperInferenceOptions{},
	)
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

	var got nativewire.Transcript
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
		lang: "it", rate: 16000, finalTimeout: finalTimeoutFor(defaultSTTTuning()),
	}
	if err := transcriber.partial(speechSegment{pcm: []int16{1, -1}, endSamples: 16000}); err != nil {
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

			_, _, err := whisperTranscribe(t.Context(), server.Client(), server.URL, nil, 16000, whisperInferenceOptions{})
			if err == nil || !strings.Contains(err.Error(), "JSON shape") {
				t.Fatalf("body %s error = %v, want JSON shape error", test.body, err)
			}
		})
	}
}

func TestWhisperTranscribeRejectsMalformedDeclaredJSON(t *testing.T) {
	t.Parallel()

	server := newWhisperStub(t, contentTypeJSON, `{`)

	_, _, err := whisperTranscribe(t.Context(), server.Client(), server.URL, nil, 16000, whisperInferenceOptions{})
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

	_, _, err := whisperTranscribe(t.Context(), server.Client(), server.URL, nil, 16000, whisperInferenceOptions{})
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

	_, _, err := whisperTranscribe(t.Context(), server.Client(), server.URL, nil, 16000, whisperInferenceOptions{})
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
	_, _, err := whisperTranscribe(t.Context(), client, redirect.URL, nil, 16000, whisperInferenceOptions{})
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

	if _, err := whisperRequest(t.Context(), "http://example.com:8080", nil, whisperInferenceOptions{}); err == nil {
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

// TestFinalInferenceFailureInterruptsHeldOpenInput: when a final decode fails
// terminally, the worker closes stdin so a held-open capture pipe unblocks
// the read loop instead of wedging the session.
func TestFinalInferenceFailureInterruptsHeldOpenInput(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "decoder down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	reader, writer := io.Pipe()
	t.Cleanup(func() {
		if err := writer.Close(); err != nil {
			t.Errorf("close pipe writer: %v", err)
		}
	})
	out := &syncLineBuffer{}
	tuning := callProfileSTTTuning()
	result := make(chan error, 1)
	go func() {
		result <- streamSTT(reader, sttStreamTestRate, tuning, newStreamTestTranscriber(server, out, tuning))
	}()

	writeSTTStreamChunk(t, writer, int16ToBytes(filledSamples(2*sttStreamTestRate/5, 20000)))
	writeSTTStreamChunk(t, writer, int16ToBytes(filledSamples(2*sttStreamTestRate/5, 0)))

	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "stt: inference") {
			t.Fatalf("streamSTT error = %v, want terminal final-inference failure", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("failed final inference did not interrupt the held-open input")
	}
	if lines := out.lines(); len(lines) != 0 {
		t.Fatalf("failed session emitted protocol lines: %q", lines)
	}
}

// sttStreamTestRate is the production capture rate used by the stream test.
const sttStreamTestRate = 16000

// callProfileSTTTuning mirrors lane.sttTuning for session.ProfileCall — the
// exact values production ships on conversational lanes.
func callProfileSTTTuning() nativewire.STTTuning {
	return nativewire.STTTuning{
		SilenceHang: 300 * time.Millisecond, MaxWindow: 5 * time.Second,
		MinSpeech: 250 * time.Millisecond,
	}
}

// newSampleCountingWhisperStub answers every inference instantly with a
// transcript derived from the posted WAV's sample count, so identical
// segments produce identical lines across independent runs.
func newSampleCountingWhisperStub(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}
		wav, readErr := io.ReadAll(file)
		if err := errors.Join(readErr, file.Close()); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}
		w.Header().Set("Server", whisperServerName)
		w.Header().Set("Content-Type", contentTypeJSON)
		if _, err := fmt.Fprintf(w, `{"text":"seg %d","language":"it"}`, (len(wav)-44)/2); err != nil {
			t.Errorf("write whisper stub response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	return server
}

// syncLineBuffer collects protocol lines written by inference goroutines while
// the test goroutine polls them.
type syncLineBuffer struct {
	buf bytes.Buffer
	mu  sync.Mutex
}

func (b *syncLineBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(data)
}

func (b *syncLineBuffer) lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	text := strings.TrimSuffix(b.buf.String(), "\n")
	if text == "" {
		return nil
	}

	return strings.Split(text, "\n")
}

type sttStreamLine struct {
	raw        string
	endSamples int64
	final      bool
}

func parseSTTStreamLines(t *testing.T, raw []string) []sttStreamLine {
	t.Helper()

	lines := make([]sttStreamLine, len(raw))
	for i, line := range raw {
		var msg nativewire.Transcript
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("transcript line %d %q: %v", i, line, err)
		}
		lines[i] = sttStreamLine{raw: line, endSamples: msg.EndSamples, final: msg.Final}
	}

	return lines
}

func countSTTStreamLines(t *testing.T, out *syncLineBuffer) (partials, finals int) {
	t.Helper()

	for _, line := range parseSTTStreamLines(t, out.lines()) {
		if line.final {
			finals++
		} else {
			partials++
		}
	}

	return partials, finals
}

// waitForSTTStreamLines paces the feed: the next chunk is withheld until the
// expected lines appeared. A timeout falls through so the closing assertions
// report exactly which lines never came.
func waitForSTTStreamLines(t *testing.T, out *syncLineBuffer, wantPartials, wantFinals int) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		partials, finals := countSTTStreamLines(t, out)
		if partials >= wantPartials && finals >= wantFinals {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func writeSTTStreamChunk(t *testing.T, writer io.Writer, chunk []byte) {
	t.Helper()

	if _, err := writer.Write(chunk); err != nil {
		t.Fatalf("write PCM chunk: %v", err)
	}
}

func newStreamTestTranscriber(
	server *httptest.Server, out io.Writer, tuning nativewire.STTTuning,
) *whisperSegmentTranscriber {
	return &whisperSegmentTranscriber{
		client: server.Client(), out: json.NewEncoder(out), base: server.URL,
		lang: "it", rate: sttStreamTestRate, fastDecode: true,
		finalTimeout: finalTimeoutFor(tuning),
	}
}

// TestStreamSTTEmitsPartialsBetweenCallEndpoints drives streamSTT end to end
// with the production call-profile tuning against an instant whisper stub.
// Every endpoint interval must yield at least two partials — below two,
// LocalAgreement-2 commits nothing early and the feature is a pure latency
// regression — every partial must stay ahead of the last final, and partials
// must not perturb the finals themselves.
func TestStreamSTTEmitsPartialsBetweenCallEndpoints(t *testing.T) {
	t.Parallel()

	server := newSampleCountingWhisperStub(t)
	out := &syncLineBuffer{}
	tuning := callProfileSTTTuning()
	transcriber := newStreamTestTranscriber(server, out, tuning)
	reader, writer := io.Pipe()
	t.Cleanup(func() {
		if err := writer.Close(); err != nil {
			t.Errorf("close pipe writer: %v", err)
		}
	})
	result := make(chan error, 1)
	go func() {
		result <- streamSTT(reader, sttStreamTestRate, tuning, transcriber)
	}()

	voiced := int16ToBytes(filledSamples(2*sttStreamTestRate/5, 20000))
	silence := int16ToBytes(filledSamples(2*sttStreamTestRate/5, 0))
	for turn := range 3 {
		base, _ := countSTTStreamLines(t, out)
		writeSTTStreamChunk(t, writer, voiced)
		waitForSTTStreamLines(t, out, base+1, turn)
		writeSTTStreamChunk(t, writer, voiced)
		waitForSTTStreamLines(t, out, base+2, turn)
		if turn < 2 {
			writeSTTStreamChunk(t, writer, silence)
			waitForSTTStreamLines(t, out, 0, turn+1)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close PCM stream: %v", err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("streamSTT: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("streamSTT did not finish after EOF")
	}

	lines := parseSTTStreamLines(t, out.lines())
	assertPartialsPerEndpointInterval(t, lines)
	assertPartialBoundariesAdvance(t, lines)
	assertFinalsMatchFinalsOnlyRun(t, server, lines, [][]byte{
		voiced, voiced, silence, voiced, voiced, silence, voiced, voiced,
	})
}

func assertPartialsPerEndpointInterval(t *testing.T, lines []sttStreamLine) {
	t.Helper()

	intervals := make([]int, 0, 3)
	partials := 0
	for _, line := range lines {
		if line.final {
			intervals = append(intervals, partials)
			partials = 0

			continue
		}
		partials++
	}
	if len(intervals) != 3 {
		t.Fatalf("finals = %d, want 3 endpoint intervals", len(intervals))
	}
	for i, count := range intervals {
		if count < 2 {
			t.Errorf("endpoint interval %d emitted %d partials, want at least 2 "+
				"(LocalAgreement-2 commits nothing on fewer)", i+1, count)
		}
	}
}

// assertPartialBoundariesAdvance pins the daemon's fail-fatal ordering rules:
// end_samples never move backward across lines and every partial's boundary
// strictly exceeds the last final's.
func assertPartialBoundariesAdvance(t *testing.T, lines []sttStreamLine) {
	t.Helper()

	var lastFinalEnd, lastEnd int64
	for i, line := range lines {
		if line.endSamples < lastEnd {
			t.Errorf("line %d end_samples %d moved backward from %d: %s",
				i, line.endSamples, lastEnd, line.raw)
		}
		lastEnd = line.endSamples
		if line.final {
			lastFinalEnd = line.endSamples

			continue
		}
		if line.endSamples <= lastFinalEnd {
			t.Errorf("partial line %d end_samples %d does not exceed the last final's %d: %s",
				i, line.endSamples, lastFinalEnd, line.raw)
		}
	}
}

// assertFinalsMatchFinalsOnlyRun replays the identical byte stream through the
// endpointer with finals-only transcription and requires byte-identical final
// lines, save for the wall-clock inference_ms measurement.
func assertFinalsMatchFinalsOnlyRun(
	t *testing.T, server *httptest.Server, lines []sttStreamLine, stream [][]byte,
) {
	t.Helper()

	reference := finalsOnlySTTRun(t, server, stream)
	for i, line := range reference {
		reference[i] = normalizeInferenceWallClock(t, line)
	}
	finals := make([]string, 0, len(reference))
	for _, line := range lines {
		if line.final {
			finals = append(finals, normalizeInferenceWallClock(t, line.raw))
		}
	}
	if !slices.Equal(finals, reference) {
		t.Errorf("finals with partials enabled =\n%s\nwant the finals-only run's\n%s",
			strings.Join(finals, "\n"), strings.Join(reference, "\n"))
	}
}

func finalsOnlySTTRun(t *testing.T, server *httptest.Server, stream [][]byte) []string {
	t.Helper()

	out := &syncLineBuffer{}
	transcriber := newStreamTestTranscriber(server, out, callProfileSTTTuning())
	endpointer := &energyEndpointer{rate: sttStreamTestRate, tuning: callProfileSTTTuning()}
	decoder := &s16StreamDecoder{}
	// One io.Pipe write is consumed in 8192-byte reads; the endpointer frames
	// per push, so the replay must deliver the identical read sequence.
	for _, chunk := range stream {
		for start := 0; start < len(chunk); start += 8192 {
			read := chunk[start:min(start+8192, len(chunk))]
			for _, segment := range endpointer.push(decoder.decode(read)) {
				if err := transcriber.transcribe(segment); err != nil {
					t.Fatalf("finals-only transcribe: %v", err)
				}
			}
		}
	}
	if segment := endpointer.flush(); len(segment.pcm) > 0 {
		if err := transcriber.transcribe(segment); err != nil {
			t.Fatalf("finals-only flush transcribe: %v", err)
		}
	}

	return out.lines()
}

func normalizeInferenceWallClock(t *testing.T, line string) string {
	t.Helper()

	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &fields); err != nil {
		t.Fatalf("transcript line %q: %v", line, err)
	}
	if _, ok := fields["inference_ms"]; !ok {
		t.Fatalf("transcript line %q has no inference_ms", line)
	}
	fields["inference_ms"] = json.RawMessage(`"wall-clock"`)
	normalized, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("normalize %q: %v", line, err)
	}

	return string(normalized)
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
	partial, err := json.Marshal(nativewire.Transcript{Partial: &partialText, Language: "it", EndSamples: 320})
	if err != nil {
		t.Fatalf("marshal partial: %v", err)
	}
	if string(partial) != `{"partial":"ciao a","language":"it","end_samples":320}` {
		t.Fatalf("partial wire = %s", partial)
	}

	finalText := "ciao a tutti"
	final, err := json.Marshal(nativewire.Transcript{
		Text: &finalText, Language: "it", Final: true, EndSamples: 640,
	})
	if err != nil {
		t.Fatalf("marshal final: %v", err)
	}
	if string(final) != `{"text":"ciao a tutti","language":"it","end_samples":640,"final":true}` {
		t.Fatalf("final wire = %s", final)
	}
}
