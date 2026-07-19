package speech

import (
	"bytes"
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/redact"
)

func TestClientCatalogFetchesAndValidates(t *testing.T) {
	t.Parallel()

	server := newArtifactServer(t)
	doc := catalogDoc(t, server, runtime.GOOS, runtime.GOARCH, runtimeArchive(t), map[string][]byte{
		"stt-core": packArchive(t, "models/stt/a.bin"),
	})
	url, _, _ := server.add("/catalog.json", doc)

	catalog, err := NewClient(url).Catalog(context.Background())
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if _, err := catalog.RuntimeFor(runtime.GOOS, runtime.GOARCH); err != nil {
		t.Fatalf("runtime entry: %v", err)
	}
}

func TestClientCatalogRejectsHTTPFailure(t *testing.T) {
	t.Parallel()

	server := newArtifactServer(t)
	if _, err := NewClient(server.server.URL + "/absent.json").Catalog(context.Background()); err == nil {
		t.Fatal("missing catalog must fail")
	}
}

func TestClientFetchVerifiesArtifacts(t *testing.T) {
	t.Parallel()

	server := newArtifactServer(t)
	blob := []byte("artifact-bytes-of-some-length")
	url, sha, size := server.add("/blob", blob)
	client := NewClient(server.server.URL)

	var out bytes.Buffer
	var reports []Progress
	err := client.Fetch(context.Background(), "blob", url, sha, size, &out, func(p Progress) {
		reports = append(reports, p)
	})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !bytes.Equal(out.Bytes(), blob) {
		t.Fatal("fetched bytes differ")
	}
	if len(reports) == 0 || reports[len(reports)-1].DoneBytes != size {
		t.Fatalf("progress reports: %+v", reports)
	}

	out.Reset()
	if err := client.Fetch(context.Background(), "blob", url, sha256Hex([]byte("other")), size, &out, nil); err == nil {
		t.Fatal("checksum mismatch must fail")
	}
	out.Reset()
	if err := client.Fetch(context.Background(), "blob", url, sha, size+5, &out, nil); err == nil {
		t.Fatal("size mismatch must fail")
	}
}

func TestCatalogURLHonorsOverride(t *testing.T) {
	t.Setenv(CatalogURLEnv, "http://127.0.0.1:9/custom.json")
	got, err := CatalogURL("0.1.0")
	if err != nil || got != "http://127.0.0.1:9/custom.json" {
		t.Fatalf("override CatalogURL = %q, %v; want the override", got, err)
	}

	t.Setenv(CatalogURLEnv, "")
	got, err = CatalogURL("0.1.0")
	want := "https://github.com/ubyte-source/prukka/releases/download/0.1.0/prukka-engine-catalog.json"
	if err != nil || got != want {
		t.Fatalf("versioned CatalogURL = %q, %v; want %q", got, err, want)
	}

	if _, devErr := CatalogURL("dev"); devErr == nil {
		t.Fatal("a dev build without an override must error")
	}
}

// TestCatalogURLRejectsInsecureOverride: the catalog is the one download
// trusted by transport alone, so a plaintext mirror override must fail at
// resolution instead of downgrading that trust.
func TestCatalogURLRejectsInsecureOverride(t *testing.T) {
	t.Setenv(CatalogURLEnv, "http://mirror.corp/prukka-engine-catalog.json")

	if _, err := CatalogURL("0.1.0"); err == nil {
		t.Fatal("a plaintext catalog override must be rejected")
	}
}

// A rejected URL is rendered by `prukka setup`, the daemon's Warn log and
// the catalog error surface, and the very URLs this predicate rejects may
// carry userinfo or a presigned query — the shapes the predicate admits over
// https — so a rejection must name the endpoint through the shared
// reduction, never echo the raw URL. Fixtures are assembled from parts so no
// literal is a hardcoded-credential URL.
func TestRequireHTTPSOrLoopbackRendersNoURLSecrets(t *testing.T) {
	t.Parallel()

	user, pass, sig := "mirror-user", "s3cr3tpass", "s1gnature"

	insecure := "http://" + user + ":" + pass + "@mirror.corp/engine.tgz?X-Amz-Signature=" + sig
	if err := requireHTTPSOrLoopback(insecure); err == nil {
		t.Fatal("credentialed plaintext URL must be rejected")
	} else {
		assertRejectionRedacts(t, err, user, pass, sig, "http://mirror.corp")
	}

	unparsable := "http://" + user + ":" + pass + "@mirror corp/x?X-Amz-Signature=" + sig
	if err := requireHTTPSOrLoopback(unparsable); err == nil {
		t.Fatal("unparsable URL must be rejected")
	} else {
		assertRejectionRedacts(t, err, user, pass, sig, redact.Redacted)
	}
}

func assertRejectionRedacts(t *testing.T, err error, user, pass, sig, want string) {
	t.Helper()

	got := err.Error()
	for _, secret := range []string{user + ":", pass, sig} {
		if strings.Contains(got, secret) {
			t.Fatalf("rejection renders the secret %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, want) {
		t.Fatalf("rejection does not name %q: %s", want, got)
	}
}

func TestRequireHTTPSOrLoopback(t *testing.T) {
	t.Parallel()

	if err := requireHTTPSOrLoopback("https://example.com/x"); err != nil {
		t.Fatalf("https rejected: %v", err)
	}
	if err := requireHTTPSOrLoopback("http://127.0.0.1:8080/x"); err != nil {
		t.Fatalf("loopback rejected: %v", err)
	}
	if err := requireHTTPSOrLoopback("http://example.com/x"); err == nil {
		t.Fatal("plain http must fail")
	}
	if err := requireHTTPSOrLoopback("ftp://example.com/x"); err == nil {
		t.Fatal("ftp must fail")
	}
}
