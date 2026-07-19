package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadBoundedRejectsOverflow(t *testing.T) {
	t.Parallel()

	if _, err := readBounded(strings.NewReader("12345"), 4); err == nil {
		t.Fatal("readBounded accepted a payload over its limit")
	}
	got, err := readBounded(strings.NewReader("1234"), 4)
	if err != nil || string(got) != "1234" {
		t.Fatalf("readBounded exact limit = (%q, %v)", got, err)
	}
}

// TestExtractTarReturnsRequestedMember: extraction is by name, not by
// position — proven with a member that is not the production binary.
func TestExtractTarReturnsRequestedMember(t *testing.T) {
	t.Parallel()

	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{"README": "docs", "helper": "payload"} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))}); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("tar body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	data, err := extractTar(archive.Bytes(), "helper")
	if err != nil || string(data) != "payload" {
		t.Fatalf("extractTar = %q, %v; want the requested member", data, err)
	}
}

func TestExtractTarRejectsOversizedBinaryHeader(t *testing.T) {
	t.Parallel()

	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "prukka", Mode: 0o755, Size: maxArchiveBytes + 1}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	if _, err := extractTar(archive.Bytes(), "prukka"); err == nil {
		t.Fatal("extractTar accepted an oversized binary header")
	}
}

func TestExtractTarRejectsTruncatedBinary(t *testing.T) {
	t.Parallel()

	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "prukka", Mode: 0o755, Size: 8}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write([]byte("short")); err != nil {
		t.Fatalf("tar body: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	if _, err := extractTar(archive.Bytes(), "prukka"); err == nil {
		t.Fatal("extractTar accepted a truncated binary")
	}
}

// TestExtractTarMissingBinaryIsNotEOF: a fully-read archive that simply
// lacks the binary must report that in the same identity as extractZip —
// without smuggling the stream's io.EOF into the error chain.
func TestExtractTarMissingBinaryIsNotEOF(t *testing.T) {
	t.Parallel()

	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "README", Mode: 0o644, Size: 5}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write([]byte("hello")); err != nil {
		t.Fatalf("tar body: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	_, err := extractTar(archive.Bytes(), "prukka")
	if err == nil {
		t.Fatal("extractTar found a binary in an archive without one")
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("missing binary satisfies errors.Is(err, io.EOF): %v", err)
	}
	if !strings.Contains(err.Error(), `binary "prukka" not in archive`) {
		t.Fatalf("missing binary reported as %q", err)
	}
}

// TestExtractTarReportsCorruptStreamAsReadFailure: real corruption must not
// wear the "not in archive" message.
func TestExtractTarReportsCorruptStreamAsReadFailure(t *testing.T) {
	t.Parallel()

	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	if _, err := gz.Write([]byte("this is not a tar stream")); err != nil {
		t.Fatalf("gzip body: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	_, err := extractTar(archive.Bytes(), "prukka")
	if err == nil {
		t.Fatal("extractTar accepted a corrupt tar stream")
	}
	if !strings.Contains(err.Error(), "read release archive") ||
		strings.Contains(err.Error(), "not in archive") {
		t.Fatalf("corruption reported as %q, want a read failure", err)
	}
}

func TestExtractZipRejectsOversizedBinaryHeader(t *testing.T) {
	t.Parallel()

	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	header := &zip.FileHeader{Name: "prukka.exe", Method: zip.Store}
	header.SetMode(0o755)
	header.UncompressedSize64 = maxArchiveBytes + 1
	if _, err := zw.CreateRaw(header); err != nil {
		t.Fatalf("zip header: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}

	if _, err := extractZip(archive.Bytes(), "prukka.exe"); err == nil {
		t.Fatal("extractZip accepted an oversized binary header")
	}
}
