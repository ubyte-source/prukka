package native

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/engine"
)

// subMT is the translation subcommand of the configured engine helper.
const subMT = "mt"

// Language-pair flags.
const (
	flagFrom = "--from"
	flagTo   = "--to"
)

// MTConfig configures translation through an external engine helper. One
// protocol process is kept per language pair and exchanges newline-delimited
// requests and responses with the mt subcommand.
type MTConfig struct {
	Log   *slog.Logger
	Bin   string
	Pairs []engine.LanguagePair
}

// MT implements engine.Translator over warm per-pair translation helpers.
type MT struct {
	closeErr  error
	life      context.Context
	cancel    context.CancelFunc
	log       *slog.Logger
	procs     map[string]*pairProc
	pairs     map[string]bool
	bin       string
	mu        sync.Mutex
	closeOnce sync.Once
	closed    bool
	declared  bool
}

// NewMT wires a translator from the resolved config.
func NewMT(cfg *MTConfig) *MT {
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	pairs := make(map[string]bool, len(cfg.Pairs))
	for _, pair := range cfg.Pairs {
		pairs[pairKey(baseTag(pair.From), baseTag(pair.To))] = true
	}
	life, cancel := context.WithCancel(context.Background())

	return &MT{
		life: life, cancel: cancel,
		log: log, procs: map[string]*pairProc{}, pairs: pairs,
		bin: cfg.Bin, declared: cfg.Pairs != nil,
	}
}

// Compile-time port checks.
var (
	_ engine.Translator = (*MT)(nil)
	_ engine.Closer     = (*MT)(nil)
)

// Supports reports configured model availability. A nil declaration keeps the
// adapter usable in protocol tests; daemon composition always supplies pairs.
func (t *MT) Supports(from, to core.Lang) bool {
	return !t.declared || t.pairs[pairKey(baseTag(from), baseTag(to))]
}

// Translate renders one source segment into the target through the language
// pair's warm process, spawning it on first use.
func (t *MT) Translate(
	ctx context.Context, source engine.Segment, to core.Lang,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !t.Supports(source.Lang, to) {
		return "", fmt.Errorf("native mt: model unavailable for %s to %s", source.Lang, to)
	}

	from, target := baseTag(source.Lang), baseTag(to)
	var lastErr error

	// A cached helper may have exited between calls. Translation is
	// idempotent, so replace it and retry once after a pipe/protocol failure.
	for range 2 {
		proc, err := t.pair(ctx, from, target)
		if err != nil {
			return "", err
		}

		translated, err := proc.translate(ctx, source.Text)
		if err == nil {
			return translated, nil
		}

		lastErr = err
		if !proc.unusable() {
			return "", err
		}

		if cleanupErr := t.discard(from, target, proc); cleanupErr != nil {
			return "", errors.Join(err, fmt.Errorf("cleanup native mt: %w", cleanupErr))
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
	}

	return "", lastErr
}

// pair returns the provider-owned warm process for a language pair, spawning
// it on first use.
func (t *MT) pair(ctx context.Context, from, to string) (*pairProc, error) {
	t.mu.Lock()

	if t.closed {
		t.mu.Unlock()

		return nil, errors.New("native mt: translator is closed")
	}
	if err := ctx.Err(); err != nil {
		t.mu.Unlock()

		return nil, err
	}

	key := pairKey(from, to)
	if proc, ok := t.procs[key]; ok && !proc.unusable() {
		t.mu.Unlock()

		return proc, nil
	}

	// Replace the (missing or stale) entry under the lock, but reap the stale
	// helper after releasing it: its shutdown can take the full stop grace, and
	// holding the lock through that would stall every other pair.
	stale := t.procs[key]
	proc, err := startPairProc(t.life, t.bin, from, to, t.log)
	if err != nil {
		delete(t.procs, key)
		t.mu.Unlock()

		return nil, errors.Join(err, closePairProc(stale))
	}
	t.procs[key] = proc
	t.mu.Unlock()

	if cleanupErr := closePairProc(stale); cleanupErr != nil {
		t.log.Warn("stale native mt helper cleanup failed", "err", cleanupErr)
	}

	return proc, nil
}

// closePairProc reaps a replaced helper, tolerating a nil (no prior process).
func closePairProc(proc *pairProc) error {
	if proc == nil {
		return nil
	}

	return proc.close()
}

// discard removes proc only if it is still the cached process for the pair,
// then reaps it outside the lock so its shutdown never stalls other pairs.
func (t *MT) discard(from, to string, proc *pairProc) error {
	key := pairKey(from, to)

	t.mu.Lock()
	if t.procs[key] == proc {
		delete(t.procs, key)
	}
	t.mu.Unlock()

	return proc.close()
}

// Close stops and reaps every cached translation helper. It is idempotent.
func (t *MT) Close() error {
	t.closeOnce.Do(func() { t.closeErr = t.close() })

	return t.closeErr
}

func (t *MT) close() error {
	t.mu.Lock()
	t.closed = true
	procs := make([]*pairProc, 0, len(t.procs))
	for key, proc := range t.procs {
		procs = append(procs, proc)
		delete(t.procs, key)
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

func pairKey(from, to string) string { return from + ">" + to }

// mtRequest is one translation request line; mtResponse is its reply.
type (
	mtRequest struct {
		Text string `json:"text"`
	}
	mtResponse struct {
		Text string `json:"text"`
	}
	mtResult struct {
		err error
		msg mtResponse
	}
)

// pairProc is one warm translation process for a single language pair. The
// gate serializes requests; the read pump owns stdout and process reaping.
type pairProc struct {
	*warmProcess

	responses chan mtResult
	gate      chan struct{}
}

// startPairProc spawns and wires one warm translation process for a pair.
func startPairProc(ctx context.Context, bin, from, to string, log *slog.Logger) (*pairProc, error) {
	// bin is the operator-selected native engine, executed directly without a
	// shell; gosec G204 is waived for this file in .golangci.yml.
	cmd := exec.CommandContext(ctx, bin, subMT, flagFrom, from, flagTo, to)
	child, err := startWarmProcess(ctx, cmd, "native mt", log)
	if err != nil {
		return nil, err
	}

	proc := &pairProc{
		warmProcess: child,
		responses:   make(chan mtResult, 1),
		gate:        make(chan struct{}, 1),
	}
	proc.gate <- struct{}{}

	go proc.read(log)
	go proc.watch(ctx)

	return proc, nil
}

// translate sends one source text and returns the model's translation.
func (p *pairProc) translate(ctx context.Context, text string) (string, error) {
	if err := acquire(ctx, p.gate, p.done); err != nil {
		if p.finished() {
			return "", p.terminalError()
		}

		return "", err
	}
	defer func() { p.gate <- struct{}{} }()

	req, err := json.Marshal(mtRequest{Text: text})
	if err != nil {
		return "", fmt.Errorf("encode native mt request: %w", err)
	}
	req = append(req, '\n')

	if err := p.writeLine(ctx, req); err != nil {
		return "", fmt.Errorf("write native mt request: %w", err)
	}

	select {
	case <-ctx.Done():
		p.abort()

		return "", ctx.Err()
	case result, ok := <-p.responses:
		if !ok {
			return "", p.terminalError()
		}
		if result.err != nil {
			p.abort()

			return "", result.err
		}

		return result.msg.Text, nil
	}
}

// read owns stdout for the lifetime of the helper and reaps it on every exit.
func (p *pairProc) read(log *slog.Logger) {
	defer close(p.responses)

	scanner := bufio.NewScanner(p.stdout)
	scanner.Buffer(make([]byte, 0, 64<<10), scanLineMax)

	for scanner.Scan() {
		msg, err := decodeMTResponse(scanner.Bytes())

		select {
		case p.responses <- mtResult{msg: msg, err: err}:
		case <-p.stop:
			p.finish("native mt", scanner.Err())

			return
		}
	}

	p.finish("native mt", scanner.Err())
	if !p.stopping() {
		log.Debug("native mt reader exited", "err", p.exitErr)
	}
}

func decodeMTResponse(line []byte) (mtResponse, error) {
	var wire struct {
		Text *string `json:"text"`
	}
	if err := json.Unmarshal(line, &wire); err != nil {
		return mtResponse{}, fmt.Errorf("decode native mt response: %w", err)
	}
	if wire.Text == nil {
		return mtResponse{}, errors.New("decode native mt response: missing string field text")
	}

	return mtResponse{Text: *wire.Text}, nil
}
