package speech

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// catalogFixture is a mutable, typed valid document: mutations reach the
// runtime and pack maps without type assertions.
type catalogFixture struct {
	doc      map[string]any
	runtimes []map[string]any
	packs    []map[string]any
}

// validCatalogFixture is a hand-rolled minimal valid document.
func validCatalogFixture() *catalogFixture {
	runtimes := []map[string]any{{
		"os": "darwin", "arch": "arm64",
		"url":    "https://example.com/runtime.tar.gz",
		"sha256": strings.Repeat("ab", 32),
		"size":   1024,
	}}
	packs := []map[string]any{
		{
			"id": "stt-core", "kind": "stt",
			"url": "https://example.com/stt.tar.gz", "sha256": strings.Repeat("cd", 32), "size": 2048,
		},
		{
			"id": "mt-it-en", "kind": "mt", "from": "it", "to": "en",
			"url": "https://example.com/mt.tar.gz", "sha256": strings.Repeat("ef", 32), "size": 4096,
		},
		{
			"id": "voice-it", "kind": "voice", "lang": "it", "voice": "models/tts/it_IT-paola-medium.onnx",
			"url": "https://example.com/v.tar.gz", "sha256": strings.Repeat("01", 32), "size": 8192,
		},
	}
	doc := map[string]any{
		"schema":   "prukka.engine.catalog",
		"version":  1,
		"protocol": 2,
		"runtimes": runtimes,
		"packs":    packs,
	}

	return &catalogFixture{doc: doc, runtimes: runtimes, packs: packs}
}

func parseDoc(t *testing.T, doc map[string]any) (*Catalog, error) {
	t.Helper()

	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	return ParseCatalog(bytes.NewReader(raw))
}

func TestParseCatalogAcceptsValidDocument(t *testing.T) {
	t.Parallel()

	c, err := parseDoc(t, validCatalogFixture().doc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(c.Runtimes) != 1 || len(c.Packs) != 3 {
		t.Fatalf("unexpected shape: %+v", c)
	}
}

func TestCatalogLookupsSelectEntries(t *testing.T) {
	t.Parallel()

	c, err := parseDoc(t, validCatalogFixture().doc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	rt, err := c.RuntimeFor("darwin", "arm64")
	if err != nil || rt.Size != 1024 {
		t.Fatalf("runtime lookup: %+v, %v", rt, err)
	}
	if _, missingErr := c.RuntimeFor("linux", "amd64"); missingErr == nil {
		t.Fatal("missing platform must fail")
	}

	pack, err := c.PackByID("mt-it-en")
	if err != nil || pack.From != "it" || pack.To != "en" {
		t.Fatalf("pack lookup: %+v, %v", pack, err)
	}
	if _, missingErr := c.PackByID("nope"); missingErr == nil {
		t.Fatal("unknown pack must fail")
	}
}

func TestParseCatalogRejectsInvalidDocuments(t *testing.T) {
	t.Parallel()

	cases := []struct {
		mutate func(f *catalogFixture)
		name   string
	}{
		{name: "wrong schema", mutate: func(f *catalogFixture) { f.doc["schema"] = "other" }},
		{name: "wrong version", mutate: func(f *catalogFixture) { f.doc["version"] = 2 }},
		{name: "wrong protocol", mutate: func(f *catalogFixture) { f.doc["protocol"] = 1 }},
		{name: "no runtimes", mutate: func(f *catalogFixture) { f.doc["runtimes"] = []map[string]any{} }},
		{name: "duplicate runtime", mutate: func(f *catalogFixture) {
			f.doc["runtimes"] = append(f.runtimes, f.runtimes[0])
		}},
		{name: "http url", mutate: func(f *catalogFixture) { f.runtimes[0]["url"] = "http://example.com/x" }},
		{name: "bad sha", mutate: func(f *catalogFixture) { f.runtimes[0]["sha256"] = "zz" }},
		{name: "zero size", mutate: func(f *catalogFixture) { f.runtimes[0]["size"] = 0 }},
		{name: "missing stt-core", mutate: func(f *catalogFixture) { f.doc["packs"] = f.packs[1:] }},
		{name: "duplicate pack", mutate: func(f *catalogFixture) { f.doc["packs"] = append(f.packs, f.packs[0]) }},
		{name: "mt id mismatch", mutate: func(f *catalogFixture) { f.packs[1]["id"] = "mt-en-it" }},
		{name: "mt self route", mutate: func(f *catalogFixture) {
			f.packs[1]["to"] = "it"
			f.packs[1]["id"] = "mt-it-it"
		}},
		{name: "mt regional language", mutate: func(f *catalogFixture) {
			f.packs[1]["from"] = "it-IT"
			f.packs[1]["id"] = "mt-it-IT-en"
		}},
		{name: "mt auto language", mutate: func(f *catalogFixture) {
			f.packs[1]["from"] = "auto"
			f.packs[1]["id"] = "mt-auto-en"
		}},
		{name: "voice path escape", mutate: func(f *catalogFixture) {
			f.packs[2]["voice"] = "models/tts/../../prukka"
		}},
		{name: "voice with route fields", mutate: func(f *catalogFixture) { f.packs[2]["from"] = "it" }},
		{name: "stt with voice fields", mutate: func(f *catalogFixture) { f.packs[0]["lang"] = "it" }},
		{name: "unknown pack kind", mutate: func(f *catalogFixture) { f.packs[0]["kind"] = "extra" }},
		{name: "unknown field", mutate: func(f *catalogFixture) { f.doc["surprise"] = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := validCatalogFixture()
			tc.mutate(fixture)
			if _, err := parseDoc(t, fixture.doc); err == nil {
				t.Fatalf("%s: parse must fail", tc.name)
			}
		})
	}
}

func TestParseCatalogRejectsMalformedStreams(t *testing.T) {
	t.Parallel()

	if _, err := ParseCatalog(strings.NewReader("{} trailing")); err == nil {
		t.Fatal("trailing data must fail")
	}
	if _, err := ParseCatalog(strings.NewReader(strings.Repeat(" ", catalogMaxBytes+2))); err == nil {
		t.Fatal("oversized document must fail")
	}
}

func TestPackIDHelpersAreCanonical(t *testing.T) {
	t.Parallel()

	if got := MTPackID("it", "en"); got != "mt-it-en" {
		t.Fatalf("MTPackID: %s", got)
	}
	if got := VoicePackID("it"); got != "voice-it" {
		t.Fatalf("VoicePackID: %s", got)
	}
}
