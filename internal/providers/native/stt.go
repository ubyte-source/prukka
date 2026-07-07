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
)

// sttEventQueue buffers transcript updates so a brief consumer stall never
// blocks the stdout read pump.
const sttEventQueue = 8

// stderrTail bounds how much of a helper's stderr is kept for a failure log.
const stderrTail = 4 << 10

// scanLineMax caps one JSON transcript line; STT lines are short sentences.
const scanLineMax = 1 << 20

// subSTT is the transcription subcommand of the configured engine helper.
const subSTT = "stt"

// Helper invocation flags, named once.
const (
	flagModel    = "--model"
	flagRate     = "--rate"
	flagThreads  = "--threads"
	flagLanguage = "--language"
)

// STTConfig configures transcription through an external engine helper. Its
// stt subcommand reads 16-bit little-endian mono PCM from stdin and writes
// newline-delimited JSON transcripts to stdout.
type STTConfig struct {
	Log     *slog.Logger
	Bin     string
	Model   string
	Rate    int
	Threads int
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

	stderr := &tailBuffer{limit: stderrTail}
	cmd.Stderr = stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("native stt stdin: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("native stt stdout: %w", err), stdin.Close())
	}

	// Own cancellation so stdin gets a graceful close before the tree kill.
	cmd.Cancel = nil
	prepareProcessTree(cmd)
	if startErr := cmd.Start(); startErr != nil {
		return nil, errors.Join(
			fmt.Errorf("start native stt: %w", startErr),
			stdin.Close(),
			stdout.Close(),
		)
	}
	tree, treeErr := attachProcessTree(cmd)
	if treeErr != nil {
		s.log.Warn("native helper process-tree fallback", "stage", "native stt", "err", treeErr)
	}

	session := &sttSession{
		stdin: stdin, stdout: stdout, ctx: ctx, cmd: cmd, stderr: stderr, tree: tree,
		events: make(chan engine.Transcript, sttEventQueue),
		done:   make(chan struct{}), stop: make(chan struct{}),
		log: s.log, lang: hint, rate: s.cfg.Rate,
	}

	go session.watch(ctx)
	go session.read()

	return session, nil
}

// args builds the helper invocation; the language hint is passed only when the
// caller pinned one, leaving auto-detection to the model otherwise.
func (s *STT) args(hint core.Lang) []string {
	threads := max(1, s.cfg.Threads)
	args := []string{
		subSTT,
		flagModel, s.cfg.Model,
		flagRate, strconv.Itoa(s.cfg.Rate),
		flagThreads, strconv.Itoa(threads),
	}
	if hint != core.LangAuto {
		args = append(args, flagLanguage, baseTag(hint))
	}

	return args
}

// sttMessage is one line from the helper: a partial refines the live tail, a
// final closes a segment; language, when present, reports the detected tongue.
type sttMessage struct {
	Partial  string `json:"partial"`
	Text     string `json:"text"`
	Language string `json:"language"`
	Final    bool   `json:"final"`
}

// sttSession is one live transcription: the read pump owns events, Push and
// CloseSend write to stdin, watch ends both on cancellation.
type sttSession struct {
	stdin      io.WriteCloser
	stdout     io.ReadCloser
	ctx        context.Context
	tree       processTree
	terminal   error
	cleanupErr error
	writeErr   error
	cmd        *exec.Cmd
	stderr     *tailBuffer
	events     chan engine.Transcript
	done       chan struct{}
	stop       chan struct{}
	log        *slog.Logger
	lang       core.Lang
	raw        []byte
	rate       int
	stopped    atomic.Bool
	sendClosed atomic.Bool
	errMu      sync.Mutex
	writeMu    sync.Mutex
	stdinOnce  sync.Once
	stopOnce   sync.Once
}

// Push implements engine.Transcription: it streams one audio chunk to stdin.
func (s *sttSession) Push(frame core.PCM) error {
	if frame.Rate != s.rate {
		return fmt.Errorf("native stt: PCM rate %d, want %d", frame.Rate, s.rate)
	}
	if frame.Ch != 1 {
		return fmt.Errorf("native stt: PCM channels %d, want 1", frame.Ch)
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if err := s.ctx.Err(); err != nil {
		return err
	}

	s.raw = pipeline.AppendS16LE(s.raw[:0], frame.Data)
	n, err := s.stdin.Write(s.raw)
	if err != nil {
		if ctxErr := s.ctx.Err(); ctxErr != nil {
			return ctxErr
		}

		return s.writeFailure(err)
	}
	if n != len(s.raw) {
		return s.writeFailure(io.ErrShortWrite)
	}

	return nil
}

// writeFailure records the pipe cause before stopping the helper so reap keeps
// its terminal diagnostic even if another goroutine cancels the session. The
// wait is bounded by stopHelper's process-tree kill.
func (s *sttSession) writeFailure(writeErr error) error {
	failure := fmt.Errorf("native stt write PCM: %w", writeErr)
	s.errMu.Lock()
	s.writeErr = errors.Join(s.writeErr, failure)
	s.errMu.Unlock()

	s.stopHelper()
	<-s.done
	if ctxErr := s.ctx.Err(); ctxErr != nil {
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
	msg, err := decodeSTTMessage(line)
	if err != nil {
		return err
	}
	if err := s.applyDetectedLanguage(msg.Language); err != nil {
		return err
	}

	update, ok := transcriptUpdate(msg, s.lang)
	if !ok || s.emit(update) {
		return nil
	}

	return context.Canceled
}

func decodeSTTMessage(line []byte) (sttMessage, error) {
	var msg sttMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		return sttMessage{}, fmt.Errorf("native stt response JSON: %w", err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return sttMessage{}, fmt.Errorf("native stt response fields: %w", err)
	}
	if err := validateSTTMessageShape(msg, fields); err != nil {
		return sttMessage{}, err
	}

	return msg, nil
}

func validateSTTMessageShape(msg sttMessage, fields map[string]json.RawMessage) error {
	_, hasPartial := fields["partial"]
	_, hasText := fields["text"]
	if msg.Final {
		if !hasText || hasPartial {
			return errors.New("native stt response: final requires text and forbids partial")
		}
		if err := validateStringField(fields, "text"); err != nil {
			return err
		}
	} else if !hasPartial || hasText {
		return errors.New("native stt response: partial requires partial and forbids text")
	} else if err := validateStringField(fields, "partial"); err != nil {
		return err
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

func transcriptUpdate(msg sttMessage, language core.Lang) (engine.Transcript, bool) {
	if msg.Final {
		text := strings.TrimSpace(msg.Text)

		return engine.Transcript{Text: text, Lang: language, Stable: true, Final: true}, text != ""
	}

	text := strings.TrimSpace(msg.Partial)

	return engine.Transcript{Text: text, Lang: language}, text != ""
}

func validateStringField(fields map[string]json.RawMessage, name string) error {
	var value *string
	if err := json.Unmarshal(fields[name], &value); err != nil {
		return fmt.Errorf("native stt response: %s must be a string: %w", name, err)
	}
	if value == nil {
		return fmt.Errorf("native stt response: %s must not be null", name)
	}

	return nil
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
	timer := time.NewTimer(helperStopGrace)
	defer timer.Stop()

	select {
	case <-s.done:
		return
	case <-timer.C:
	}

	if err := s.tree.kill(); err != nil {
		s.log.Debug("native helper kill tree", "stage", "native stt", "err", err)
	}
	if err := s.stdout.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		s.log.Debug("native helper close output", "stage", "native stt", "err", err)
	}
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

// baseTag is the ISO 639-1 base of a language tag.
func baseTag(l core.Lang) string {
	base, _, _ := strings.Cut(string(l), "-")

	return base
}

// tailBuffer keeps the last limit bytes written to it, for failure reporting.
type tailBuffer struct {
	buf   []byte
	mu    sync.Mutex
	limit int
}

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.limit <= 0 {
		return len(b), nil
	}
	if len(b) >= t.limit {
		t.buf = append(t.buf[:0], b[len(b)-t.limit:]...)

		return len(b), nil
	}

	t.buf = append(t.buf, b...)
	if len(t.buf) > t.limit {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-t.limit:]...)
	}

	return len(b), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	return strings.TrimSpace(string(t.buf))
}
