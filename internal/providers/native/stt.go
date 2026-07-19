// Package native adapts an operator-configured external engine bundle over
// stdio. Each stage starts the engine's stt, mt or tts subcommand; inference
// remains outside the Go process.
package native

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/engine"
	"github.com/ubyte-source/prukka/internal/core/lang"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
	"github.com/ubyte-source/prukka/internal/nativewire"
)

// sttEventQueue buffers transcript updates so a brief consumer stall never
// blocks the stdout read pump.
const sttEventQueue = 8

// scanLineMax caps one JSON transcript line; STT lines are short sentences.
const scanLineMax = 1 << 20

// sttReadyTimeout bounds outer-helper startup after the process was spawned.
// The inner whisper-server has its own tighter model-load readiness deadline.
const sttReadyTimeout = 30 * time.Second

// subSTT is the transcription subcommand of the configured engine helper.
const subSTT = "stt"

// errSessionStopping ends the read pump when an update cannot be emitted
// because the session was asked to stop. It is a deliberate local condition,
// never context.Canceled: a genuine pipe failure joined with this sentinel
// must not masquerade as a cancellation any caller asked for.
var errSessionStopping = errors.New("native stt: session stopping")

// Helper invocation flags, named once.
const (
	flagModel       = "--model"
	flagRate        = "--rate"
	flagThreads     = "--threads"
	flagLanguage    = "--language"
	flagSilenceHang = "--silence-hang"
	flagMaxWindow   = "--max-window"
	flagMinSpeech   = "--min-speech"
	flagFastDecode  = "--fast-decode"
	flagProtocol    = "--protocol-version"
)

// STTConfig configures transcription through an external engine helper. Its
// stt subcommand reads 16-bit little-endian mono PCM from stdin and writes
// newline-delimited JSON transcripts to stdout. A zero Tuning leaves the
// helper's broadcast-safe defaults in force.
type STTConfig struct {
	Log        *slog.Logger
	Inference  func(kind string, duration time.Duration)
	Bin        string
	EngineRoot string
	Model      string
	Tuning     nativewire.STTTuning
	Rate       int
	Threads    int
	FastDecode bool
}

// STT implements engine.Transcriber over a spawned streaming STT helper.
type STT struct {
	log *slog.Logger
	cfg STTConfig
}

// NewSTT wires a transcriber from the resolved config.
func NewSTT(cfg *STTConfig) *STT {
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &STT{cfg: *cfg, log: log}
}

// Compile-time port check.
var _ engine.Transcriber = (*STT)(nil)

// Open spawns one helper for a transcription session. Canceling ctx closes its
// input and, after a grace period, stops the complete helper process tree.
func (s *STT) Open(ctx context.Context, hint core.Lang) (engine.Transcription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, s.cfg.Bin, s.args(hint)...)
	cmd.Env = engineChildEnv(s.cfg.EngineRoot)
	child, err := spawnHelper(ctx, cmd, "native stt", s.log)
	if err != nil {
		return nil, err
	}

	session := &sttSession{
		spawnedHelper: child,
		events:        make(chan engine.Transcript, sttEventQueue),
		ready:         make(chan struct{}), done: make(chan struct{}), stop: make(chan struct{}),
		log: s.log, lang: hint, rate: s.cfg.Rate, inference: s.cfg.Inference,
	}

	go session.watch(ctx)
	go session.read()
	if readyErr := session.waitReady(ctx, sttReadyTimeout); readyErr != nil {
		session.stopped.Store(true)

		return nil, errors.Join(
			fmt.Errorf(
				"native stt protocol v%d startup failed; rebuild an incompatible engine bundle: %w",
				nativewire.ProtocolVersion, readyErr,
			),
			session.Close(),
		)
	}

	return session, nil
}

// args builds the helper invocation; the language hint is passed only when the
// caller pinned one, leaving auto-detection to the model otherwise.
func (s *STT) args(hint core.Lang) []string {
	threads := max(1, s.cfg.Threads)
	args := []string{
		subSTT,
		flagProtocol, strconv.Itoa(nativewire.ProtocolVersion),
		flagModel, s.cfg.Model,
		flagRate, strconv.Itoa(s.cfg.Rate),
		flagThreads, strconv.Itoa(threads),
	}
	args = appendDurationArg(args, flagSilenceHang, s.cfg.Tuning.SilenceHang)
	args = appendDurationArg(args, flagMaxWindow, s.cfg.Tuning.MaxWindow)
	args = appendDurationArg(args, flagMinSpeech, s.cfg.Tuning.MinSpeech)
	if s.cfg.FastDecode {
		args = append(args, flagFastDecode)
	}
	if hint != core.LangAuto {
		args = append(args, flagLanguage, string(hint.Base()))
	}

	return args
}

func appendDurationArg(args []string, name string, value time.Duration) []string {
	if value <= 0 {
		return args
	}

	return append(args, name, value.String())
}

// sttSession is one live transcription: the read pump owns events, Push and
// CloseSend write to stdin, watch ends both on cancellation.
type sttSession struct {
	*spawnedHelper

	terminal   error
	cleanupErr error
	writeErr   error
	stop       chan struct{}
	events     chan engine.Transcript
	ready      chan struct{}
	done       chan struct{}
	log        *slog.Logger
	inference  func(kind string, duration time.Duration)
	lang       core.Lang
	raw        []byte
	timeline   sourceTimeline
	rate       int
	stdinOnce  sync.Once
	stopOnce   sync.Once
	errMu      sync.Mutex
	writeMu    sync.Mutex
	stopped    atomic.Bool
	readySeen  atomic.Bool
	sendClosed atomic.Bool
}

// Push implements engine.Transcription: it streams one audio chunk to stdin.
func (s *sttSession) Push(ctx context.Context, frame core.PCM) error {
	if frame.Rate != s.rate {
		return fmt.Errorf("native stt: PCM rate %d, want %d", frame.Rate, s.rate)
	}
	if frame.Ch != 1 {
		return fmt.Errorf("native stt: PCM channels %d, want 1", frame.Ch)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	s.raw = pipeline.AppendS16LE(s.raw[:0], frame.Data)
	s.timeline.record(frame)
	n, err := s.stdin.Write(s.raw)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}

		return s.writeFailure(ctx, err)
	}
	if n != len(s.raw) {
		return s.writeFailure(ctx, io.ErrShortWrite)
	}

	return nil
}

// writeFailure records the pipe cause before stopping the helper so reap keeps
// its terminal diagnostic even if another goroutine cancels the session. The
// wait is bounded by stopHelper's process-tree kill.
func (s *sttSession) writeFailure(ctx context.Context, writeErr error) error {
	failure := fmt.Errorf("native stt write PCM: %w", writeErr)
	s.errMu.Lock()
	s.writeErr = errors.Join(s.writeErr, failure)
	s.errMu.Unlock()

	s.stopHelper()
	<-s.done
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if terminalErr := s.Err(); terminalErr != nil {
		return terminalErr
	}

	return failure
}

// Events implements engine.Transcription.
func (s *sttSession) Events() <-chan engine.Transcript { return s.events }

// Err implements engine.Transcription. Call it after Events closes.
func (s *sttSession) Err() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()

	return s.terminal
}

// CloseSend implements engine.Transcription: closing stdin signals end of audio
// so the helper flushes its final transcripts and exits.
func (s *sttSession) CloseSend() error {
	s.sendClosed.Store(true)
	var err error
	s.stdinOnce.Do(func() { err = s.stdin.Close() })

	return err
}

// Close stops the helper and waits until its process tree has been reaped.
func (s *sttSession) Close() error {
	s.stopped.Store(true)
	s.stopHelper()
	<-s.done
	s.errMu.Lock()
	defer s.errMu.Unlock()

	return s.cleanupErr
}

// read pumps stdout lines into transcript events until the helper exits, then
// releases the channel and reaps the process exactly once.
func (s *sttSession) read() {
	defer close(s.done)
	defer close(s.events)

	var scanErr error
	defer func() { s.reap(scanErr) }()

	scanner := bufio.NewScanner(s.stdout)
	scanner.Buffer(make([]byte, 0, 64<<10), scanLineMax)

	for scanner.Scan() {
		if dispatchErr := s.dispatch(scanner.Bytes()); dispatchErr != nil {
			scanErr = dispatchErr

			return
		}
	}

	scanErr = scanner.Err()
}

// dispatch validates and folds one protocol line into an event. Protocol
// violations are terminal: silently skipping them can make a failed helper
// look like a successful transcription with no captions.
func (s *sttSession) dispatch(line []byte) error {
	msg, ready, err := decodeSTTMessage(line)
	if err != nil {
		return err
	}
	if ready {
		if !s.readySeen.CompareAndSwap(false, true) {
			return errors.New("native stt response: duplicate ready handshake")
		}
		close(s.ready)

		return nil
	}
	if !s.readySeen.Load() {
		return errors.New("native stt response: transcript before ready handshake")
	}
	if languageErr := s.applyDetectedLanguage(msg.Language); languageErr != nil {
		return languageErr
	}
	s.observeInference(&msg)
	sourceEnd, timingErr := s.timeline.resolve(msg.EndSamples)
	if timingErr != nil {
		return fmt.Errorf("native stt response timing: %w", timingErr)
	}

	update, ok := transcriptUpdate(&msg, s.lang, sourceEnd)
	if !ok || s.emit(update) {
		return nil
	}

	return errSessionStopping
}

func (s *sttSession) waitReady(ctx context.Context, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-s.ready:
		return nil
	case <-s.done:
		if s.readySeen.Load() {
			return nil
		}
		if err := s.Err(); err != nil {
			return fmt.Errorf("native stt readiness: %w", err)
		}

		return errors.New("native stt readiness: helper exited before ready")
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("native stt readiness timed out after %s", timeout)
	}
}

func (s *sttSession) observeInference(msg *nativewire.Transcript) {
	if msg.InferenceMS == nil || s.inference == nil {
		return
	}

	kind := "partial"
	if msg.Final {
		kind = "final"
	}
	s.inference(kind, time.Duration(*msg.InferenceMS*float64(time.Millisecond)))
}

// decodeSTTMessage validates one protocol line and reports whether it is the
// ready handshake rather than a transcript.
func decodeSTTMessage(line []byte) (nativewire.Transcript, bool, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return nativewire.Transcript{}, false, fmt.Errorf("native stt response JSON: %w", err)
	}
	if raw, hasReady := fields["ready"]; hasReady {
		return nativewire.Transcript{}, true, validateReadyField(raw, len(fields))
	}

	var msg nativewire.Transcript
	if err := json.Unmarshal(line, &msg); err != nil {
		return nativewire.Transcript{}, false, fmt.Errorf("native stt response JSON: %w", err)
	}
	if err := validateTranscriptShape(&msg, fields); err != nil {
		return nativewire.Transcript{}, false, err
	}

	return msg, false, nil
}

func validateTranscriptShape(msg *nativewire.Transcript, fields map[string]json.RawMessage) error {
	if err := validateTranscriptText(msg); err != nil {
		return err
	}
	if err := validateInferenceField(msg.InferenceMS, fields); err != nil {
		return err
	}

	return validateEndSamplesField(fields)
}

func validateReadyField(raw json.RawMessage, fieldCount int) error {
	var ready bool
	if err := json.Unmarshal(raw, &ready); err != nil || !ready || fieldCount != 1 {
		return errors.New("native stt response: ready must be the sole true field")
	}

	return nil
}

// validateTranscriptText enforces the exclusive partial/final shape. A JSON
// null decodes to the same nil pointer as an absent field, so both are
// rejected by the presence test.
func validateTranscriptText(msg *nativewire.Transcript) error {
	if msg.Final {
		if msg.Text == nil || msg.Partial != nil {
			return errors.New("native stt response: final requires text and forbids partial")
		}

		return nil
	}
	if msg.Partial == nil || msg.Text != nil {
		return errors.New("native stt response: partial requires partial and forbids text")
	}

	return nil
}

func validateInferenceField(value *float64, fields map[string]json.RawMessage) error {
	if raw, present := fields["inference_ms"]; present {
		var decoded *float64
		if err := json.Unmarshal(raw, &decoded); err != nil || decoded == nil {
			return errors.New("native stt response: inference_ms must be a number")
		}
	}
	if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0) ||
		*value < 0 || *value > float64((10*time.Minute)/time.Millisecond)) {
		return errors.New("native stt response: inference_ms must be between 0 and 600000")
	}

	return nil
}

func validateEndSamplesField(fields map[string]json.RawMessage) error {
	raw, present := fields["end_samples"]
	if !present {
		return errors.New("native stt response: end_samples is required on every transcript")
	}
	var value *int64
	if err := json.Unmarshal(raw, &value); err != nil || value == nil || *value < 0 {
		return errors.New("native stt response: end_samples must be a non-negative integer")
	}

	return nil
}

func (s *sttSession) applyDetectedLanguage(value string) error {
	if value == "" {
		return nil
	}

	detected, err := lang.Parse(value)
	if err != nil {
		return fmt.Errorf("native stt response language %q: %w", value, err)
	}
	if detected == core.LangAuto {
		return fmt.Errorf("native stt response language %q is not concrete", value)
	}
	s.lang = detected

	return nil
}

// transcriptUpdate folds one validated frame into an engine event; the text
// pointer of the reported kind is non-nil by validateTranscriptText.
func transcriptUpdate(
	msg *nativewire.Transcript, language core.Lang, sourceEnd time.Duration,
) (engine.Transcript, bool) {
	if msg.Final {
		text := strings.TrimSpace(*msg.Text)

		// An empty final still closes the agreement epoch. Dropping it leaks
		// the previous utterance's prefix into the next speaker turn.
		return engine.Transcript{
			Text: text, Lang: language, SourceEnd: sourceEnd,
			Stable: true, Final: true, HasSourceEnd: true,
		}, true
	}

	text := strings.TrimSpace(*msg.Partial)

	return engine.Transcript{
		Text: text, Lang: language, SourceEnd: sourceEnd, HasSourceEnd: true,
	}, text != ""
}

// sourceTimeline maps the helper's cumulative mono-sample offsets back onto
// the PTS carried by frames written to its stdin. Frames are recorded before
// the corresponding pipe write, so even a very fast helper can resolve an
// event without racing the producer. Resolved history is pruned while retaining
// the boundary frame for duplicate offsets.
type sourceTimeline struct {
	frames       []sourceFrame
	totalSamples int64
	lastResolved int64
	mu           sync.Mutex
}

type sourceFrame struct {
	startSamples int64
	endSamples   int64
	pts          time.Duration
	rate         int
}

func (t *sourceTimeline) record(frame core.PCM) {
	if len(frame.Data) == 0 {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	start := t.totalSamples
	t.totalSamples += int64(len(frame.Data))
	if len(t.frames) != 0 {
		last := &t.frames[len(t.frames)-1]
		duration, err := sampleDuration(last.endSamples-last.startSamples, last.rate)
		const maxDuration = time.Duration(1<<63 - 1)
		if err == nil && last.rate == frame.Rate && last.pts <= maxDuration-duration &&
			last.pts+duration == frame.PTS {
			last.endSamples = t.totalSamples

			return
		}
	}
	t.frames = append(t.frames, sourceFrame{
		startSamples: start, endSamples: t.totalSamples, pts: frame.PTS, rate: frame.Rate,
	})
}

func (t *sourceTimeline) resolve(endSamples int64) (time.Duration, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if endSamples < t.lastResolved {
		return 0, fmt.Errorf(
			"end_samples moved backward from %d to %d", t.lastResolved, endSamples,
		)
	}
	if endSamples > t.totalSamples {
		return 0, fmt.Errorf(
			"end_samples %d exceeds %d samples written to the helper",
			endSamples, t.totalSamples,
		)
	}
	if endSamples == 0 {
		t.lastResolved = 0
		if len(t.frames) != 0 {
			return t.frames[0].pts, nil
		}

		return 0, nil
	}

	for i, frame := range t.frames {
		if endSamples > frame.endSamples {
			continue
		}
		delta, err := sampleDuration(endSamples-frame.startSamples, frame.rate)
		if err != nil {
			return 0, err
		}
		const maxDuration = time.Duration(1<<63 - 1)
		if frame.pts > maxDuration-delta {
			return 0, errors.New("resolved source timestamp overflows time.Duration")
		}

		t.lastResolved = endSamples
		if i > 0 {
			t.frames = append(t.frames[:0], t.frames[i:]...)
		}

		return frame.pts + delta, nil
	}

	return 0, fmt.Errorf("end_samples %d has no recorded source frame", endSamples)
}

func sampleDuration(samples int64, rate int) (time.Duration, error) {
	if samples < 0 || rate <= 0 {
		return 0, errors.New("invalid source sample offset")
	}

	const maxDuration = time.Duration(1<<63 - 1)
	seconds := samples / int64(rate)
	if seconds > int64(maxDuration/time.Second) {
		return 0, errors.New("source sample offset overflows time.Duration")
	}
	duration := time.Duration(seconds) * time.Second
	fraction := time.Duration(samples%int64(rate)) * time.Second / time.Duration(rate)
	if fraction > maxDuration-duration {
		return 0, errors.New("source sample offset overflows time.Duration")
	}

	return duration + fraction, nil
}

// emit forwards one update unless the session is stopping.
func (s *sttSession) emit(update engine.Transcript) bool {
	select {
	case s.events <- update:
		return true
	case <-s.stop:
		return false
	}
}

// watch kills the helper when the context is canceled, unblocking a read that
// is waiting on stdout or on a stalled consumer.
func (s *sttSession) watch(ctx context.Context) {
	select {
	case <-ctx.Done():
		s.stopped.Store(true)
		s.stopHelper()
	case <-s.done:
	}
}

// stopHelper closes input and arms a bounded process-tree kill. It is safe
// when cancellation and a protocol/scan failure arrive concurrently.
func (s *sttSession) stopHelper() {
	s.stopOnce.Do(func() {
		close(s.stop)
		if err := s.CloseSend(); err != nil && !errors.Is(err, os.ErrClosed) {
			s.errMu.Lock()
			s.cleanupErr = errors.Join(s.cleanupErr, fmt.Errorf("close native stt input: %w", err))
			s.errMu.Unlock()
			s.log.Debug("native stt closesend on stop", "err", err)
		}
		go s.forceStop()
	})
}

func (s *sttSession) forceStop() {
	s.spawnedHelper.forceStop(s.done, "native stt", s.log)
}

// reap retires the process tree and waits for the child exactly once; a
// non-clean exit that was not asked for is logged with the helper's stderr
// tail.
func (s *sttSession) reap(scanErr error) {
	unexpectedEOF := scanErr == nil && !s.sendClosed.Load() && !s.stopped.Load()
	// A helper may close stdout while it still waits on stdin. Arm shutdown
	// before every Wait; otherwise even a clean scanner EOF can wedge a lane.
	s.stopHelper()
	if unexpectedEOF {
		scanErr = io.ErrUnexpectedEOF
	}
	waitErr, treeErr := closeTreeAndWait(s.tree, s.cmd.Wait)
	stderr := s.stderr.String()
	var terminalErr, terminalCause error

	s.errMu.Lock()
	s.cleanupErr = errors.Join(s.cleanupErr, treeErr)
	writeErr := s.writeErr
	if s.stopped.Load() && writeErr == nil {
		s.errMu.Unlock()

		return
	}

	terminalCause = errors.Join(writeErr, scanErr, waitErr, treeErr)
	if terminalCause != nil {
		terminalErr = withHelperStderrTail(
			fmt.Errorf("native stt helper: %w", terminalCause), stderr,
		)
		s.terminal = terminalErr
	}
	s.errMu.Unlock()

	if terminalErr != nil {
		s.log.Warn("native stt exited", "err", terminalCause, "stderr", stderr)
	}
}
