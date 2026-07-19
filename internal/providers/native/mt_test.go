package native

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/engine"
)

// translateOne renders one source text from the fake language to a target.
func translateOne(t *testing.T, mt *MT, text string, to core.Lang) string {
	t.Helper()

	got, err := mt.Translate(t.Context(), engine.Segment{Text: text, Lang: fakeLang}, to)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	return got
}

func newTestMT(t *testing.T, cfg *MTConfig) *MT {
	t.Helper()

	mt := NewMT(cfg)
	t.Cleanup(func() {
		if err := mt.Close(); err != nil {
			t.Errorf("close test translator: %v", err)
		}
	})

	return mt
}

func TestMTTranslates(t *testing.T) {
	t.Parallel()

	mt := newTestMT(t, &MTConfig{Bin: os.Args[0]})

	if got := translateOne(t, mt, "ciao", core.Lang("en")); got != "mt:ciao" {
		t.Fatalf("translation = %q, want %q", got, "mt:ciao")
	}
}

func TestMTEnforcesDeclaredPairs(t *testing.T) {
	t.Parallel()

	mt := newTestMT(t, &MTConfig{Bin: filepath.Join(t.TempDir(), "missing"), Pairs: []engine.LanguagePair{
		{From: "it", To: "en"},
	}})
	if !mt.Supports("it-IT", "en-US") || mt.Supports("en", "it") {
		t.Fatal("Supports did not apply the directed base-language capability")
	}
	_, err := mt.Translate(t.Context(), engine.Segment{Text: "hello", Lang: "en"}, "it")
	if err == nil || !strings.Contains(err.Error(), "model unavailable for en to it") {
		t.Fatalf("unsupported Translate error = %v", err)
	}
}

func TestDecodeMTResponseRequiresTextField(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		line    string
		want    string
		wantErr bool
	}{
		{name: "text", line: `{"text":"hello"}`, want: "hello"},
		{name: "empty text", line: `{"text":""}`},
		{name: "missing", line: `{}`, wantErr: true},
		{name: "null text", line: `{"text":null}`, wantErr: true},
		{name: "wrong type", line: `{"text":3}`, wantErr: true},
		{name: "null response", line: `null`, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := decodeMTResponse([]byte(test.line))
			if (err != nil) != test.wantErr {
				t.Fatalf("decodeMTResponse(%s) error = %v, wantErr %v", test.line, err, test.wantErr)
			}
			if err == nil && got.Text != test.want {
				t.Fatalf("decodeMTResponse(%s) text = %q, want %q", test.line, got.Text, test.want)
			}
		})
	}
}
