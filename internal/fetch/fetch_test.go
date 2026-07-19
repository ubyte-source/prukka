package fetch_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/fetch"
)

func serve(t *testing.T, payload []byte) (url, sha string) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write(payload); err != nil {
			t.Errorf("serve payload: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	sum := sha256.Sum256(payload)

	return server.URL, hex.EncodeToString(sum[:])
}

func TestBytesReadsWithinTheLimit(t *testing.T) {
	t.Parallel()

	payload := []byte("catalog-document")
	url, _ := serve(t, payload)

	got, err := fetch.New().Bytes(t.Context(), url, int64(len(payload)))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("Bytes = (%q, %v), want the payload", got, err)
	}
}

func TestBytesRejectsAnOversizedBody(t *testing.T) {
	t.Parallel()

	url, _ := serve(t, []byte("12345"))

	if _, err := fetch.New().Bytes(t.Context(), url, 4); err == nil {
		t.Fatal("Bytes accepted a payload over its limit")
	}
}

func TestBytesRejectsAnErrorStatus(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	if _, err := fetch.New().Bytes(t.Context(), server.URL, 1<<10); err == nil {
		t.Fatal("Bytes accepted a 404 response")
	}
}

func TestVerifiedAcceptsTheDeclaredArtifact(t *testing.T) {
	t.Parallel()

	payload := []byte("artifact-bytes")
	url, sha := serve(t, payload)

	var out bytes.Buffer
	err := fetch.New().Verified(t.Context(), url, &out,
		fetch.Want{SHA256: sha, Size: int64(len(payload))})
	if err != nil || !bytes.Equal(out.Bytes(), payload) {
		t.Fatalf("Verified = %v, wrote %q", err, out.Bytes())
	}
}

func TestVerifiedRejectsChecksumAndSizeMismatches(t *testing.T) {
	t.Parallel()

	payload := []byte("artifact-bytes")
	url, sha := serve(t, payload)
	client := fetch.New()

	var out bytes.Buffer
	if err := client.Verified(t.Context(), url, &out,
		fetch.Want{SHA256: strings.Repeat("0", 64), Size: int64(len(payload))}); err == nil {
		t.Fatal("Verified accepted a checksum mismatch")
	}

	out.Reset()
	if err := client.Verified(t.Context(), url, &out,
		fetch.Want{SHA256: sha, Size: int64(len(payload)) + 5}); err == nil {
		t.Fatal("Verified accepted a size mismatch")
	}

	out.Reset()
	if err := client.Verified(t.Context(), url, &out, fetch.Want{SHA256: sha, Limit: 4}); err == nil {
		t.Fatal("Verified accepted a payload over its limit")
	}
}

func TestVerifiedRequiresASizeLimit(t *testing.T) {
	t.Parallel()

	url, sha := serve(t, []byte("artifact-bytes"))

	if err := fetch.New().Verified(t.Context(), url, &bytes.Buffer{}, fetch.Want{SHA256: sha}); err == nil {
		t.Fatal("Verified accepted an unbounded download")
	}
}

// A redirect off https is how a hijacked mirror downgrades a download; the
// transport refuses to follow it.
func TestRedirectsMustStayHTTPS(t *testing.T) {
	t.Parallel()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte("payload")); err != nil {
			t.Errorf("serve payload: %v", err)
		}
	}))
	t.Cleanup(target.Close)

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	if _, err := fetch.New().Bytes(t.Context(), redirector.URL, 1<<10); err == nil {
		t.Fatal("Bytes followed a plain-http redirect")
	}
}
