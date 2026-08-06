// Package pivot routes machine translation through a hub language: with only
// hub<->X models installed, any source reaches any target as two legs instead of
// an N^2 matrix of direct pair models.
package pivot

import (
	"context"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/realtime"
)

// English is the pivot hub for the bundled Opus-MT models.
const English core.Lang = "en"

// warmable is a translator whose directed models can be preloaded.
type warmable interface {
	realtime.Translator
	Warm(ctx context.Context, from, to core.Lang) error
}

// Translator decorates an inner translator with hub routing: a route with no
// direct model is served through the hub, one the hub cannot bridge is
// unsupported.
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
	_ realtime.Translator = (*Translator)(nil)
	_ realtime.Closer     = (*Translator)(nil)
)

// Supported reports whether from->to is translatable given a direct-pair oracle:
// directly, as a free same-language copy, or bridged through hub. Every
// admission gate must route through it or its verdict diverges from the runtime.
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

// Translate renders source into to; a same-language target returns the source
// unchanged, and a pivoted route's hub leg keeps the source span.
func (t *Translator) Translate(
	ctx context.Context, source realtime.Segment, to core.Lang,
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

	return t.inner.Translate(ctx, realtime.Segment{Text: mid, Lang: t.hub, Span: source.Span}, to)
}

// Warm preloads from->to, both legs of a pivoted route.
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
