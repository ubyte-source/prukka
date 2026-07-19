package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
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
