package speech

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"testing"
)

func TestExtractArchiveMaterializesEntries(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archive := tarGz(t, []tarEntry{
		{name: "models", dir: true, mode: 0o755},
		{name: "models/stt/model.bin", body: []byte("weights"), mode: 0o644},
		{name: "bin/tool", body: []byte("#!"), mode: 0o755},
		{name: "lib/liba.dylib", body: []byte("a"), mode: 0o644},
		{name: "lib/liba.1.dylib", link: "liba.dylib"},
	})

	files, err := extractArchive(bytes.NewReader(archive), dir)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}

	want := []string{"models/stt/model.bin", "bin/tool", "lib/liba.dylib", "lib/liba.1.dylib"}
	slices.Sort(files)
	slices.Sort(want)
	if !slices.Equal(files, want) {
		t.Fatalf("files: %v, want %v", files, want)
	}

	weights, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "models", "stt", "model.bin")))
	if err != nil || string(weights) != "weights" {
		t.Fatalf("model content: %q, %v", weights, err)
	}
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(filepath.Join(dir, "bin", "tool"))
		if statErr != nil || info.Mode().Perm()&0o100 == 0 {
			t.Fatalf("execute intent lost: %v, %v", info, statErr)
		}
	}
	linked, err := os.Readlink(filepath.Join(dir, "lib", "liba.1.dylib"))
	if err != nil || linked != "liba.dylib" {
		t.Fatalf("symlink: %q, %v", linked, err)
	}
}

func TestExtractArchiveRejectsHostileEntries(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		entries []tarEntry
	}{
		{"absolute path", []tarEntry{{name: "/etc/passwd", body: []byte("x"), mode: 0o644}}},
		{"parent escape", []tarEntry{{name: "../outside", body: []byte("x"), mode: 0o644}}},
		{"sneaky escape", []tarEntry{{name: "a/../../outside", body: []byte("x"), mode: 0o644}}},
		{"absolute link target", []tarEntry{{name: "lib/evil", link: "/etc/passwd"}}},
		{"escaping link target", []tarEntry{{name: "lib/evil", link: "../../outside"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := extractArchive(bytes.NewReader(tarGz(t, tc.entries)), t.TempDir()); err == nil {
				t.Fatalf("%s must fail", tc.name)
			}
		})
	}
}

func TestExtractArchiveBoundsEntryCount(t *testing.T) {
	t.Parallel()

	entries := make([]tarEntry, maxArchiveEntries+1)
	for i := range entries {
		entries[i] = tarEntry{name: "models/many/" + strconv.Itoa(i), body: []byte("x"), mode: 0o644}
	}
	if _, err := extractArchive(bytes.NewReader(tarGz(t, entries)), t.TempDir()); err == nil {
		t.Fatal("entry bound must fail")
	}
}
