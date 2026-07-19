package fetch_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/fetch"
)

func serve(t *testing.T, payload []byte) (endpoint, sha string) {
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
	endpoint, _ := serve(t, payload)

	got, err := fetch.New().Bytes(t.Context(), endpoint, int64(len(payload)))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("Bytes = (%q, %v), want the payload", got, err)
	}
}

func TestBytesRejectsAnOversizedBody(t *testing.T) {
	t.Parallel()

	endpoint, _ := serve(t, []byte("12345"))

	if _, err := fetch.New().Bytes(t.Context(), endpoint, 4); err == nil {
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
	endpoint, sha := serve(t, payload)

	var out bytes.Buffer
	err := fetch.New().Verified(t.Context(), endpoint, &out,
		fetch.Want{SHA256: sha, Size: int64(len(payload))})
	if err != nil || !bytes.Equal(out.Bytes(), payload) {
		t.Fatalf("Verified = %v, wrote %q", err, out.Bytes())
	}
}

func TestVerifiedRejectsChecksumAndSizeMismatches(t *testing.T) {
	t.Parallel()

	payload := []byte("artifact-bytes")
	endpoint, sha := serve(t, payload)
	client := fetch.New()

	var out bytes.Buffer
	if err := client.Verified(t.Context(), endpoint, &out,
		fetch.Want{SHA256: strings.Repeat("0", 64), Size: int64(len(payload))}); err == nil {
		t.Fatal("Verified accepted a checksum mismatch")
	}

	out.Reset()
	if err := client.Verified(t.Context(), endpoint, &out,
		fetch.Want{SHA256: sha, Size: int64(len(payload)) + 5}); err == nil {
		t.Fatal("Verified accepted a size mismatch")
	}

	out.Reset()
	if err := client.Verified(t.Context(), endpoint, &out, fetch.Want{SHA256: sha, Limit: 4}); err == nil {
		t.Fatal("Verified accepted a payload over its limit")
	}
}

func TestVerifiedRequiresASizeLimit(t *testing.T) {
	t.Parallel()

	endpoint, sha := serve(t, []byte("artifact-bytes"))

	if err := fetch.New().Verified(t.Context(), endpoint, &bytes.Buffer{}, fetch.Want{SHA256: sha}); err == nil {
		t.Fatal("Verified accepted an unbounded download")
	}
}

// The credential shapes a catalog or artifact URL may legally carry:
// internal/speech's requireHTTPSOrLoopback admits userinfo and any query, so a
// credentialed mirror and a presigned URL are both supported configurations.
const (
	secretUser      = "mirror-user"
	secretPassword  = "s3cr3tpass"
	secretSignature = "s1gnature"
)

// credentialed rewrites a loopback server URL into one of those shapes.
func credentialed(t *testing.T, base, path string) string {
	t.Helper()

	parsed, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse %s: %v", base, err)
	}
	parsed.User = url.UserPassword(secretUser, secretPassword)
	parsed.Path = path
	parsed.RawQuery = "X-Amz-Signature=" + secretSignature

	return parsed.String()
}

// assertRedacted holds the whole package to one display form: an error names
// the scheme and host it failed on, and nothing else the URL carried.
func assertRedacted(t *testing.T, err error, want string) {
	t.Helper()

	if err == nil {
		t.Fatal("the failure path returned no error")
	}

	got := err.Error()
	for _, secret := range []string{secretUser, secretPassword, secretSignature} {
		if strings.Contains(got, secret) {
			t.Fatalf("error renders the secret %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, want) {
		t.Fatalf("error does not name %q: %s", want, got)
	}
}

// leakServer answers /artifact with a declared length and /chunked with none,
// so both the declared-length and the streamed byte ceilings are reachable.
func leakServer(t *testing.T) (base, sha string) {
	t.Helper()

	payload := []byte("artifact-bytes")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chunked" {
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Error("test server response is not flushable")

				return
			}
			flusher.Flush()
		} else if r.URL.Path != "/artifact" {
			http.NotFound(w, r)

			return
		}
		if _, err := w.Write(payload); err != nil {
			t.Errorf("serve payload: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	sum := sha256.Sum256(payload)

	return server.URL, hex.EncodeToString(sum[:])
}

// Every error this package renders travels to EngineStatus.CatalogError, the
// events stream and `prukka setup` output, so no response-shaped failure may
// echo the credential its URL carried.
func TestResponseErrorsRenderNoURLCredentials(t *testing.T) {
	t.Parallel()

	base, sha := leakServer(t)
	client := fetch.New()
	artifact := credentialed(t, base, "/artifact")
	chunked := credentialed(t, base, "/chunked")

	for name, run := range map[string]func() error{
		"no size limit": func() error {
			return client.Verified(t.Context(), artifact, io.Discard, fetch.Want{SHA256: sha})
		},
		"http status": func() error {
			_, err := client.Bytes(t.Context(), credentialed(t, base, "/missing"), 1<<10)

			return err
		},
		"declared length over the limit": func() error {
			return client.Verified(t.Context(), artifact, io.Discard, fetch.Want{SHA256: sha, Limit: 4})
		},
		"streamed body over the limit": func() error {
			return client.Verified(t.Context(), chunked, io.Discard, fetch.Want{SHA256: sha, Limit: 4})
		},
		"buffered body over the limit": func() error {
			_, err := client.Bytes(t.Context(), chunked, 4)

			return err
		},
		"size mismatch": func() error {
			return client.Verified(t.Context(), artifact, io.Discard, fetch.Want{SHA256: sha, Size: 99})
		},
		"checksum mismatch": func() error {
			return client.Verified(t.Context(), artifact, io.Discard,
				fetch.Want{SHA256: strings.Repeat("0", 64), Size: 14})
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assertRedacted(t, run(), base)
		})
	}
}

// The request half leaks differently: net/http records the URL on every
// *url.Error it returns — whole for a parse failure, password-masked for a
// transport failure — so the wrapped cause must not be rendered verbatim.
func TestRequestErrorsRenderNoURLCredentials(t *testing.T) {
	t.Parallel()

	client := fetch.New()
	credentials := secretUser + ":" + secretPassword + "@"
	signature := "?X-Amz-Signature=" + secretSignature

	t.Run("unparsable url", func(t *testing.T) {
		t.Parallel()

		_, err := client.Bytes(t.Context(), "http://"+credentials+"exam ple.com/x"+signature, 1<<10)
		assertRedacted(t, err, "[redacted-url]")
	})

	t.Run("transport failure", func(t *testing.T) {
		t.Parallel()

		// Port 1 is reserved and unbound, so the dial fails without a server.
		_, err := client.Bytes(t.Context(), "http://"+credentials+"127.0.0.1:1/x"+signature, 1<<10)
		assertRedacted(t, err, "http://127.0.0.1:1")
	})

	t.Run("non-https redirect", func(t *testing.T) {
		t.Parallel()

		base, _ := leakServer(t)
		target := credentialed(t, base, "/artifact")
		redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target, http.StatusFound)
		}))
		t.Cleanup(redirector.Close)

		_, err := client.Bytes(t.Context(), redirector.URL, 1<<10)
		assertRedacted(t, err, base)
	})
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
