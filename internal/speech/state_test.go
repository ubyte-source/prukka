package speech

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func sampleState() *State {
	return &State{
		Schema: stateSchema, Version: stateVersion, Protocol: SupportedProtocol,
		Runtime: InstalledRun{OS: "darwin", Arch: "arm64", SHA256: sha256Hex([]byte("rt"))},
		Packs: []InstalledPack{{
			ID: PackIDSTTCore, Kind: PackSTT, SHA256: sha256Hex([]byte("stt")),
			Files: []string{"models/stt/a.bin"},
		}},
	}
}

func TestStateRoundTrips(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), stateName)
	if err := writeState(path, sampleState()); err != nil {
		t.Fatalf("write: %v", err)
	}

	loaded, err := readState(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if loaded.Runtime.SHA256 != sha256Hex([]byte("rt")) || len(loaded.Packs) != 1 {
		t.Fatalf("round trip lost data: %+v", loaded)
	}
}

func TestReadStateRejectsForeignDocuments(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
	}{
		{"wrong schema", `{"schema":"x","version":1,"protocol":2,"runtime":{},"packs":[]}`},
		{"wrong version", `{"schema":"prukka.engine.state","version":9,"protocol":2,"runtime":{},"packs":[]}`},
		{"wrong protocol", `{"schema":"prukka.engine.state","version":1,"protocol":1,"runtime":{},"packs":[]}`},
		{"unknown field", `{"schema":"prukka.engine.state","version":1,"protocol":2,"runtime":{},"packs":[],"x":1}`},
		{"trailing data", `{"schema":"prukka.engine.state","version":1,"protocol":2,"runtime":{},"packs":[]} {}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), stateName)
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatalf("plant: %v", err)
			}
			if _, err := readState(path); err == nil {
				t.Fatalf("%s must fail", tc.name)
			}
		})
	}
}

// TestReadStateRejectsEscapingPackFileNames: the pack-file rule lives on
// both sides of the inventory. Staging refuses what an archive may not
// publish; the load must refuse what a persisted list may not NAME, or a
// state file edited between runs re-admits exactly what staging rejected.
func TestReadStateRejectsEscapingPackFileNames(t *testing.T) {
	t.Parallel()

	s := sampleState()
	s.Packs[0].Files = []string{"../escape"}
	path := filepath.Join(t.TempDir(), stateName)
	if err := writeState(path, s); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := readState(path)
	if err == nil || !strings.Contains(err.Error(), "../escape") {
		t.Fatalf("readState = %v, want the escaping entry named", err)
	}
}

// TestReadStateRejectsSymlinkedPackFiles: staging rejects a symlink in a
// pack, so the inventory must never name one either — a link where a model
// belongs is not a file any install produced, whatever it resolves to.
func TestReadStateRejectsSymlinkedPackFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("planting symlinks needs developer mode on windows")
	}
	t.Parallel()

	dir := t.TempDir()
	models := filepath.Join(dir, bundleDirName, modelsDirName, "stt")
	if err := os.MkdirAll(models, 0o700); err != nil {
		t.Fatalf("stage models: %v", err)
	}
	if err := os.WriteFile(filepath.Join(models, "real.bin"), []byte("model"), 0o600); err != nil {
		t.Fatalf("stage model: %v", err)
	}
	if err := os.Symlink("real.bin", filepath.Join(models, "a.bin")); err != nil {
		t.Fatalf("plant alias: %v", err)
	}
	path := filepath.Join(dir, stateName)
	if err := writeState(path, sampleState()); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := readState(path)
	if err == nil || !strings.Contains(err.Error(), "models/stt/a.bin") {
		t.Fatalf("readState = %v, want the symlinked entry named", err)
	}
}

func TestStatePackBookkeeping(t *testing.T) {
	t.Parallel()

	s := sampleState()
	s.upsertPack(&InstalledPack{ID: "voice-it", Kind: PackVoice, Lang: "it"})
	s.upsertPack(&InstalledPack{ID: "mt-it-en", Kind: PackMT, From: "it", To: "en"})
	if len(s.Packs) != 3 || s.Packs[0].ID != "mt-it-en" || s.Packs[2].ID != "voice-it" {
		t.Fatalf("packs not sorted: %+v", s.Packs)
	}

	s.upsertPack(&InstalledPack{ID: "voice-it", Kind: PackVoice, Lang: "it", SHA256: "updated"})
	if len(s.Packs) != 3 {
		t.Fatalf("upsert duplicated: %+v", s.Packs)
	}
	if pack, ok := s.Pack("voice-it"); !ok || pack.SHA256 != "updated" {
		t.Fatalf("upsert lost update: %+v", pack)
	}

	s.dropPack("voice-it")
	if _, ok := s.Pack("voice-it"); ok {
		t.Fatal("drop kept the pack")
	}
	if _, ok := s.Pack(PackIDSTTCore); !ok {
		t.Fatal("drop removed an unrelated pack")
	}
}
