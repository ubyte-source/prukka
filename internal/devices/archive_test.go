package devices

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// testArchive builds a gzip'd tar holding <name>/bin with the given
// body and the executable bit set.
// writeArchive owns the gzip+tar scaffold: write receives the tar writer,
// the builder closes both layers and returns the bytes.
func writeArchive(t *testing.T, write func(tw *tar.Writer)) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	write(tw)
	if err := errors.Join(tw.Close(), gz.Close()); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	return buf.Bytes()
}

func testArchive(t *testing.T, name, body string) []byte {
	t.Helper()

	return writeArchive(t, func(tw *tar.Writer) {
		if err := tw.WriteHeader(&tar.Header{Name: name + "/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
			t.Fatalf("dir header: %v", err)
		}

		header := &tar.Header{
			Name: name + "/bin", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body)),
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatalf("file header: %v", err)
		}

		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("file body: %v", err)
		}
	})
}

// TestExtractPreservesTreeAndModes: the bundle lands intact with its
// executable bit.
func TestExtractPreservesTreeAndModes(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := extract(testArchive(t, "Bundle.driver", "#!/bin/sh\n"), dir); err != nil {
		t.Fatalf("extract: %v", err)
	}

	info, statErr := os.Stat(filepath.Join(dir, "Bundle.driver", "bin"))
	if statErr != nil {
		t.Fatalf("extracted file missing: %v", statErr)
	}

	if runtime.GOOS != "windows" && info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("executable bit lost: %v", info.Mode())
	}
}

// TestExtractRejectsEscapingEntries: entries pointing outside the
// destination never land.
func TestExtractRejectsEscapingEntries(t *testing.T) {
	t.Parallel()

	data := writeArchive(t, func(tw *tar.Writer) {
		body := "evil"
		header := tar.Header{Name: "../evil", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}
		if err := tw.WriteHeader(&header); err != nil {
			t.Fatalf("file header: %v", err)
		}

		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("file body: %v", err)
		}
	})

	err := extract(data, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("extract = %v, want escape rejection", err)
	}
}

// TestExtractRejectsCorruptArchives: garbage is refused, not written.
func TestExtractRejectsCorruptArchives(t *testing.T) {
	t.Parallel()

	if err := extract([]byte("not a gzip"), t.TempDir()); err == nil {
		t.Fatal("extract accepted a corrupt archive")
	}
}

func TestExtractRejectsOversizedEntry(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	header := &tar.Header{Name: "driver/huge", Typeflag: tar.TypeReg, Mode: 0o644, Size: maxArchiveFile + 1}
	if err := tw.WriteHeader(header); err != nil {
		t.Fatalf("oversized header: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}

	if err := extract(buf.Bytes(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("extract oversized entry = %v", err)
	}
}

func TestExtractRejectsArchiveBomb(t *testing.T) {
	t.Parallel()

	header := &tar.Header{Name: "driver/file", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1}
	if _, err := validateEntry(header, maxArchiveSize); err == nil || !strings.Contains(err.Error(), "decompressed") {
		t.Fatalf("validateEntry = %v, want total-size rejection", err)
	}

	data := writeArchive(t, func(tw *tar.Writer) {
		for index := 0; index <= maxArchiveEntries; index++ {
			entry := &tar.Header{Name: "driver/d/" + strconv.Itoa(index), Typeflag: tar.TypeDir, Mode: 0o755}
			if err := tw.WriteHeader(entry); err != nil {
				t.Fatalf("entry %d: %v", index, err)
			}
		}
	})
	if err := extract(data, t.TempDir()); err == nil || !strings.Contains(err.Error(), "entries") {
		t.Fatalf("extract entry bomb = %v", err)
	}
}

func TestExtractRejectsDuplicateEntries(t *testing.T) {
	t.Parallel()

	data := writeArchive(t, func(tw *tar.Writer) {
		for range 2 {
			if err := tw.WriteHeader(&tar.Header{Name: "driver/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
				t.Fatalf("duplicate header: %v", err)
			}
		}
	})

	if err := extract(data, t.TempDir()); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("extract duplicate = %v", err)
	}
}

func TestExtractValidatesSymlinkResolution(t *testing.T) {
	t.Parallel()

	t.Run("contained parent reference", func(t *testing.T) {
		t.Parallel()

		data := linkArchive(t, "Bundle/links/current", "../target", "")
		dir := t.TempDir()
		if err := extract(data, dir); err != nil {
			t.Fatalf("extract safe link: %v", err)
		}
		// extract writes link targets in host separators.
		want := filepath.FromSlash("../target")
		if target, err := os.Readlink(filepath.Join(dir, "Bundle", "links", "current")); err != nil || target != want {
			t.Fatalf("safe link = %q, %v; want %q", target, err, want)
		}
	})

	t.Run("escaping parent reference", func(t *testing.T) {
		t.Parallel()

		data := linkArchive(t, "Bundle/link", "../../outside", "")
		if err := extract(data, t.TempDir()); err == nil || !strings.Contains(err.Error(), "unsafe link") {
			t.Fatalf("extract escaping link = %v", err)
		}
	})

	t.Run("followed archive link", func(t *testing.T) {
		t.Parallel()

		data := linkArchive(t, "Bundle/link", "inside", "Bundle/link/file")
		if err := extract(data, t.TempDir()); err == nil || !strings.Contains(err.Error(), "follows symlink") {
			t.Fatalf("extract followed link = %v", err)
		}
	})
}

func TestExtractRejectsUnsafeMode(t *testing.T) {
	t.Parallel()

	header := &tar.Header{Name: "driver/bin", Typeflag: tar.TypeReg, Mode: 0o4755}
	if _, err := validateEntry(header, 0); err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("validateEntry unsafe mode = %v", err)
	}
}

func linkArchive(t *testing.T, name, link, followedFile string) []byte {
	t.Helper()

	return writeArchive(t, func(tw *tar.Writer) {
		for _, dir := range []string{"Bundle/", "Bundle/links/", "Bundle/target/", "Bundle/inside/"} {
			if err := tw.WriteHeader(&tar.Header{Name: dir, Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
				t.Fatalf("directory %s: %v", dir, err)
			}
		}
		linkHeader := &tar.Header{Name: name, Typeflag: tar.TypeSymlink, Mode: 0o777, Linkname: link}
		if err := tw.WriteHeader(linkHeader); err != nil {
			t.Fatalf("symlink header: %v", err)
		}
		if followedFile == "" {
			return
		}
		body := []byte("blocked")
		header := &tar.Header{
			Name: followedFile, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body)),
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatalf("followed file header: %v", err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatalf("followed file: %v", err)
		}
	})
}
