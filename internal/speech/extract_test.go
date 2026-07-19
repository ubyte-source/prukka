package speech

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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

	if err := extractArchive(bytes.NewReader(archive), dir); err != nil {
		t.Fatalf("extract: %v", err)
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

			if err := extractArchive(bytes.NewReader(tarGz(t, tc.entries)), t.TempDir()); err == nil {
				t.Fatalf("%s must fail", tc.name)
			}
		})
	}
}

// TestExtractArchiveContainsChainedSymlinkEscape: each hop is lexically legal
// against its OWN parent, so a per-entry textual check admits the chain while
// the kernel walks one directory further out at every hop and the payload
// lands outside the bundle. Containment must come from the syscall layer.
func TestExtractArchiveContainsChainedSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("archive symlinks need developer mode on windows")
	}
	t.Parallel()

	base := t.TempDir()
	dir := filepath.Join(base, "stage")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("stage: %v", err)
	}

	archive := tarGz(t, []tarEntry{
		{name: "d", dir: true, mode: 0o755},
		{name: "d/a", link: ".."},
		{name: "d/a/b", link: ".."},
		{name: "d/a/b/escaped.txt", body: []byte("PWNED"), mode: 0o644},
	})
	if err := extractArchive(bytes.NewReader(archive), dir); err == nil {
		t.Fatal("chained symlink escape must fail")
	}

	if _, err := os.Lstat(filepath.Join(base, "escaped.txt")); err == nil {
		t.Fatal("payload landed outside the extraction root")
	}
}

func TestExtractArchiveBoundsEntryCount(t *testing.T) {
	t.Parallel()

	entries := make([]tarEntry, maxArchiveEntries+1)
	for i := range entries {
		entries[i] = tarEntry{name: "models/many/" + strconv.Itoa(i), body: []byte("x"), mode: 0o644}
	}
	if err := extractArchive(bytes.NewReader(tarGz(t, entries)), t.TempDir()); err == nil {
		t.Fatal("entry bound must fail")
	}
}

// tarGzDeclaring renders one member whose header DECLARES size bytes with no
// body behind it: both byte caps read the declared size before any payload is
// copied, so a multi-gigabyte fixture costs nothing. The tar writer is
// deliberately left unclosed — closing it would demand the declared bytes.
func tarGzDeclaring(tb testing.TB, name string, size int64) []byte {
	tb.Helper()

	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	if err := tar.NewWriter(gz).WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: size}); err != nil {
		tb.Fatalf("write header %s: %v", name, err)
	}
	if err := gz.Close(); err != nil {
		tb.Fatalf("close gzip: %v", err)
	}

	return archive.Bytes()
}

// The two byte caps sit within four lines of each other on the same hostile
// path and would each catch the other's fixture, so both assert their own
// message: neither can be deleted behind the other's refusal.
func TestExtractArchiveBoundsTotalExpansion(t *testing.T) {
	t.Parallel()

	err := extractArchive(bytes.NewReader(tarGzDeclaring(t, "models/huge", maxArchiveTotal+1)), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "expands past") {
		t.Fatalf("total-expansion bound = %v, want a refusal naming the expansion", err)
	}
}

func TestExtractArchiveBoundsOneEntry(t *testing.T) {
	t.Parallel()

	// Over the per-file cap but under the total, so only the per-file guard
	// can be what refuses it.
	err := extractArchive(bytes.NewReader(tarGzDeclaring(t, "models/big", maxArchiveFile+1)), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("per-file bound = %v, want a refusal naming the entry", err)
	}
}

// FuzzExtractArchive feeds arbitrary bytes to the extractor — the path that
// produced four symlink-escape vectors across this project's hardening. Two
// invariants survive ANY input, accepted or refused: nothing materializes
// outside the extraction root, and the regular files that did land never sum
// past maxArchiveTotal. The root sits two directories beneath the walked
// base, so an escape of up to two upward hops lands where the walk sees it.
func FuzzExtractArchive(f *testing.F) {
	f.Add(tarGz(f, []tarEntry{
		{name: "models", dir: true, mode: 0o755},
		{name: "models/stt/model.bin", body: []byte("weights"), mode: 0o644},
		{name: "bin/tool", body: []byte("#!"), mode: 0o755},
		{name: "lib/liba.dylib", body: []byte("a"), mode: 0o644},
		{name: "lib/liba.1.dylib", link: "liba.dylib"},
	}))
	f.Add(tarGz(f, []tarEntry{
		{name: "d", dir: true, mode: 0o755},
		{name: "d/a", link: parentRef},
		{name: "d/a/b", link: parentRef},
		{name: "d/a/b/escaped.txt", body: []byte("PWNED"), mode: 0o644},
	}))
	f.Add(tarGz(f, []tarEntry{{name: "/etc/passwd", body: []byte("x"), mode: 0o644}}))
	f.Add(tarGz(f, []tarEntry{{name: "a/../../outside", body: []byte("x"), mode: 0o644}}))
	f.Add(tarGz(f, []tarEntry{{name: "lib/evil", link: "/etc/passwd"}}))
	f.Add(tarGz(f, []tarEntry{{name: "lib/evil", link: "../../outside"}}))
	f.Add(tarGzDeclaring(f, "models/huge", maxArchiveTotal+1))
	f.Add(tarGzDeclaring(f, "models/big", maxArchiveFile+1))
	f.Add([]byte("not a gzip stream"))

	f.Fuzz(func(t *testing.T, data []byte) {
		base := t.TempDir()
		dir := filepath.Join(base, "outer", "stage")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("stage: %v", err)
		}

		extractErr := extractArchive(bytes.NewReader(data), dir)
		assertExtractionContained(t, base, dir, extractErr)
	})
}

// assertExtractionContained walks base and fails on anything the archive
// placed outside dir; only base itself and the one scaffold directory
// between them may exist out there. It also re-checks the expansion cap on
// what DID land: the extractor's caps read declared header sizes, and only
// the on-disk sum proves they bound actual bytes.
func assertExtractionContained(t *testing.T, base, dir string, extractErr error) {
	t.Helper()

	var total int64
	walk := func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		if rel == parentRef || strings.HasPrefix(rel, parentRef+string(os.PathSeparator)) {
			assertScaffoldOnly(t, base, dir, path, extractErr)

			return nil
		}
		size, sizeErr := regularSize(d)
		if sizeErr != nil {
			return sizeErr
		}
		total += size

		return nil
	}
	if err := filepath.WalkDir(base, walk); err != nil {
		t.Fatalf("walk extraction root: %v", err)
	}
	if total > maxArchiveTotal {
		t.Fatalf("extraction wrote %d bytes past the %d cap (extract err: %v)", total, int64(maxArchiveTotal), extractErr)
	}
}

// assertScaffoldOnly admits exactly the two directories that predate the
// extraction between base and dir; anything else out there escaped.
func assertScaffoldOnly(t *testing.T, base, dir, path string, extractErr error) {
	t.Helper()

	if path != base && path != filepath.Dir(dir) {
		t.Fatalf("entry escaped the extraction root (extract err: %v): %s", extractErr, path)
	}
}

// regularSize reports a regular file's on-disk size and zero for every
// other entry type.
func regularSize(d os.DirEntry) (int64, error) {
	if !d.Type().IsRegular() {
		return 0, nil
	}
	info, err := d.Info()
	if err != nil {
		return 0, err
	}

	return info.Size(), nil
}
