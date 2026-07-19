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
	"sync/atomic"
	"time"

	"github.com/ubyte-source/prukka/internal/nativewire"
)

const (
	sttSilenceHang = 300 * time.Millisecond
	sttMaxWindow   = 5 * time.Second
	sttMinSpeech   = 250 * time.Millisecond
	sttVoicedRMS   = 0.012
	sttPreRoll     = 100 * time.Millisecond
	sttHTTPTimeout = 2 * time.Minute
	maxSTTThreads  = 64
	// sttPartialStride is how much new audio must accrue before the live
	// buffer earns a background partial transcription. A busy CPU stretches
	// the effective pace instead of queueing (one inference in flight).
	sttPartialStride   = 700 * time.Millisecond
	sttTuningMin       = 20 * time.Millisecond
	sttTuningMax       = 30 * time.Second
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

	return streamSTT(os.Stdin, opts.rate, opts.tuning, transcriber, &partialPacer{
		run: transcriber.partial, rate: opts.rate, stride: opts.tuning.partialStride,
		partialTimeout: finalTimeoutFor(opts.tuning),
	})
}

func finalTimeoutFor(tuning sttTuning) time.Duration {
	return max(sttFinalMinTimeout, 3*tuning.maxWindow)
}

func writeSTTReady(output io.Writer) error {
	return json.NewEncoder(output).Encode(nativewire.Ready{Ready: true})
}

type sttTuning struct {
	silenceHang   time.Duration
	maxWindow     time.Duration
	minSpeech     time.Duration
	partialStride time.Duration
}

func defaultSTTTuning() sttTuning {
	return sttTuning{
		silenceHang: sttSilenceHang, maxWindow: sttMaxWindow,
		minSpeech: sttMinSpeech, partialStride: sttPartialStride,
	}
}

type sttOptions struct {
	model      string
	language   string
	rate       int
	threads    int
	protocol   int
	tuning     sttTuning
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
	flags.DurationVar(&opts.tuning.silenceHang, "silence-hang", opts.tuning.silenceHang,
		"trailing silence before an endpoint")
	flags.DurationVar(&opts.tuning.maxWindow, "max-window", opts.tuning.maxWindow,
		"maximum live transcription window")
	flags.DurationVar(&opts.tuning.minSpeech, "min-speech", opts.tuning.minSpeech,
		"minimum voiced audio before a silence endpoint")
	flags.DurationVar(&opts.tuning.partialStride, "partial-stride", opts.tuning.partialStride,
		"new live audio between partial inferences")
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
	if err := validateSTTTuning(opts.tuning); err != nil {
		return sttOptions{}, err
	}
	if !validLanguageArg(opts.language, true) {
		return sttOptions{}, fmt.Errorf("stt: invalid --language %q", opts.language)
	}
	// whisper's -l rejects region-qualified tags (it-CH), so pin it to the base
	// subtag; the transcript language stays base too, which is what MT pairs on.
	opts.language = baseLanguageTag(strings.ToLower(opts.language))

	return opts, nil
}

func validateSTTTuning(tuning sttTuning) error {
	values := [...]struct {
		name  string
		value time.Duration
	}{
		{name: "silence-hang", value: tuning.silenceHang},
		{name: "max-window", value: tuning.maxWindow},
		{name: "min-speech", value: tuning.minSpeech},
		{name: "partial-stride", value: tuning.partialStride},
	}
	for _, item := range values {
		if item.value < sttTuningMin || item.value > sttTuningMax {
			return fmt.Errorf(
				"stt: --%s must be between %s and %s, got %s",
				item.name, sttTuningMin, sttTuningMax, item.value,
			)
		}
	}
	if tuning.partialStride > tuning.maxWindow {
		return errors.New("stt: --partial-stride must not exceed --max-window")
	}
	if tuning.minSpeech > tuning.maxWindow {
		return errors.New("stt: --min-speech must not exceed --max-window")
	}

	return nil
}

type whisperSegmentTranscriber struct {
	client *http.Client
	out    *json.Encoder
	base   string
	lang   string
	// mu serializes transcript lines: background partials race endpointed
	// finals for stdout, and a gated emit under the same lock guarantees a
	// stale partial can never follow the final that supersedes it.
	mu      sync.Mutex
	inferMu sync.Mutex
	rate    int
	// finalTimeout must be positive; finalTimeoutFor floors it at
	// sttFinalMinTimeout.
	finalTimeout time.Duration
	fastDecode   bool
}

func (t *whisperSegmentTranscriber) transcribe(segment speechSegment) error {
	t.inferMu.Lock()
	defer t.inferMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), t.finalTimeout)
	defer cancel()

	started := time.Now()
	text, detected, err := whisperTranscribe(
		ctx, t.client, t.base, segment.pcm, t.rate, whisperInferenceOptions{fastDecode: t.fastDecode},
	)
	inference := time.Since(started)
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
	text = strings.TrimSpace(text)
	// whisper marks near-silent windows with a bracketed non-speech token
	// ("[BLANK_AUDIO]", "(music)"); those must not enter the stream as speech.
	if isNonSpeechPlaceholder(text) {
		text = ""
	}

	return t.emit(nil, transcript{
		Text: &text, Language: firstNonAuto(t.lang, detected), Final: true,
		InferenceMS: milliseconds(inference), EndSamples: segment.endSamples,
	})
}

// partial transcribes a snapshot of the live buffer and emits it as a
// non-final transcript, so the daemon's committer streams stable clauses
// mid-utterance instead of waiting for the endpoint. The gate is checked
// under the emit lock: once the segment is cut, the in-flight partial drops.
func (t *whisperSegmentTranscriber) partial(
	ctx context.Context, segment speechSegment, live func() bool,
) error {
	t.inferMu.Lock()
	defer t.inferMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	started := time.Now()
	text, detected, err := whisperTranscribe(
		ctx, t.client, t.base, segment.pcm, t.rate, whisperInferenceOptions{fastDecode: t.fastDecode},
	)
	inference := time.Since(started)
	if err != nil {
		if errors.Is(err, errUnsafeWhisperTranscript) {
			// A partial owns no endpoint. Dropping it leaves the last safe revision
			// intact until a final or a later safe partial supersedes it.
			fmt.Fprintf(os.Stderr, "stt: discarded unsafe partial transcript: %v\n", err)

			return nil
		}

		return fmt.Errorf("stt: partial inference: %w", err)
	}
	text = strings.TrimSpace(text)
	if isNonSpeechPlaceholder(text) {
		text = ""
	}

	return t.emit(live, transcript{
		Partial: &text, Language: firstNonAuto(t.lang, detected),
		InferenceMS: milliseconds(inference), EndSamples: segment.endSamples,
	})
}

func milliseconds(duration time.Duration) float64 {
	return float64(duration) / float64(time.Millisecond)
}

// emit writes one transcript line unless the gate reports it stale.
func (t *whisperSegmentTranscriber) emit(gate func() bool, entry transcript) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if gate != nil && !gate() {
		return nil
	}

	return t.out.Encode(entry)
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

// partialPacer decides when the live buffer earns a background partial: one
// inference in flight at a time (a slow CPU stretches the pace instead of
// queueing) and only after one stride of new audio accrued. cut renders
// any in-flight partial stale before the segment's final is written, but lets
// its native request finish: disconnecting whisper.cpp mid-inference can leave
// the reused server unable to decode the immediately following final.
type partialPacer struct {
	run      func(context.Context, speechSegment, func() bool) error
	blocked  func() bool
	cancel   context.CancelFunc
	inflight sync.WaitGroup
	cancelMu sync.Mutex
	rate     int
	// stride must be a positive, parseSTTOptions-validated duration.
	stride time.Duration
	// partialTimeout bounds one partial inference so a wedged decode releases
	// inferMu on the same budget the final path uses, never ~2 minutes.
	partialTimeout time.Duration
	dispatched     int
	epoch          atomic.Uint64
	busy           atomic.Bool
}

func (p *partialPacer) observe(buf []int16, sawSpeech bool, endSamples int64) {
	if p.run == nil || !sawSpeech || p.inferenceBlocked() {
		return
	}
	stride := int(p.stride * time.Duration(p.rate) / time.Second)
	if len(buf)-p.dispatched < stride {
		return
	}
	if !p.busy.CompareAndSwap(false, true) {
		return
	}
	if p.inferenceBlocked() {
		p.busy.Store(false)

		return
	}

	p.dispatched = len(buf)
	snapshot := speechSegment{pcm: append([]int16(nil), buf...), endSamples: endSamples}
	epoch := p.epoch.Load()
	ctx, cancel := context.WithTimeout(context.Background(), p.partialTimeout)
	p.setCancel(cancel)
	p.inflight.Add(1)

	go p.infer(ctx, cancel, snapshot, epoch)
}

func (p *partialPacer) infer(
	ctx context.Context, cancel context.CancelFunc, snapshot speechSegment, epoch uint64,
) {
	defer p.inflight.Done()
	defer cancel()
	defer func() {
		p.setCancel(nil)
		p.busy.Store(false)
	}()

	live := func() bool { return p.epoch.Load() == epoch }
	if err := p.run(ctx, snapshot, live); err != nil && !errors.Is(err, context.Canceled) {
		// A failed partial only delays the next stable clause; the endpointed
		// final still covers this audio, so degrade instead of dying.
		fmt.Fprintf(os.Stderr, "stt: %v\n", err)
	}
}

func (p *partialPacer) inferenceBlocked() bool {
	return p.blocked != nil && p.blocked()
}

func (p *partialPacer) setCancel(cancel context.CancelFunc) {
	p.cancelMu.Lock()
	p.cancel = cancel
	p.cancelMu.Unlock()
}

func (p *partialPacer) cancelForShutdown() {
	p.cancelMu.Lock()
	cancel := p.cancel
	p.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// cut invalidates in-flight partials and restarts the stride accounting; the
// caller runs on the read loop, the only goroutine touching dispatched. It
// deliberately does not cancel the HTTP request. busy and the final-pending
// barrier provide backpressure while inferMu makes the final wait until the
// stale request has left whisper.cpp cleanly.
func (p *partialPacer) cut() {
	p.epoch.Add(1)
	p.dispatched = 0
}

// drain waits for the in-flight partial so process exit cannot truncate a
// transcript line mid-write.
func (p *partialPacer) drain() {
	p.inflight.Wait()
}

// shutdown may abort the last partial because the whisper server is torn down
// immediately afterwards and will never be reused for a final inference.
func (p *partialPacer) shutdown() {
	p.epoch.Add(1)
	p.cancelForShutdown()
	p.drain()
}

// streamSTT pairs endpointed finals with paced background partials over one
// whisper session: segments cut the pacer's epoch before their final emits,
// so no stale partial can outlive the text that supersedes it.
func streamSTT(
	input io.Reader, rate int, tuning sttTuning,
	transcriber *whisperSegmentTranscriber, partials *partialPacer,
) error {
	endpointer := &energyEndpointer{rate: rate, tuning: tuning}
	endpointGeneration := endpointer.generation
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
	segments := newInterruptingSegmentWorker(transcriber.transcribe, interrupt)
	partials.blocked = segments.hasPending
	submitSegment := func(segment speechSegment) error {
		// Reset cadence and invalidate stale partial work at the audio boundary,
		// not later when a queued final reaches the inference worker.
		partials.cut()

		return segments.submit(segment)
	}
	defer func() {
		partials.shutdown()
	}()

	for {
		n, readErr := input.Read(buf)
		ready := endpointer.push(decoder.decode(buf[:n]))
		syncPartialBoundary(endpointer, partials, &endpointGeneration)
		if err := transcribeSegments(ready, submitSegment); err != nil {
			return errors.Join(err, segments.close())
		}
		live, sawSpeech := endpointer.live()
		partials.observe(live, sawSpeech, endpointer.totalSamples)
		if readErr != nil {
			finishErr := finishSTT(readErr, decoder, endpointer, submitSegment)

			return errors.Join(finishErr, segments.close())
		}
	}
}

// syncPartialBoundary invalidates partial inference whenever the endpointer
// resets, including a discarded max-window that never becomes a final. Without
// this signal, a click-triggered partial can leak into the next speaker turn.
func syncPartialBoundary(
	endpointer *energyEndpointer, partials *partialPacer, generation *uint64,
) {
	if endpointer.generation == *generation {
		return
	}

	partials.cut()
	*generation = endpointer.generation
}

// segmentWorker serializes final inference without blocking the stdin read
// loop. That keeps live capture draining while whisper handles an endpointed
// window, instead of accumulating delayed PCM in ffmpeg and OS pipes.
type segmentWorker struct {
	run       func(speechSegment) error
	interrupt func()
	in        chan speechSegment
	done      chan struct{}
	err       error
	pending   atomic.Int64
	closeOnce sync.Once
}

func newInterruptingSegmentWorker(run func(speechSegment) error, interrupt func()) *segmentWorker {
	w := &segmentWorker{
		run: run, in: make(chan speechSegment, 2), done: make(chan struct{}), interrupt: interrupt,
	}
	go w.serve()

	return w
}

func (w *segmentWorker) serve() {
	defer close(w.done)
	for segment := range w.in {
		err := w.run(segment)
		if err != nil {
			w.err = err
			if w.interrupt != nil {
				w.interrupt()
			}

			return
		}
		w.pending.Add(-1)
	}
}

func (w *segmentWorker) submit(segment speechSegment) error {
	select {
	case <-w.done:
		return w.err
	default:
	}

	w.pending.Add(1)
	select {
	case w.in <- segment:
		// If the worker failed while this buffered send was selectable, let
		// the terminal error win instead of reporting a segment as accepted
		// after its only consumer has exited.
		select {
		case <-w.done:
			return w.err
		default:
			return nil
		}
	case <-w.done:
		w.pending.Add(-1)

		return w.err
	}
}

// hasPending is the priority barrier between the final queue and background
// partials. It stays true until every accepted final has completed inference
// and written its protocol event, so a later turn can never overtake it.
func (w *segmentWorker) hasPending() bool { return w.pending.Load() > 0 }

func (w *segmentWorker) close() error {
	w.closeOnce.Do(func() { close(w.in) })
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

// transcript is one stdout line from the stt subcommand: a partial refines
// the live tail while the speaker is still talking, a final closes the
// endpointed segment. The daemon's committer turns successive partials into
// early stable clauses (local agreement), which is what keeps a live call's
// perceived latency at clause level instead of utterance level.
type transcript struct {
	Partial     *string `json:"partial,omitempty"`
	Text        *string `json:"text,omitempty"`
	Language    string  `json:"language,omitempty"`
	Final       bool    `json:"final,omitempty"`
	InferenceMS float64 `json:"inference_ms,omitempty"`
	EndSamples  int64   `json:"end_samples"`
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
	tuning       sttTuning
	totalSamples int64
	generation   uint64
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
	enoughSpeech := e.sawSpeech && e.voicedRun >= e.tuning.minSpeech
	endpoint := enoughSpeech && e.silenceRun >= e.tuning.silenceHang
	if !endpoint && bufferDuration < e.tuning.maxWindow {
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
// partial pacer can decide when the tail earns a background transcription.
func (e *energyEndpointer) live() ([]int16, bool) {
	return e.buf, e.sawSpeech
}

func (e *energyEndpointer) flush() speechSegment {
	if !e.sawSpeech || e.voicedRun < e.tuning.minSpeech {
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
	e.generation++
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
