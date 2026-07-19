// Package pivot routes machine translation through a hub language. With only
// hub<->X models installed per language, any source reaches any target as two
// legs — source->hub->target — instead of shipping an N^2 matrix of direct
// pair models. English is the hub for the bundled Opus-MT models: every shipped
// pair is en<->X, so English connects every language to every other.
package pivot

import (
	"context"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/engine"
)

// English is the pivot hub for the bundled Opus-MT models.
const English core.Lang = "en"

// warmable is a translator whose directed models can be preloaded before live
// audio. The native adapter satisfies it; the decorator preserves the
// capability by warming each leg of a pivoted route.
type warmable interface {
	engine.Translator
	Warm(ctx context.Context, from, to core.Lang) error
}

// Translator decorates an inner translator with hub routing. A route with no
// direct model is served through the hub; a route the hub cannot bridge is
// reported unsupported, exactly as the inner translator would report it.
type Translator struct {
	inner warmable
	hub   core.Lang
}

// NewTranslator wraps inner so any from<->to backed by from<->hub and hub<->to
// becomes translatable through hub.
func NewTranslator(inner warmable, hub core.Lang) *Translator {
	return &Translator{inner: inner, hub: hub}
}

// Compile-time port checks: the decorator is a drop-in translator and closer.
var (
	_ engine.Translator = (*Translator)(nil)
	_ engine.Closer     = (*Translator)(nil)
)

// Supported reports whether from->to is translatable given a direct-pair
// oracle: directly, as a free same-language copy, or bridged through hub
// (from->hub->to). The live decorator and any admission gate route through it,
// so their verdicts on which sessions the hub can serve cannot diverge — a gate
// that re-derived support from raw config pairs would reject exactly the bridged
// sessions the decorator was built to serve.
func Supported(direct func(from, to core.Lang) bool, hub, from, to core.Lang) bool {
	if core.SameLang(from, to) {
		return true
	}
	if direct(from, to) {
		return true
	}
	if core.SameLang(from, hub) || core.SameLang(to, hub) {
		return false
	}

	return direct(from, hub) && direct(hub, to)
}

// Supports reports whether from->to is translatable directly, as a free
// same-language copy, or bridged through the hub.
func (t *Translator) Supports(from, to core.Lang) bool {
	return Supported(t.inner.Supports, t.hub, from, to)
}

// Translate renders source into to. A same-language target returns the source
// text unchanged; a target with no direct model pivots through the hub, and the
// hub leg keeps the source span so downstream schedules stay aligned.
func (t *Translator) Translate(
	ctx context.Context, source engine.Segment, to core.Lang,
) (string, error) {
	if core.SameLang(source.Lang, to) {
		return source.Text, nil
	}
	if t.inner.Supports(source.Lang, to) {
		return t.inner.Translate(ctx, source, to)
	}

	mid, err := t.inner.Translate(ctx, source, t.hub)
	if err != nil {
		return "", err
	}

	return t.inner.Translate(ctx, engine.Segment{Text: mid, Lang: t.hub, Span: source.Span}, to)
}

// Warm preloads from->to. A pivoted route warms both hub legs so the first live
// clause pays no model-load latency on either.
func (t *Translator) Warm(ctx context.Context, from, to core.Lang) error {
	if core.SameLang(from, to) {
		return nil
	}
	if t.inner.Supports(from, to) {
		return t.inner.Warm(ctx, from, to)
	}

	if err := t.inner.Warm(ctx, from, t.hub); err != nil {
		return err
	}

	return t.inner.Warm(ctx, t.hub, to)
}

// Close releases the wrapped translator.
func (t *Translator) Close() error { return t.inner.Close() }
