package pivot_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/engine"
	"github.com/ubyte-source/prukka/internal/providers/pivot"
)

// route is one directed language pair, keyed by base tag to mirror the native
// adapter, which resolves regional variants to their base before lookup.
type route struct{ from, to core.Lang }

func based(from, to core.Lang) route { return route{from.Base(), to.Base()} }

// fakeInner is a scriptable Translator that records the legs the decorator
// drives. Installed pairs are keyed by base tag; recorded legs keep the raw
// tags so tests can assert the exact hub routing.
type fakeInner struct {
	pairs      map[route]bool
	transErr   map[route]error
	warmErr    map[route]error
	translated []route
	segs       []engine.Segment
	warmed     []route
	closes     int
}

func (f *fakeInner) Supports(from, to core.Lang) bool { return f.pairs[based(from, to)] }

func (f *fakeInner) Translate(
	_ context.Context, source engine.Segment, to core.Lang,
) (string, error) {
	f.translated = append(f.translated, route{source.Lang, to})
	f.segs = append(f.segs, source)
	if err := f.transErr[based(source.Lang, to)]; err != nil {
		return "", err
	}
	if !f.pairs[based(source.Lang, to)] {
		return "", errors.New("fake: no model")
	}

	return string(to.Base()) + ":" + source.Text, nil
}

func (f *fakeInner) Warm(_ context.Context, from, to core.Lang) error {
	f.warmed = append(f.warmed, route{from, to})

	return f.warmErr[based(from, to)]
}

func (f *fakeInner) Close() error {
	f.closes++

	return nil
}

// hub wires a decorator over an inner seeded with the it<->en<->de spokes plus
// a dangling fr>en leg (no en>fr) to prove one-sided coverage is not a bridge.
func hub() (*pivot.Translator, *fakeInner) {
	inner := &fakeInner{
		pairs: map[route]bool{
			{"it", "en"}: true, {"en", "it"}: true,
			{"de", "en"}: true, {"en", "de"}: true,
			{"fr", "en"}: true,
		},
		transErr: map[route]error{},
		warmErr:  map[route]error{},
	}

	return pivot.NewTranslator(inner, pivot.English), inner
}

func TestSupports(t *testing.T) {
	t.Parallel()

	tr, _ := hub()
	cases := []struct {
		from, to core.Lang
		why      string
		want     bool
	}{
		{"it", "en", "direct spoke to hub", true},
		{"en", "de", "direct spoke from hub", true},
		{"it", "de", "bridged it>en>de", true},
		{"de", "it", "bridged de>en>it", true},
		{"it", "it", "same language is free", true},
		{"en-US", "en", "regional variant of same base", true},
		{"fr", "de", "bridged fr>en>de: both legs installed", true},
		{"fr", "en", "direct fr>en spoke", true},
		{"it", "fr", "it>en installed but en>fr missing, no bridge", false},
		{"de", "fr", "de>en installed but en>fr missing, no bridge", false},
		{"en", "fr", "hub source cannot bridge", false},
		{"pt", "de", "unknown source", false},
	}
	for _, c := range cases {
		if got := tr.Supports(c.from, c.to); got != c.want {
			t.Errorf("Supports(%q,%q)=%v, want %v (%s)", c.from, c.to, got, c.want, c.why)
		}
	}
}

func TestSupported(t *testing.T) {
	t.Parallel()

	installed := map[route]bool{
		{"it", "en"}: true, {"en", "it"}: true,
		{"de", "en"}: true, {"en", "de"}: true,
	}
	direct := func(from, to core.Lang) bool { return installed[based(from, to)] }

	cases := []struct {
		from, to core.Lang
		why      string
		want     bool
	}{
		{"it", "en", "direct spoke", true},
		{"it", "de", "bridged it>en>de", true},
		{"de", "it", "bridged de>en>it", true},
		{"it", "it", "same language", true},
		{"en", "de", "direct from hub", true},
		{"it", "fr", "no en>fr leg", false},
		{"en", "fr", "hub source cannot bridge", false},
		{"pt", "de", "unknown source", false},
	}
	for _, c := range cases {
		if got := pivot.Supported(direct, pivot.English, c.from, c.to); got != c.want {
			t.Errorf("Supported(%q,%q)=%v, want %v (%s)", c.from, c.to, got, c.want, c.why)
		}
	}
}

func TestTranslateDirect(t *testing.T) {
	t.Parallel()

	tr, inner := hub()
	got, err := tr.Translate(context.Background(), engine.Segment{Text: "ciao", Lang: "it"}, "en")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "en:ciao" {
		t.Errorf("got %q, want %q", got, "en:ciao")
	}
	if want := []route{{"it", "en"}}; !reflect.DeepEqual(inner.translated, want) {
		t.Errorf("legs = %v, want single direct leg %v", inner.translated, want)
	}
}

func TestTranslatePivot(t *testing.T) {
	t.Parallel()

	tr, inner := hub()
	got, err := tr.Translate(context.Background(), engine.Segment{Text: "ciao", Lang: "it"}, "de")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "de:en:ciao" {
		t.Errorf("got %q, want the twice-translated %q", got, "de:en:ciao")
	}
	if want := []route{{"it", "en"}, {"en", "de"}}; !reflect.DeepEqual(inner.translated, want) {
		t.Errorf("legs = %v, want hub route %v", inner.translated, want)
	}
}

func TestTranslateSameLanguageSkipsInner(t *testing.T) {
	t.Parallel()

	tr, inner := hub()
	got, err := tr.Translate(context.Background(), engine.Segment{Text: "ciao", Lang: "it-CH"}, "it")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "ciao" {
		t.Errorf("got %q, want the source text unchanged", got)
	}
	if len(inner.translated) != 0 {
		t.Errorf("inner was called %d times, want 0 for a same-language target", len(inner.translated))
	}
}

func TestTranslateFirstLegError(t *testing.T) {
	t.Parallel()

	tr, inner := hub()
	boom := errors.New("boom")
	inner.transErr[route{"it", "en"}] = boom

	_, err := tr.Translate(context.Background(), engine.Segment{Text: "ciao", Lang: "it"}, "de")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the first-leg failure", err)
	}
	if want := []route{{"it", "en"}}; !reflect.DeepEqual(inner.translated, want) {
		t.Errorf("legs = %v, want the second leg skipped after the first fails", inner.translated)
	}
}

func TestTranslateSecondLegError(t *testing.T) {
	t.Parallel()

	tr, inner := hub()
	boom := errors.New("boom")
	inner.transErr[route{"en", "de"}] = boom

	_, err := tr.Translate(context.Background(), engine.Segment{Text: "ciao", Lang: "it"}, "de")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the second-leg failure", err)
	}
	if want := []route{{"it", "en"}, {"en", "de"}}; !reflect.DeepEqual(inner.translated, want) {
		t.Errorf("legs = %v, want both legs attempted", inner.translated)
	}
}

func TestTranslatePivotKeepsSpan(t *testing.T) {
	t.Parallel()

	tr, inner := hub()
	span := [2]time.Duration{100 * time.Millisecond, 900 * time.Millisecond}
	_, err := tr.Translate(
		context.Background(), engine.Segment{Text: "ciao", Lang: "it", Span: span}, "de",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(inner.segs) != 2 {
		t.Fatalf("recorded %d legs, want 2", len(inner.segs))
	}
	hubLeg := inner.segs[1]
	if hubLeg.Lang != pivot.English {
		t.Errorf("hub leg language = %q, want %q", hubLeg.Lang, pivot.English)
	}
	if hubLeg.Span != span {
		t.Errorf("hub leg span = %v, want the source span %v preserved", hubLeg.Span, span)
	}
}

func TestWarmDirect(t *testing.T) {
	t.Parallel()

	tr, inner := hub()
	if err := tr.Warm(context.Background(), "it", "en"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []route{{"it", "en"}}; !reflect.DeepEqual(inner.warmed, want) {
		t.Errorf("warmed = %v, want single direct leg %v", inner.warmed, want)
	}
}

func TestWarmPivotWarmsBothLegs(t *testing.T) {
	t.Parallel()

	tr, inner := hub()
	if err := tr.Warm(context.Background(), "it", "de"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []route{{"it", "en"}, {"en", "de"}}; !reflect.DeepEqual(inner.warmed, want) {
		t.Errorf("warmed = %v, want both hub legs %v", inner.warmed, want)
	}
}

func TestWarmSameLanguageIsNoop(t *testing.T) {
	t.Parallel()

	tr, inner := hub()
	if err := tr.Warm(context.Background(), "it", "it"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(inner.warmed) != 0 {
		t.Errorf("warmed %d legs, want 0 for a same-language route", len(inner.warmed))
	}
}

func TestWarmFirstLegErrorSkipsSecond(t *testing.T) {
	t.Parallel()

	tr, inner := hub()
	boom := errors.New("boom")
	inner.warmErr[route{"it", "en"}] = boom

	if err := tr.Warm(context.Background(), "it", "de"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the first-leg failure", err)
	}
	if want := []route{{"it", "en"}}; !reflect.DeepEqual(inner.warmed, want) {
		t.Errorf("warmed = %v, want the second leg skipped", inner.warmed)
	}
}

func TestCloseForwards(t *testing.T) {
	t.Parallel()

	tr, inner := hub()
	if err := tr.Close(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inner.closes != 1 {
		t.Errorf("inner closed %d times, want 1", inner.closes)
	}
}
