package native

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/engine"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

// subTTS is the synthesis subcommand of the configured engine helper.
const subTTS = "tts"

// ttsPCMQueue buffers synthesized chunks so a brief consumer stall never blocks
// the helper's stdout pump.
const ttsPCMQueue = 8

// TTSConfig configures synthesis through an external engine helper. One stdio
// protocol process is kept per voice; the helper owns any per-clause synthesis
// subprocesses. core.Voice.ID names the configured voice model.
type TTSConfig struct {
	Log  *slog.Logger
	Bin  string
	Rate int
}

// TTS implements engine.Synthesizer over warm per-voice synthesis helpers.
type TTS struct {
	closeErr  error
	life      context.Context
	cancel    context.CancelFunc
	log       *slog.Logger
	procs     map[string]*voiceProc
	bin       string
	rate      int
	mu        sync.Mutex
	closeOnce sync.Once
	closed    bool
}

// NewTTS wires a synthesizer from the resolved config.
func NewTTS(cfg *TTSConfig) *TTS {
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	life, cancel := context.WithCancel(context.Background())

	return &TTS{
		life: life, cancel: cancel,
		log: log, procs: map[string]*voiceProc{}, bin: cfg.Bin, rate: cfg.Rate,
	}
}

// Compile-time port checks.
var (
	_ engine.Synthesizer = (*TTS)(nil)
	_ engine.Closer      = (*TTS)(nil)
)

// Speak streams synthesized speech for one turn: clauses arrive on text and PCM
// chunks leave on the returned channel, both through the voice's warm process.
func (t *TTS) Speak(
	ctx context.Context, to core.Lang, v core.Voice, text <-chan string,
) (*engine.AudioStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !v.Supports(to) {
		return nil, fmt.Errorf("native tts: voice %q (%s) does not support %s", v.ID, v.Lang, to)
	}

	proc, err := t.warm(ctx, v)
	if err != nil {
		return nil, err
	}

	out := make(chan core.PCM, ttsPCMQueue)
	result := make(chan error, 1)
	go func() {
		runErr := proc.run(ctx, text, out)
		if proc.unusable() {
			runErr = errors.Join(runErr, t.discard(v.ID, proc))
		}
		result <- runErr
		close(result)
		close(out)
	}()

	return engine.NewAudioStream(out, result), nil
}

// warm returns the provider-owned synthesis process for the voice, spawning it
// on first use.
func (t *TTS) warm(ctx context.Context, v core.Voice) (*voiceProc, error) {
	t.mu.Lock()

	if t.closed {
		t.mu.Unlock()

		return nil, errors.New("native tts: synthesizer is closed")
	}
	if err := ctx.Err(); err != nil {
		t.mu.Unlock()

		return nil, err
	}

	if proc, ok := t.procs[v.ID]; ok && !proc.unusable() {
		t.mu.Unlock()

		return proc, nil
	}

	// Replace the (missing or stale) entry under the lock, but reap the stale
	// helper after releasing it: holding the lock through its shutdown would
	// stall every other voice.
	stale := t.procs[v.ID]
	proc, err := startVoiceProc(t.life, t.bin, v.ID, t.rate, t.log)
	if err != nil {
		delete(t.procs, v.ID)
		t.mu.Unlock()

		return nil, errors.Join(err, closeVoiceProc(stale))
	}
	t.procs[v.ID] = proc
	t.mu.Unlock()

	if cleanupErr := closeVoiceProc(stale); cleanupErr != nil {
		t.log.Warn("stale native tts helper cleanup failed", "err", cleanupErr)
	}

	return proc, nil
}

// closeVoiceProc reaps a replaced helper, tolerating a nil (no prior process).
func closeVoiceProc(proc *voiceProc) error {
	if proc == nil {
		return nil
	}

	return proc.close()
}

// discard removes proc only if it is still the cached process for voice, then
// reaps it outside the lock so its shutdown never stalls other voices.
func (t *TTS) discard(voice string, proc *voiceProc) error {
	t.mu.Lock()
	if t.procs[voice] == proc {
		delete(t.procs, voice)
	}
	t.mu.Unlock()

	return proc.close()
}

// Close stops and reaps every cached synthesis helper. It is idempotent.
func (t *TTS) Close() error {
	t.closeOnce.Do(func() { t.closeErr = t.close() })

	return t.closeErr
}

func (t *TTS) close() error {
	t.mu.Lock()
	t.closed = true
	procs := make([]*voiceProc, 0, len(t.procs))
	for voice, proc := range t.procs {
		procs = append(procs, proc)
		delete(t.procs, voice)
	}
	t.mu.Unlock()

	t.cancel()
	for _, proc := range procs {
		proc.abort()
	}
	cleanupErrs := make([]error, 0, len(procs))
	for _, proc := range procs {
		cleanupErrs = append(cleanupErrs, proc.close())
	}

	return errors.Join(cleanupErrs...)
}

// ttsRequest is one synthesis request line written to the helper.
type ttsRequest struct {
	Text string `json:"text"`
}

// ttsResponse is one helper output line: an audio chunk, or the turn boundary.
type ttsResponse struct {
	Audio string `json:"audio"`
	Done  bool   `json:"done"`
}

type ttsResult struct {
	err error
	msg ttsResponse
}

// voiceProc is one warm synthesis process for a single voice. The gate
// serializes turns; the read pump owns stdout and process reaping.
type voiceProc struct {
	*warmProcess

	responses chan ttsResult
	gate      chan struct{}
	rate      int
}

// startVoiceProc spawns and wires one warm synthesis process for a voice.
func startVoiceProc(ctx context.Context, bin, voice string, rate int, log *slog.Logger) (*voiceProc, error) {
	// bin is the operator-selected native engine, executed directly without a
	// shell; gosec G204 is waived for this file in .golangci.yml.
	cmd := exec.CommandContext(ctx, bin, subTTS, flagModel, voice, flagRate, strconv.Itoa(rate))
	child, err := startWarmProcess(ctx, cmd, "native tts", log)
	if err != nil {
		return nil, err
	}

	proc := &voiceProc{
		warmProcess: child,
		responses:   make(chan ttsResult, 1),
		gate:        make(chan struct{}, 1),
		rate:        rate,
	}
	proc.gate <- struct{}{}

	go proc.read(log)
	go proc.watch(ctx)

	return proc, nil
}

// run synthesizes each clause of one turn onto out, in order.
func (p *voiceProc) run(ctx context.Context, text <-chan string, out chan<- core.PCM) error {
	if err := acquire(ctx, p.gate, p.done); err != nil {
		if p.finished() {
			err = p.terminalError()
		}

		return err
	}
	defer func() { p.gate <- struct{}{} }()

	for {
		select {
		case <-ctx.Done():
			p.abort()

			return ctx.Err()
		case <-p.done:
			return p.terminalError()
		case clause, ok := <-text:
			if !ok {
				return nil
			}
			if strings.TrimSpace(clause) == "" {
				continue
			}
			if err := p.synth(ctx, clause, out); err != nil {
				return err
			}
		}
	}
}

// synth writes one clause and streams its PCM chunks until the turn boundary.
func (p *voiceProc) synth(ctx context.Context, clause string, out chan<- core.PCM) error {
	req, err := json.Marshal(ttsRequest{Text: clause})
	if err != nil {
		return fmt.Errorf("encode native tts request: %w", err)
	}
	req = append(req, '\n')

	if err := p.writeLine(ctx, req); err != nil {
		return fmt.Errorf("write native tts request: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			p.abort()

			return ctx.Err()
		case result, ok := <-p.responses:
			if !ok {
				return p.terminalError()
			}
			if result.err != nil {
				p.abort()

				return result.err
			}
			if result.msg.Done {
				return nil
			}
			if err := p.forward(ctx, result.msg.Audio, out); err != nil {
				p.abort()

				return err
			}
		}
	}
}

// read owns stdout for the lifetime of the helper and reaps it on every exit.
func (p *voiceProc) read(log *slog.Logger) {
	defer close(p.responses)

	scanner := bufio.NewScanner(p.stdout)
	scanner.Buffer(make([]byte, 0, 64<<10), scanLineMax)

	for scanner.Scan() {
		msg, err := decodeTTSResponse(scanner.Bytes())

		select {
		case p.responses <- ttsResult{msg: msg, err: err}:
		case <-p.stop:
			p.finish("native tts", scanner.Err())

			return
		}
	}

	p.finish("native tts", scanner.Err())
	if !p.stopping() {
		log.Debug("native tts reader exited", "err", p.exitErr)
	}
}

func decodeTTSResponse(line []byte) (ttsResponse, error) {
	var msg ttsResponse
	if err := json.Unmarshal(line, &msg); err != nil {
		return ttsResponse{}, fmt.Errorf("decode native tts response: %w", err)
	}

	hasAudio := msg.Audio != ""
	if hasAudio == msg.Done {
		return ttsResponse{}, errors.New(
			"decode native tts response: expected exactly one of non-empty audio or done=true",
		)
	}

	return msg, nil
}

// forward decodes and validates one base64 PCM chunk before publishing it.
func (p *voiceProc) forward(ctx context.Context, audio string, out chan<- core.PCM) error {
	if audio == "" {
		return nil
	}

	raw, err := base64.StdEncoding.DecodeString(audio)
	if err != nil {
		return fmt.Errorf("decode native tts audio: %w", err)
	}
	if len(raw)%2 != 0 {
		return fmt.Errorf("decode native tts audio: odd PCM byte count %d", len(raw))
	}

	samples := make([]int16, len(raw)/2)
	if decoded := pipeline.DecodeS16LE(samples, raw); decoded != len(samples) {
		return fmt.Errorf("decode native tts PCM: decoded %d of %d samples", decoded, len(samples))
	}

	select {
	case out <- core.PCM{Data: samples, Rate: p.rate, Ch: 1}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
