package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/realtime"
	"github.com/ubyte-source/prukka/internal/nativewire"
)

// Language-pair flags, spelled by nativewire like the rest of the argv half.
const (
	flagFrom = nativewire.FlagPrefix + nativewire.FlagFrom
	flagTo   = nativewire.FlagPrefix + nativewire.FlagTo
)

// MTConfig configures translation through an external engine helper, one
// process per language pair.
type MTConfig struct {
	Log        *slog.Logger
	Bin        string
	EngineRoot string
	Pairs      []realtime.LanguagePair
}

// MT implements realtime.Translator over warm per-pair translation helpers.
type MT struct {
	cache      *procCache[realtime.LanguagePair, *pairProc]
	pairs      map[realtime.LanguagePair]bool
	bin        string
	engineRoot string
	declared   bool
}

// NewMT wires a translator from the resolved config.
func NewMT(cfg *MTConfig) *MT {
	pairs := make(map[realtime.LanguagePair]bool, len(cfg.Pairs))
	for _, pair := range cfg.Pairs {
		pairs[basePair(pair.From, pair.To)] = true
	}

	return &MT{
		cache:      newProcCache[realtime.LanguagePair, *pairProc]("native mt: translator", cfg.Log),
		pairs:      pairs,
		bin:        cfg.Bin,
		engineRoot: cfg.EngineRoot,
		declared:   cfg.Pairs != nil,
	}
}

// Compile-time port checks.
var (
	_ realtime.Translator = (*MT)(nil)
	_ realtime.Closer     = (*MT)(nil)
)

// Supports reports configured model availability; a nil declaration admits
// every pair.
func (t *MT) Supports(from, to core.Lang) bool {
	return !t.declared || t.pairs[basePair(from, to)]
}

// Warm loads one directed model and proves its request path before live audio
// starts; the probe's output is discarded.
func (t *MT) Warm(ctx context.Context, from, to core.Lang) error {
	_, err := t.Translate(ctx, realtime.Segment{Text: ".", Lang: from}, to)
	if err != nil {
		return fmt.Errorf("warm native mt %s to %s: %w", from, to, err)
	}

	return nil
}

// Translate renders one source segment into the target through the pair's warm
// process, spawning it on first use.
func (t *MT) Translate(
	ctx context.Context, source realtime.Segment, to core.Lang,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !t.Supports(source.Lang, to) {
		return "", fmt.Errorf("native mt: model unavailable for %s to %s", source.Lang, to)
	}

	route := basePair(source.Lang, to)
	var lastErr error

	// Translation is idempotent, so a helper that exited between calls is
	// replaced and the call retried once.
	for range 2 {
		proc, err := t.pair(ctx, route)
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

		if cleanupErr := t.discard(route, proc); cleanupErr != nil {
			return "", errors.Join(err, fmt.Errorf("cleanup native mt: %w", cleanupErr))
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
	}

	return "", lastErr
}

// pair returns the warm process for a language pair, spawning it on first use.
func (t *MT) pair(ctx context.Context, route realtime.LanguagePair) (*pairProc, error) {
	return t.cache.get(ctx, route, func(life context.Context, log *slog.Logger) (*pairProc, error) {
		return startPairProc(life, t.bin, t.engineRoot, string(route.From), string(route.To), log)
	})
}

// discard removes proc only if it is still the cached process for the pair.
func (t *MT) discard(route realtime.LanguagePair, proc *pairProc) error {
	return t.cache.discard(route, proc)
}

// Close stops and reaps every cached translation helper. It is idempotent.
func (t *MT) Close() error {
	return t.cache.Close()
}

// basePair is the comparable identity of one directed translation route; both
// tags reduce to their base, so a regional variant keys the model it resolves to.
func basePair(from, to core.Lang) realtime.LanguagePair {
	return realtime.LanguagePair{From: from.Base(), To: to.Base()}
}

// pairProc is one warm translation process for a single language pair; the gate
// serializes requests, the read pump owns stdout and reaping.
type pairProc struct {
	*warmProcess

	responses chan helperResult[nativewire.TextLine]
	gate      chan struct{}
}

// startPairProc spawns and wires one warm translation process for a pair.
func startPairProc(ctx context.Context, bin, engineRoot, from, to string, log *slog.Logger) (*pairProc, error) {
	// bin runs directly without a shell (gosec G204 is waived for this file in
	// .golangci.yml).
	cmd := exec.CommandContext(ctx, bin, nativewire.SubMT, flagFrom, from, flagTo, to)
	cmd.Env = engineChildEnv(engineRoot)
	child, err := startWarmProcess(ctx, cmd, "native mt", log)
	if err != nil {
		return nil, err
	}

	proc := &pairProc{
		warmProcess: child,
		responses:   make(chan helperResult[nativewire.TextLine], 1),
		gate:        make(chan struct{}, 1),
	}
	proc.gate <- struct{}{}

	go readLines(child, proc.responses, decodeMTResponse)
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

	req, err := json.Marshal(nativewire.TextLine{Text: text})
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

func decodeMTResponse(line []byte) (nativewire.TextLine, error) {
	var wire struct {
		Text *string `json:"text"`
	}
	if err := json.Unmarshal(line, &wire); err != nil {
		return nativewire.TextLine{}, fmt.Errorf("decode native mt response: %w", err)
	}
	if wire.Text == nil {
		return nativewire.TextLine{}, errors.New("decode native mt response: missing string field text")
	}

	return nativewire.TextLine{Text: *wire.Text}, nil
}
