package update_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ubyte-source/prukka/internal/update"
)

// reply writes one handler response, failing the test on error.
func replyf(t *testing.T, w http.ResponseWriter, format string, args ...any) {
	t.Helper()

	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		t.Errorf("write response: %v", err)
	}
}

// fixture serves a fabricated release: one platform archive holding the
// given binary, its checksums.txt, and the /releases/latest index.
func fixture(t *testing.T, tag string, binary []byte) *httptest.Server {
	t.Helper()

	name, archive := platformArchive(t, binary)
	sum := sha256.Sum256(archive)
	checksums := hex.EncodeToString(sum[:]) + "  " + name + "\n"

	var server *httptest.Server

	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		replyf(t, w, `{"tag_name":%q,"assets":[
			{"name":%q,"browser_download_url":"%s/dl/%s"},
			{"name":"checksums.txt","browser_download_url":"%s/dl/checksums.txt"}]}`,
			tag, name, server.URL, name, server.URL)
	})
	mux.HandleFunc("/dl/"+name, func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write(archive); err != nil {
			t.Errorf("serve archive: %v", err)
		}
	})
	mux.HandleFunc("/dl/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(checksums)); err != nil {
			t.Errorf("serve checksums: %v", err)
		}
	})

	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return server
}

// platformArchive builds the archive shape goreleaser publishes for the
// host platform.
func platformArchive(t *testing.T, binary []byte) (name string, archive []byte) {
	t.Helper()

	var buf bytes.Buffer

	if runtime.GOOS == "windows" {
		zw := zip.NewWriter(&buf)

		f, err := zw.Create("prukka.exe")
		if err != nil {
			t.Fatalf("zip member: %v", err)
		}

		if _, err := f.Write(binary); err != nil {
			t.Fatalf("zip write: %v", err)
		}

		if err := zw.Close(); err != nil {
			t.Fatalf("zip close: %v", err)
		}

		return fmt.Sprintf("prukka_windows_%s.zip", runtime.GOARCH), buf.Bytes()
	}

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	if err := tw.WriteHeader(&tar.Header{Name: "prukka", Mode: 0o755, Size: int64(len(binary))}); err != nil {
		t.Fatalf("tar header: %v", err)
	}

	if _, err := tw.Write(binary); err != nil {
		t.Fatalf("tar write: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}

	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	return fmt.Sprintf("prukka_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH), buf.Bytes()
}

func TestApplyReplacesTheBinary(t *testing.T) {
	t.Parallel()

	server := fixture(t, "v9.9.9", []byte("new build"))
	client := update.New(server.URL)

	release, err := client.Latest(t.Context())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}

	if release.Tag != "v9.9.9" {
		t.Fatalf("tag = %q", release.Tag)
	}

	dest := filepath.Join(t.TempDir(), "prukka")
	if seedErr := os.WriteFile(dest, []byte("old build"), 0o600); seedErr != nil {
		t.Fatalf("seed old binary: %v", seedErr)
	}

	if err := client.Apply(t.Context(), release, "0.6.0", dest); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got, readErr := os.ReadFile(filepath.Clean(dest))
	if readErr != nil || string(got) != "new build" {
		t.Fatalf("binary after update = %q (%v)", got, readErr)
	}
}

func TestApplyIsUpToDateOnSameVersion(t *testing.T) {
	t.Parallel()

	server := fixture(t, "v0.6.0", []byte("same"))
	client := update.New(server.URL)

	release, err := client.Latest(t.Context())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}

	err = client.Apply(t.Context(), release, "0.6.0", filepath.Join(t.TempDir(), "prukka"))
	if !errors.Is(err, update.ErrUpToDate) {
		t.Fatalf("Apply = %v, want ErrUpToDate", err)
	}
}

func TestApplyRejectsTamperedArchive(t *testing.T) {
	t.Parallel()

	// The checksums.txt of this fixture covers a different payload than
	// the archive the server actually serves.
	name, archive := platformArchive(t, []byte("tampered"))
	sum := sha256.Sum256([]byte("what the release signed"))
	checksums := hex.EncodeToString(sum[:]) + "  " + name + "\n"

	var server *httptest.Server

	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		replyf(t, w, `{"tag_name":"v9.9.9","assets":[
			{"name":%q,"browser_download_url":"%s/dl/%s"},
			{"name":"checksums.txt","browser_download_url":"%s/dl/checksums.txt"}]}`,
			name, server.URL, name, server.URL)
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) == "checksums.txt" {
			replyf(t, w, "%s", checksums)

			return
		}

		if _, err := w.Write(archive); err != nil {
			t.Errorf("serve archive: %v", err)
		}
	})

	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := update.New(server.URL)

	release, err := client.Latest(t.Context())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "prukka")

	err = client.Apply(t.Context(), release, "0.6.0", dest)
	if err == nil || !errors.Is(err, err) || err.Error() == "" {
		t.Fatal("Apply accepted a tampered archive")
	}

	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatal("tampered update touched the destination")
	}
}

func TestApplyMissingPlatformAsset(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		replyf(t, w, `{"tag_name":"v9.9.9","assets":[]}`)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := update.New(server.URL)

	release, err := client.Latest(t.Context())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}

	if err := client.Apply(t.Context(), release, "0.6.0", filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("Apply succeeded without a platform asset")
	}
}
