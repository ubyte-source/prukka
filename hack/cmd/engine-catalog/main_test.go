package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/speech"
)

const validSTTMeta = `{"id":"stt-core","kind":"stt",` +
	`"license":"MIT (OpenAI Whisper models via ggerganov/whisper.cpp)"}`

func validFixture() map[string]string {
	return map[string]string{
		"prukka-engine-runtime_darwin_amd64.tar.gz": "runtime-amd64",
		"prukka-engine-runtime_darwin_arm64.tar.gz": "runtime-arm64",
		"prukka-engine-pack_stt-core.tar.gz":        "stt-models",
		"stt-core.meta.json":                        validSTTMeta,
		"prukka-engine-pack_mt-it-en.tar.gz":        "mt-models",
		"mt-it-en.meta.json":                        `{"id":"mt-it-en","kind":"mt","from":"it","to":"en"}`,
		"prukka-engine-pack_voice-it.tar.gz":        "voice-model",
		"voice-it.meta.json": `{"id":"voice-it","kind":"voice","lang":"it",` +
			`"voice":"models/tts/it_IT-paola-medium.onnx","license":"see bundled MODEL_CARD"}`,
	}
}

func TestRunEmitsTheExactCatalogContract(t *testing.T) {
	t.Parallel()
	dir := writeFixture(t, validFixture())
	output := filepath.Join(t.TempDir(), "catalog.json")

	// The trailing slash must not double up in asset URLs.
	err := run(options{dir: dir, baseURL: "https://example.test/engine/", protocol: 2, output: output})
	if err != nil {
		t.Fatal(err)
	}

	want := fmt.Sprintf(`{
  "schema": "prukka.engine.catalog",
  "runtimes": [
    {
      "os": "darwin",
      "arch": "amd64",
      "url": "https://example.test/engine/prukka-engine-runtime_darwin_amd64.tar.gz",
      "sha256": "%s",
      "size": 13
    },
    {
      "os": "darwin",
      "arch": "arm64",
      "url": "https://example.test/engine/prukka-engine-runtime_darwin_arm64.tar.gz",
      "sha256": "%s",
      "size": 13
    }
  ],
  "packs": [
    {
      "id": "mt-it-en",
      "kind": "mt",
      "from": "it",
      "to": "en",
      "url": "https://example.test/engine/prukka-engine-pack_mt-it-en.tar.gz",
      "sha256": "%s",
      "size": 9
    },
    {
      "id": "stt-core",
      "kind": "stt",
      "url": "https://example.test/engine/prukka-engine-pack_stt-core.tar.gz",
      "sha256": "%s",
      "license": "MIT (OpenAI Whisper models via ggerganov/whisper.cpp)",
      "size": 10
    },
    {
      "id": "voice-it",
      "kind": "voice",
      "lang": "it",
      "voice": "models/tts/it_IT-paola-medium.onnx",
      "url": "https://example.test/engine/prukka-engine-pack_voice-it.tar.gz",
      "sha256": "%s",
      "license": "see bundled MODEL_CARD",
      "size": 11
    }
  ],
  "version": 1,
  "protocol": 2
}
`,
		sha256Hex("runtime-amd64"), sha256Hex("runtime-arm64"),
		sha256Hex("mt-models"), sha256Hex("stt-models"), sha256Hex("voice-model"))
	if got := string(readOutput(t, output)); got != want {
		t.Fatalf("catalog mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestRunIsDeterministic(t *testing.T) {
	t.Parallel()
	dir := writeFixture(t, validFixture())
	outputDir := t.TempDir()
	first := filepath.Join(outputDir, "first.json")
	second := filepath.Join(outputDir, "second.json")

	opts := options{dir: dir, baseURL: "https://example.test/engine", protocol: 2}
	opts.output = first
	if err := run(opts); err != nil {
		t.Fatal(err)
	}
	opts.output = second
	if err := run(opts); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readOutput(t, first), readOutput(t, second)) {
		t.Fatal("catalog changed between identical runs")
	}
}

func TestRunRejectsBrokenArtifactLayouts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(files map[string]string)
		wantErr string
	}{
		{
			name:    "unknown file",
			mutate:  func(files map[string]string) { files["README.md"] = "typo" },
			wantErr: `unexpected file "README.md"`,
		},
		{
			name:    "pack without metadata",
			mutate:  func(files map[string]string) { delete(files, "voice-it.meta.json") },
			wantErr: "has no voice-it.meta.json",
		},
		{
			name:    "metadata without pack",
			mutate:  func(files map[string]string) { delete(files, "prukka-engine-pack_voice-it.tar.gz") },
			wantErr: "has no prukka-engine-pack_voice-it.tar.gz archive",
		},
		{
			name: "no runtime archives",
			mutate: func(files map[string]string) {
				delete(files, "prukka-engine-runtime_darwin_amd64.tar.gz")
				delete(files, "prukka-engine-runtime_darwin_arm64.tar.gz")
			},
			wantErr: "no prukka-engine-runtime_<os>_<arch>.tar.gz archives found",
		},
		{
			name: "no pack archives",
			mutate: func(files map[string]string) {
				for name := range files {
					if strings.Contains(name, "pack") || strings.Contains(name, "meta") {
						delete(files, name)
					}
				}
			},
			wantErr: "no prukka-engine-pack_<id>.tar.gz archives found",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			files := validFixture()
			test.mutate(files)
			assertRunFails(t, files, test.wantErr)
		})
	}
}

func TestRunRejectsDirectoriesInTheArtifactDirectory(t *testing.T) {
	t.Parallel()
	dir := writeFixture(t, validFixture())
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o750); err != nil {
		t.Fatal(err)
	}

	output := filepath.Join(t.TempDir(), "catalog.json")
	err := run(options{dir: dir, baseURL: "https://example.test", protocol: 2, output: output})
	if err == nil || !strings.Contains(err.Error(), `unexpected directory "nested"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunRejectsInvalidMetadata(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		meta    string
		wantErr string
	}{
		{
			name:    "id mismatch",
			meta:    `{"id":"voice-en","kind":"voice","lang":"it","voice":"models/tts/v.onnx"}`,
			wantErr: `id "voice-en" does not match the file name`,
		},
		{
			name:    "unknown kind",
			meta:    `{"id":"voice-it","kind":"tts","lang":"it","voice":"models/tts/v.onnx"}`,
			wantErr: `unknown pack kind "tts"`,
		},
		{
			name:    "voice without lang",
			meta:    `{"id":"voice-it","kind":"voice","voice":"models/tts/v.onnx"}`,
			wantErr: "voice packs need a lang value",
		},
		{
			name:    "voice with mt fields",
			meta:    `{"id":"voice-it","kind":"voice","lang":"it","voice":"models/tts/v.onnx","from":"it"}`,
			wantErr: "voice packs must not set from",
		},
		{
			name:    "mt fields on an stt pack",
			meta:    `{"id":"voice-it","kind":"stt","from":"it"}`,
			wantErr: "stt packs must not set from",
		},
		{
			name:    "mt without directions",
			meta:    `{"id":"voice-it","kind":"mt"}`,
			wantErr: "mt packs need a from value",
		},
		{
			name:    "unknown field",
			meta:    `{"id":"voice-it","kind":"voice","lang":"it","voice":"models/tts/v.onnx","extra":1}`,
			wantErr: `unknown field "extra"`,
		},
		{
			name:    "trailing data",
			meta:    `{"id":"voice-it","kind":"voice","lang":"it","voice":"models/tts/v.onnx"}{}`,
			wantErr: "trailing data after the JSON document",
		},
		{
			name:    "invalid JSON",
			meta:    "not json",
			wantErr: `parse "voice-it.meta.json"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			files := validFixture()
			files["voice-it.meta.json"] = test.meta
			assertRunFails(t, files, test.wantErr)
		})
	}
}

func TestRunRequiresCompleteOptions(t *testing.T) {
	t.Parallel()
	complete := options{dir: "artifacts", baseURL: "https://example.test", protocol: 2, output: "catalog.json"}
	tests := []struct {
		mutate func(opts *options)
		name   string
	}{
		{name: "missing dir", mutate: func(opts *options) { opts.dir = "" }},
		{name: "missing base-url", mutate: func(opts *options) { opts.baseURL = "" }},
		{name: "missing output", mutate: func(opts *options) { opts.output = "" }},
		{name: "missing protocol", mutate: func(opts *options) { opts.protocol = 0 }},
		{name: "negative protocol", mutate: func(opts *options) { opts.protocol = -2 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			opts := complete
			test.mutate(&opts)
			err := run(opts)
			if err == nil || !strings.Contains(err.Error(), "are required") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func assertRunFails(t *testing.T, files map[string]string, wantErr string) {
	t.Helper()
	dir := writeFixture(t, files)
	output := filepath.Join(t.TempDir(), "catalog.json")
	err := run(options{dir: dir, baseURL: "https://example.test", protocol: 2, output: output})
	if err == nil || !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("err = %v, want it to contain %q", err, wantErr)
	}
	if _, statErr := os.Stat(output); statErr == nil {
		t.Fatal("a failing run must not write a catalog")
	}
}

func writeFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func readOutput(t *testing.T, path string) []byte {
	t.Helper()
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	data, err := root.ReadFile(filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func sha256Hex(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// TestGeneratedCatalogSatisfiesTheDaemonParser locks the producer-consumer
// contract: whatever this tool emits must parse under the daemon's strict
// catalog validation in internal/speech.
func TestGeneratedCatalogSatisfiesTheDaemonParser(t *testing.T) {
	t.Parallel()
	dir := writeFixture(t, validFixture())
	output := filepath.Join(t.TempDir(), "catalog.json")

	err := run(options{dir: dir, baseURL: "https://example.test/engine", protocol: 2, output: output})
	if err != nil {
		t.Fatal(err)
	}

	rendered, err := os.Open(filepath.Clean(output))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := rendered.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}()

	catalog, err := speech.ParseCatalog(rendered)
	if err != nil {
		t.Fatalf("daemon parser rejected the generated catalog: %v", err)
	}
	if _, err := catalog.RuntimeFor("darwin", "amd64"); err != nil {
		t.Fatalf("runtime lookup: %v", err)
	}
	if _, err := catalog.PackByID(speech.PackIDSTTCore); err != nil {
		t.Fatalf("pack lookup: %v", err)
	}
}
