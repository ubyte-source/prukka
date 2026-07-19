package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestCreateArchiveIsDeterministic(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "payload", "b.txt"), "b", 0o600)
	writeTestFile(t, filepath.Join(root, "payload", "a.txt"), "a", 0o700)

	outputDir := t.TempDir()
	first := filepath.Join(outputDir, "first.tar.gz")
	second := filepath.Join(outputDir, "second.tar.gz")
	if err := createArchive(options{output: first, root: root, paths: []string{"payload"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(root, "payload", "a.txt"), time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := createArchive(options{output: second, root: root, paths: []string{"payload"}}); err != nil {
		t.Fatal(err)
	}

	want := readTestFile(t, outputDir, "first.tar.gz")
	got := readTestFile(t, outputDir, "second.tar.gz")
	if !bytes.Equal(got, want) {
		t.Fatal("archive changed with filesystem timestamps")
	}

	assertNormalizedEntries(t, archiveEntries(t, got))
}

func assertNormalizedEntries(t *testing.T, entries []*tar.Header) {
	t.Helper()
	wantNames := []string{"payload/", "payload/a.txt", "payload/b.txt"}
	if len(entries) != len(wantNames) {
		t.Fatalf("entries = %v", entries)
	}
	// Modes are canonical — 0755 with the execute intent, 0644 without —
	// so archives do not depend on the producing machine's umask. Windows
	// has no execute bit, so the exec intent is unreachable there.
	wantExec := int64(0o755)
	if runtime.GOOS == "windows" {
		wantExec = 0o644
	}
	wantModes := map[string]int64{"payload/": 0o755, "payload/a.txt": wantExec, "payload/b.txt": 0o644}
	for i, name := range wantNames {
		if entries[i].Name != name {
			t.Fatalf("entry %d = %q, want %q", i, entries[i].Name, name)
		}
		if !entries[i].ModTime.Equal(time.Unix(0, 0)) || entries[i].Uid != 0 || entries[i].Gid != 0 {
			t.Fatalf("entry %q has host metadata: %+v", name, entries[i])
		}
		if entries[i].Mode != wantModes[name] {
			t.Fatalf("entry %q mode = %o, want %o", name, entries[i].Mode, wantModes[name])
		}
	}
}

func TestCreateArchiveRejectsUnsafeAndDuplicatePaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "payload", "file"), "data", 0o644)

	tests := []struct {
		name  string
		paths []string
	}{
		{name: "parent", paths: []string{"../payload"}},
		{name: "absolute", paths: []string{filepath.Join(root, "payload")}},
		{name: "duplicate", paths: []string{"payload", "payload/file"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			output := filepath.Join(t.TempDir(), "archive.tar.gz")
			if err := createArchive(options{output: output, root: root, paths: test.paths}); err == nil {
				t.Fatal("createArchive succeeded")
			}
		})
	}
}

func TestCreateArchivePreservesSymlinkWithoutFollowingIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "target"), "data", 0o644)
	if err := os.Symlink("target", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	output := filepath.Join(t.TempDir(), "archive.tar.gz")
	if err := createArchive(options{output: output, root: root, paths: []string{"link"}}); err != nil {
		t.Fatal(err)
	}
	data := readTestFile(t, filepath.Dir(output), filepath.Base(output))
	entries := archiveEntries(t, data)
	if len(entries) != 1 || entries[0].Typeflag != tar.TypeSymlink || entries[0].Linkname != "target" {
		t.Fatalf("entries = %+v", entries)
	}
}

func writeTestFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, root, name string) []byte {
	t.Helper()
	dir, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := dir.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	data, err := dir.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func archiveEntries(t *testing.T, data []byte) []*tar.Header {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := gz.Close(); err != nil {
			t.Error(err)
		}
	}()

	tr := tar.NewReader(gz)
	var entries []*tar.Header
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return entries
		}
		if err != nil {
			t.Fatal(err)
		}
		copyHeader := *header
		entries = append(entries, &copyHeader)
	}
}
