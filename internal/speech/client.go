package speech

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// The managed engine's native tools and models ride on prukka's OWN release,
// so the catalog is an asset of the release whose tag equals the daemon
// version — there is no separate engine release.
const (
	releaseAssetBase = "https://github.com/ubyte-source/prukka/releases/download/"
	catalogAsset     = "prukka-engine-catalog.json"
)

// CatalogURLEnv overrides the catalog location for mirrors, tests and
// development builds, the same escape hatch the shell installer offers via
// PRUKKA_INSTALL_URL.
const CatalogURLEnv = "PRUKKA_ENGINE_CATALOG"

// maxArtifactBytes bounds any single engine download; the largest legitimate
// artifact today (the darwin runtime with libraries) stays well under it.
const maxArtifactBytes = 2 << 30

// CatalogURL resolves the catalog location for a daemon version: the managed
// engine assets ride on prukka's own release, so the release tag is the
// version. PRUKKA_ENGINE_CATALOG overrides it; a development build ("dev" or
// empty) has no published release and requires the override.
func CatalogURL(version string) (string, error) {
	if override := os.Getenv(CatalogURLEnv); override != "" {
		return override, nil
	}

	version = strings.TrimSpace(version)
	if version == "" || version == "dev" {
		return "", fmt.Errorf("no managed engine catalog for a %q build: set %s to a catalog URL", version, CatalogURLEnv)
	}

	return releaseAssetBase + version + "/" + catalogAsset, nil
}

// Client fetches the catalog and its artifacts over a hardened transport.
type Client struct {
	http       *http.Client
	catalogURL string
}

// NewClient wires a downloader against one catalog location.
func NewClient(catalogURL string) *Client {
	return &Client{http: downloadClient(), catalogURL: catalogURL}
}

// Catalog fetches and validates the pinned catalog.
func (c *Client) Catalog(ctx context.Context) (catalog *Catalog, err error) {
	body, err := c.get(ctx, c.catalogURL)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, body.Close()) }()

	return ParseCatalog(body)
}

// Fetch streams one artifact into w, verifying its size and SHA-256 before
// reporting success; progress receives byte counts as the download advances.
func (c *Client) Fetch(
	ctx context.Context, name, rawURL, sha string, size int64, w io.Writer, progress Reporter,
) (err error) {
	body, err := c.get(ctx, rawURL)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, body.Close()) }()

	hash := sha256.New()
	counted := &countingWriter{next: io.MultiWriter(w, hash), name: name, total: size, progress: progress}
	copied, err := io.Copy(counted, io.LimitReader(body, size+1))
	if err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	if copied != size {
		return fmt.Errorf("download %s: got %d bytes, catalog declares %d", name, copied, size)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != sha {
		return fmt.Errorf("download %s: checksum mismatch — refusing to install", name)
	}

	return nil
}

func (c *Client) get(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", rawURL, err)
	}

	reply, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	if reply.StatusCode != http.StatusOK {
		closeQuietly(reply.Body)

		return nil, fmt.Errorf("fetch %s: http %d", rawURL, reply.StatusCode)
	}

	return reply.Body, nil
}

// countingWriter forwards writes and reports coarse progress: at most one
// report per percent step, so a slow terminal never throttles the download.
type countingWriter struct {
	next     io.Writer
	progress Reporter
	name     string
	total    int64
	done     int64
	lastStep int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.next.Write(p)
	w.done += int64(n)
	if w.progress != nil {
		if step := w.done * 100 / max(w.total, 1); step != w.lastStep {
			w.lastStep = step
			w.progress(Progress{Phase: PhaseDownload, Item: w.name, DoneBytes: w.done, TotalBytes: w.total})
		}
	}

	return n, err
}

// downloadClient mirrors the managed-ffmpeg transport hardening: bounded
// timeouts, capped headers, and https-only redirects.
func downloadClient() *http.Client {
	transport := &http.Transport{
		Proxy:                  http.ProxyFromEnvironment,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    30 * time.Second,
		ResponseHeaderTimeout:  30 * time.Second,
		MaxResponseHeaderBytes: 1 << 20,
	}

	return &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("refusing non-https redirect to %s", req.URL)
			}

			return nil
		},
	}
}

// closeQuietly drops a best-effort close error on a resource being abandoned
// after a failure; success paths join close errors into their returns.
func closeQuietly(c io.Closer) {
	if err := c.Close(); err != nil {
		return
	}
}

// requireHTTPSOrLoopback admits catalog overrides that point at a local test
// server while every default stays https.
func requireHTTPSOrLoopback(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("catalog URL %q: %w", rawURL, err)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && u.Hostname() == "127.0.0.1" {
		return nil
	}

	return fmt.Errorf("catalog URL %q is not https", rawURL)
}
