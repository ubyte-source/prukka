package speech

import (
	"bytes"
	"context"
	"runtime"
	"testing"
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
