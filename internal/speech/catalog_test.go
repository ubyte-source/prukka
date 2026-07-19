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

// FuzzParseCatalog feeds arbitrary bytes to the catalog parser — the one
// download trusted by transport alone. It must never panic; a refusal must
// never echo the document wholesale, because catalog URLs may carry userinfo
// or a presigned query; and a document it accepts must hold every invariant
// the installer then trusts without re-checking.
func FuzzParseCatalog(f *testing.F) {
	valid, err := json.Marshal(validCatalogFixture().doc)
	if err != nil {
		f.Fatalf("marshal fixture: %v", err)
	}
	f.Add(valid)

	// A credentialed plain-http runtime URL: the refusal must redact it.
	leaky := validCatalogFixture()
	leaky.runtimes[0]["url"] = "http://mirror-user:s3cr3tpass@example.com/x?token=hush"
	raw, err := json.Marshal(leaky.doc)
	if err != nil {
		f.Fatalf("marshal leaky fixture: %v", err)
	}
	f.Add(raw)

	f.Add([]byte(`{}`))
	f.Add([]byte(`{"schema":"prukka.engine.catalog","version":1,"protocol":2}`))
	f.Add([]byte(`not json`))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		c, parseErr := ParseCatalog(bytes.NewReader(data))
		if parseErr != nil {
			// Long enough that a verbatim reappearance cannot be the one
			// character encoding/json legitimately names in a syntax error.
			if len(data) >= 16 && strings.Contains(parseErr.Error(), string(data)) {
				t.Fatalf("refusal echoes the whole document: %v", parseErr)
			}

			return
		}
		assertAcceptedCatalog(t, c)
	})
}

// assertAcceptedCatalog re-derives the validation invariants on an accepted
// document: the pinned wire contract, at least one runtime, unique platforms
// and pack ids, the mandatory stt-core pack, and a well-formed artifact
// behind every entry.
func assertAcceptedCatalog(t *testing.T, c *Catalog) {
	t.Helper()

	if c.Schema != CatalogSchema || c.Version != CatalogVersion || c.Protocol != SupportedProtocol {
		t.Fatalf("accepted catalog is off-contract: %q version %d protocol %d", c.Schema, c.Version, c.Protocol)
	}
	if len(c.Runtimes) == 0 {
		t.Fatal("accepted catalog lists no runtimes")
	}
	platforms := make(map[string]bool, len(c.Runtimes))
	for i := range c.Runtimes {
		r := &c.Runtimes[i]
		if r.OS == "" || r.Arch == "" || platforms[r.OS+"/"+r.Arch] {
			t.Fatalf("accepted runtime %d has platform %q/%q", i, r.OS, r.Arch)
		}
		platforms[r.OS+"/"+r.Arch] = true
		assertAcceptedArtifact(t, r.URL, r.SHA256, r.Size)
	}
	assertAcceptedPacks(t, c)
}

func assertAcceptedPacks(t *testing.T, c *Catalog) {
	t.Helper()

	ids := make(map[string]bool, len(c.Packs))
	for i := range c.Packs {
		p := &c.Packs[i]
		if ids[p.ID] {
			t.Fatalf("accepted pack %q twice", p.ID)
		}
		ids[p.ID] = true
		assertAcceptedPackShape(t, p)
		assertAcceptedArtifact(t, p.URL, p.SHA256, p.Size)
	}
	if !ids[PackIDSTTCore] {
		t.Fatalf("accepted catalog misses the mandatory %q pack", PackIDSTTCore)
	}
}

func assertAcceptedPackShape(t *testing.T, p *Pack) {
	t.Helper()

	switch p.Kind {
	case PackSTT:
		// The single mandatory pack; its id is pinned by validatePacks.
	case PackMT:
		if p.From == "" || p.To == "" || p.From == p.To {
			t.Fatalf("accepted mt pack %q routes %q to %q", p.ID, p.From, p.To)
		}
	case PackVoice:
		if !voiceModelPath.MatchString(p.Voice) {
			t.Fatalf("accepted voice pack %q points at %q", p.ID, p.Voice)
		}
	default:
		t.Fatalf("accepted pack %q has kind %q", p.ID, p.Kind)
	}
}

func assertAcceptedArtifact(t *testing.T, rawURL, sha string, size int64) {
	t.Helper()

	if err := requireHTTPSOrLoopback(rawURL); err != nil {
		t.Fatalf("accepted artifact URL fails the transport rule: %v", err)
	}
	if !hexSHA256.MatchString(sha) {
		t.Fatalf("accepted artifact sha256 %q is not 64 hex digits", sha)
	}
	if size <= 0 || size > maxArtifactBytes {
		t.Fatalf("accepted artifact size %d is outside (0, %d]", size, int64(maxArtifactBytes))
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
