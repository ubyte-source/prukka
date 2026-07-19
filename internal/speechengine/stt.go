package speechengine

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ubyte-source/prukka/internal/nativewire"
)

const (
	sttSilenceHang     = 300 * time.Millisecond
	sttMaxWindow       = 5 * time.Second
	sttMinSpeech       = 250 * time.Millisecond
	sttVoicedRMS       = 0.012
	sttPreRoll         = 100 * time.Millisecond
	sttHTTPTimeout     = 2 * time.Minute
	maxSTTThreads      = 64
	sttFinalMinTimeout = 30 * time.Second
)

// RunSTT serves the streaming speech-to-text stdio protocol over stdin/stdout,
// resolving whisper-server and models from the engine bundle.
func RunSTT(args []string) (retErr error) {
	opts, err := parseSTTOptions(args)
	if err != nil {
		return err
	}

	dir := engineDir()
	server, base, err := startReadyWhisperServer(
		dir, bundlePath(dir, opts.model), opts.language, opts.threads, opts.fastDecode,
	)
	if err != nil {
		return fmt.Errorf("stt: whisper-server not ready: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, server.stop()) }()

	client, transport := newWhisperHTTPClient(sttHTTPTimeout)
	defer transport.CloseIdleConnections()
	transcriber := &whisperSegmentTranscriber{
		client:       client,
		out:          json.NewEncoder(os.Stdout),
		base:         base,
		lang:         opts.language,
		rate:         opts.rate,
		fastDecode:   opts.fastDecode,
		finalTimeout: finalTimeoutFor(opts.tuning),
	}
	if err := writeSTTReady(os.Stdout); err != nil {
		return fmt.Errorf("stt: write ready handshake: %w", err)
	}

	return streamSTT(os.Stdin, opts.rate, opts.tuning, transcriber)
}

func finalTimeoutFor(tuning nativewire.STTTuning) time.Duration {
	return max(sttFinalMinTimeout, 3*tuning.MaxWindow)
}

func writeSTTReady(output io.Writer) error {
	return json.NewEncoder(output).Encode(nativewire.Ready{Ready: true})
}

func defaultSTTTuning() nativewire.STTTuning {
	return nativewire.STTTuning{
		SilenceHang: sttSilenceHang, MaxWindow: sttMaxWindow, MinSpeech: sttMinSpeech,
	}
}

type sttOptions struct {
	model      string
	language   string
	rate       int
	threads    int
	protocol   int
	tuning     nativewire.STTTuning
	fastDecode bool
}

func parseSTTOptions(args []string) (sttOptions, error) {
	opts := sttOptions{language: languageAuto, rate: 16000, threads: 1, tuning: defaultSTTTuning()}
	flags := flag.NewFlagSet("stt", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.model, "model", "", "STT model path")
	flags.IntVar(&opts.protocol, "protocol-version", 0, "daemon/helper protocol version")
	flags.StringVar(&opts.language, "language", opts.language, "source language or auto")
	flags.IntVar(&opts.rate, "rate", opts.rate, "PCM sample rate")
	flags.IntVar(&opts.threads, "threads", opts.threads, "Whisper computation threads")
	flags.DurationVar(&opts.tuning.SilenceHang, "silence-hang", opts.tuning.SilenceHang,
		"trailing silence before an endpoint")
	flags.DurationVar(&opts.tuning.MaxWindow, "max-window", opts.tuning.MaxWindow,
		"maximum live transcription window")
	flags.DurationVar(&opts.tuning.MinSpeech, "min-speech", opts.tuning.MinSpeech,
		"minimum voiced audio before a silence endpoint")
	flags.BoolVar(&opts.fastDecode, "fast-decode", false,
		"use bounded-context conversational decoding")
	if err := flags.Parse(args); err != nil {
		return sttOptions{}, fmt.Errorf("stt: parse flags: %w", err)
	}
	if flags.NArg() != 0 {
		return sttOptions{}, fmt.Errorf("stt: unexpected argument %q", flags.Arg(0))
	}
	if opts.model == "" {
		return sttOptions{}, errors.New("stt: --model is required")
	}
	if opts.protocol != nativewire.ProtocolVersion {
		return sttOptions{}, fmt.Errorf(
			"stt: --protocol-version must be %d, got %d; rebuild the engine bundle",
			nativewire.ProtocolVersion, opts.protocol,
		)
	}
	if !validSampleRate(opts.rate) {
		return sttOptions{}, fmt.Errorf(
			"stt: --rate must be between %d and %d, got %d",
			minSampleRate, maxSampleRate, opts.rate,
		)
	}
	if opts.threads < 1 || opts.threads > maxSTTThreads {
		return sttOptions{}, fmt.Errorf(
			"stt: --threads must be between 1 and %d, got %d", maxSTTThreads, opts.threads,
		)
	}
	if err := opts.tuning.Validate(); err != nil {
		return sttOptions{}, fmt.Errorf("stt: %w", err)
	}
	if !validLanguageArg(opts.language, true) {
		return sttOptions{}, fmt.Errorf("stt: invalid --language %q", opts.language)
	}
	// whisper's -l rejects region-qualified tags (it-CH), so pin it to the base
	// subtag; the transcript language stays base too, which is what MT pairs on.
	opts.language = baseLanguageTag(strings.ToLower(opts.language))

	return opts, nil
}

type whisperSegmentTranscriber struct {
	client *http.Client
	out    *json.Encoder
	base   string
	lang   string
	rate   int
	// finalTimeout must be positive; finalTimeoutFor floors it at
	// sttFinalMinTimeout.
	finalTimeout time.Duration
	fastDecode   bool
}

// decode runs one whisper inference over the segment and sanitizes the text.
func (t *whisperSegmentTranscriber) decode(
	segment speechSegment,
) (text, language string, tookMS float64, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), t.finalTimeout)
	defer cancel()

	started := time.Now()
	text, language, err = whisperTranscribe(
		ctx, t.client, t.base, segment.pcm, t.rate, whisperInferenceOptions{fastDecode: t.fastDecode},
	)
	tookMS = milliseconds(time.Since(started))
	if err != nil {
		return "", language, tookMS, err
	}
	text = strings.TrimSpace(text)
	// whisper marks near-silent windows with a bracketed non-speech token
	// ("[BLANK_AUDIO]", "(music)"); those must not enter the stream as speech.
	if isNonSpeechPlaceholder(text) {
		text = ""
	}

	return text, language, tookMS, nil
}

func (t *whisperSegmentTranscriber) transcribe(segment speechSegment) error {
	text, detected, took, err := t.decode(segment)
	if err != nil {
		if !errors.Is(err, errUnsafeWhisperTranscript) {
			return fmt.Errorf("stt: inference: %w", err)
		}

		// Preserve the endpoint and reset the downstream commit epoch without
		// allowing unsafe text to fan out into MT/TTS. The next segment can be
		// decoded by the same healthy helper instead of losing the whole lane.
		fmt.Fprintf(os.Stderr, "stt: discarded unsafe final transcript: %v\n", err)
		text = ""
	}

	return t.out.Encode(nativewire.Transcript{
		Text: &text, Language: firstNonAuto(t.lang, detected), Final: true,
		InferenceMS: &took, EndSamples: segment.endSamples,
	})
}

// partial transcribes one live-window snapshot as a non-final transcript, so
// the daemon's committer streams stable clauses mid-utterance instead of
// waiting for the endpoint.
func (t *whisperSegmentTranscriber) partial(segment speechSegment) error {
	text, detected, took, err := t.decode(segment)
	if err != nil {
		if errors.Is(err, errUnsafeWhisperTranscript) {
			// A partial owns no endpoint. Dropping it leaves the last safe revision
			// intact until a final or a later safe partial supersedes it.
			fmt.Fprintf(os.Stderr, "stt: discarded unsafe partial transcript: %v\n", err)

			return nil
		}

		return fmt.Errorf("stt: partial inference: %w", err)
	}

	return t.out.Encode(nativewire.Transcript{
		Partial: &text, Language: firstNonAuto(t.lang, detected),
		InferenceMS: &took, EndSamples: segment.endSamples,
	})
}

func milliseconds(duration time.Duration) float64 {
	return float64(duration) / float64(time.Millisecond)
}

// isNonSpeechPlaceholder reports whether text is entirely one of whisper's
// bracketed non-speech markers, rather than transcribed speech.
func isNonSpeechPlaceholder(text string) bool {
	if len(text) < 2 {
		return false
	}

	first, last := text[0], text[len(text)-1]

	return (first == '[' && last == ']') || (first == '(' && last == ')')
}

// streamSTT pairs endpointed finals with live-window partials over one
// whisper session: a single worker decodes both, finals first, so stdout
// lines and their end_samples boundaries stay in source order while the read
// loop keeps capture draining.
func streamSTT(
	input io.Reader, rate int, tuning nativewire.STTTuning, transcriber *whisperSegmentTranscriber,
) error {
	endpointer := &energyEndpointer{rate: rate, tuning: tuning}
	decoder := &s16StreamDecoder{}
	buf := make([]byte, 8192)
	var interrupt func()
	if closer, ok := input.(io.Closer); ok {
		interrupt = func() {
			if closeErr := closer.Close(); closeErr != nil {
				fmt.Fprintf(os.Stderr, "stt: interrupt input: %v\n", closeErr)
			}
		}
	}
	worker := newSTTInferenceWorker(transcriber, interrupt)

	for {
		n, readErr := input.Read(buf)
		ready := endpointer.push(decoder.decode(buf[:n]))
		if err := transcribeSegments(ready, worker.submit); err != nil {
			return errors.Join(err, worker.close())
		}
		if live, sawSpeech := endpointer.live(); sawSpeech {
			worker.publishLive(live, endpointer.totalSamples)
		}
		if readErr != nil {
			finishErr := finishSTT(readErr, decoder, endpointer, worker.submit)

			return errors.Join(finishErr, worker.close())
		}
	}
}

// sttInferenceWorker owns the one whisper decode loop and is the only stdout
// writer after the ready handshake. Endpointed finals queue in order and are
// never dropped; the live window occupies a single newest-wins slot consumed
// only when no final waits, and a snapshot is decoded only while its boundary
// still exceeds the last final's, keeping emitted end_samples monotone.
// Nothing here ever cancels a decode already sent to whisper-server:
// disconnecting whisper.cpp mid-inference poisons its next decode, so a
// superseded snapshot is dropped before dispatch, never aborted in flight.
type sttInferenceWorker struct {
	transcriber *whisperSegmentTranscriber
	interrupt   func()
	cond        *sync.Cond
	done        chan struct{}
	err         error
	finals      []speechSegment
	live        speechSegment
	mu          sync.Mutex
	hasLive     bool
	closed      bool
}

func newSTTInferenceWorker(transcriber *whisperSegmentTranscriber, interrupt func()) *sttInferenceWorker {
	w := &sttInferenceWorker{transcriber: transcriber, interrupt: interrupt, done: make(chan struct{})}
	w.cond = sync.NewCond(&w.mu)
	go w.serve()

	return w
}

func (w *sttInferenceWorker) serve() {
	defer close(w.done)

	var lastFinalEnd int64
	for {
		segment, isFinal, ok := w.take()
		switch {
		case !ok:
			return
		case isFinal:
			if err := w.transcriber.transcribe(segment); err != nil {
				w.fail(err)

				return
			}
			lastFinalEnd = segment.endSamples
		case segment.endSamples > lastFinalEnd:
			if err := w.transcriber.partial(segment); err != nil {
				// A failed partial only delays the next revision; the endpointed
				// final still covers this audio, so degrade instead of dying.
				fmt.Fprintf(os.Stderr, "stt: %v\n", err)
			}
		}
	}
}

// take blocks until work arrives, preferring queued finals over the live
// slot; ok turns false once the worker is closed and every final drained.
func (w *sttInferenceWorker) take() (segment speechSegment, isFinal, ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	for len(w.finals) == 0 && !w.hasLive && !w.closed {
		w.cond.Wait()
	}
	if len(w.finals) != 0 {
		segment, w.finals = w.finals[0], w.finals[1:]

		return segment, true, true
	}
	if w.hasLive && !w.closed {
		segment, w.live, w.hasLive = w.live, speechSegment{}, false

		return segment, false, true
	}

	return speechSegment{}, false, false
}

// fail records the terminal final-inference error and unblocks the read loop
// by closing its input, so a held-open capture pipe cannot wedge the session.
func (w *sttInferenceWorker) fail(err error) {
	w.err = err
	if w.interrupt != nil {
		w.interrupt()
	}
}

// submit queues one endpointed segment; it reports the worker's terminal
// error instead of accepting audio a dead worker would never decode.
func (w *sttInferenceWorker) submit(segment speechSegment) error {
	select {
	case <-w.done:
		return w.err
	default:
	}
	w.mu.Lock()
	w.finals = append(w.finals, segment)
	w.mu.Unlock()
	w.cond.Signal()
	select {
	case <-w.done:
		return w.err
	default:
		return nil
	}
}

// publishLive replaces the live slot with the newest window snapshot; an
// unconsumed older snapshot is overwritten, worthless by construction.
func (w *sttInferenceWorker) publishLive(buf []int16, endSamples int64) {
	snapshot := speechSegment{pcm: append([]int16(nil), buf...), endSamples: endSamples}
	w.mu.Lock()
	w.live = snapshot
	w.hasLive = true
	w.mu.Unlock()
	w.cond.Signal()
}

// close decodes every queued final, drops any pending live snapshot, waits
// for the worker to exit and returns its terminal error.
func (w *sttInferenceWorker) close() error {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	w.cond.Broadcast()
	<-w.done

	return w.err
}

func transcribeSegments(segments []speechSegment, transcribe func(speechSegment) error) error {
	for _, segment := range segments {
		if err := transcribe(segment); err != nil {
			return err
		}
	}

	return nil
}

func finishSTT(
	readErr error, decoder *s16StreamDecoder, endpointer *energyEndpointer,
	transcribe func(speechSegment) error,
) error {
	if decoder.pending {
		return errors.New("stt: truncated 16-bit PCM sample")
	}
	if segment := endpointer.flush(); len(segment.pcm) > 0 {
		if err := transcribe(segment); err != nil {
			return err
		}
	}
	if errors.Is(readErr, io.EOF) {
		return nil
	}

	return fmt.Errorf("stt: read: %w", readErr)
}

// s16StreamDecoder preserves a byte split across Read calls instead of
// dropping half of a PCM sample.
type s16StreamDecoder struct {
	scratch  []int16
	trailing byte
	pending  bool
}

// decode returns a view that remains valid until the next decode call. The STT
// loop passes it directly to energyEndpointer.push, which copies every sample.
//
//nolint:gosec // The conversion preserves the exact signed 16-bit PCM bit pattern.
func (d *s16StreamDecoder) decode(data []byte) []int16 {
	sampleCount := (len(data) + btoi(d.pending)) / 2
	if cap(d.scratch) < sampleCount {
		d.scratch = make([]int16, sampleCount)
	} else {
		d.scratch = d.scratch[:sampleCount]
	}
	samples := d.scratch
	index := 0
	if d.pending && len(data) > 0 {
		samples[0] = int16(binary.LittleEndian.Uint16([]byte{d.trailing, data[0]}))
		data = data[1:]
		index++
		d.pending = false
	}

	for len(data) >= 2 {
		samples[index] = int16(binary.LittleEndian.Uint16(data[:2]))
		data = data[2:]
		index++
	}
	if len(data) == 1 {
		d.trailing = data[0]
		d.pending = true
	}

	return samples[:index]
}

func btoi(value bool) int {
	if value {
		return 1
	}

	return 0
}

// firstNonAuto picks the first concrete language tag. Callers put a pinned
// hint first; auto-detection fills only an auto hint.
func firstNonAuto(tags ...string) string {
	for _, tag := range tags {
		if tag != "" && tag != languageAuto {
			return tag
		}
	}

	return ""
}

// speechSegment couples one immutable inference snapshot with the exclusive
// cumulative source-sample boundary it covers. Inference may finish much later;
// the boundary remains the event's source-clock truth across that delay.
type speechSegment struct {
	pcm        []int16
	endSamples int64
}

// energyEndpointer accumulates PCM and cuts a segment after trailing silence
// or at the window ceiling, preserving each cut's cumulative sample boundary.
// rate and tuning must hold parseSTTOptions-validated (positive) values.
type energyEndpointer struct {
	buf          []int16
	rate         int
	tuning       nativewire.STTTuning
	totalSamples int64
	voicedRun    time.Duration
	silenceRun   time.Duration
	sawSpeech    bool
}

func (e *energyEndpointer) push(samples []int16) []speechSegment {
	var out []speechSegment
	frame := e.rate / 50 // 20 ms endpointing frames

	for len(samples) > 0 {
		take := min(frame, len(samples))
		chunk := samples[:take]
		samples = samples[take:]
		e.appendChunk(chunk)
		if segment := e.cutReady(); segment.pcm != nil {
			out = append(out, segment)
		}
	}

	return out
}

func (e *energyEndpointer) appendChunk(chunk []int16) {
	e.buf = append(e.buf, chunk...)
	e.totalSamples += int64(len(chunk))
	duration := time.Duration(len(chunk)) * time.Second / time.Duration(e.rate)
	if rms(chunk) >= sttVoicedRMS {
		e.sawSpeech = true
		e.voicedRun += duration
		e.silenceRun = 0
	} else {
		e.silenceRun += duration
	}
	if !e.sawSpeech {
		e.trimPreRoll()
	}
}

func (e *energyEndpointer) cutReady() speechSegment {
	bufferDuration := time.Duration(len(e.buf)) * time.Second / time.Duration(e.rate)
	enoughSpeech := e.sawSpeech && e.voicedRun >= e.tuning.MinSpeech
	endpoint := enoughSpeech && e.silenceRun >= e.tuning.SilenceHang
	if !endpoint && bufferDuration < e.tuning.MaxWindow {
		return speechSegment{}
	}
	if !enoughSpeech {
		e.reset()

		return speechSegment{}
	}

	return e.take()
}

func (e *energyEndpointer) trimPreRoll() {
	keep := int(sttPreRoll * time.Duration(e.rate) / time.Second)
	if len(e.buf) <= keep {
		return
	}

	copy(e.buf, e.buf[len(e.buf)-keep:])
	e.buf = e.buf[:keep]
}

// live exposes the accumulating buffer and whether it holds speech, so the
// read loop can hand the newest window snapshot to the inference worker.
func (e *energyEndpointer) live() ([]int16, bool) {
	return e.buf, e.sawSpeech
}

func (e *energyEndpointer) flush() speechSegment {
	if !e.sawSpeech || e.voicedRun < e.tuning.MinSpeech {
		e.reset()

		return speechSegment{}
	}

	return e.take()
}

func (e *energyEndpointer) take() speechSegment {
	seg := speechSegment{
		pcm: append([]int16(nil), e.buf...), endSamples: e.totalSamples,
	}
	e.reset()

	return seg
}

func (e *energyEndpointer) reset() {
	e.buf = e.buf[:0]
	e.voicedRun, e.silenceRun, e.sawSpeech = 0, 0, false
}

func rms(samples []int16) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		v := float64(s) / 32768
		sum += v * v
	}

	return math.Sqrt(sum / float64(len(samples)))
}
