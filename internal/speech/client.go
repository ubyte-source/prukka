package speech

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/ubyte-source/prukka/internal/besteffort"
	"github.com/ubyte-source/prukka/internal/fetch"
	"github.com/ubyte-source/prukka/internal/redact"
)

// The catalog is an asset of prukka's own release, tagged with the daemon
// version; there is no separate engine release.
const (
	releaseAssetBase = "https://github.com/ubyte-source/prukka/releases/download/"
	catalogAsset     = "prukka-engine-catalog.json"
)

// CatalogURLEnv overrides the catalog location for mirrors, tests and
// development builds.
const CatalogURLEnv = "PRUKKA_ENGINE_CATALOG"

// maxArtifactBytes bounds any single engine download.
const maxArtifactBytes = 2 << 30

// CatalogURL resolves the catalog location for a daemon version;
// PRUKKA_ENGINE_CATALOG overrides it, and a development build ("dev" or empty)
// has no published release and requires the override.
func CatalogURL(version string) (string, error) {
	if override := os.Getenv(CatalogURLEnv); override != "" {
		if err := requireHTTPSOrLoopback(override); err != nil {
			return "", fmt.Errorf("%s: %w", CatalogURLEnv, err)
		}

		return override, nil
	}

	version = strings.TrimSpace(version)
	if version == "" || version == "dev" {
		return "", fmt.Errorf("no managed engine catalog for a %q build: set %s to a catalog URL", version, CatalogURLEnv)
	}

	return releaseAssetBase + version + "/" + catalogAsset, nil
}

// Client fetches the catalog and its artifacts over the hardened downloader.
type Client struct {
	http       *fetch.Client
	catalogURL string
}

// NewClient wires a downloader against one catalog location.
func NewClient(catalogURL string) *Client {
	return &Client{http: fetch.New(), catalogURL: catalogURL}
}

// Catalog fetches and validates the pinned catalog.
func (c *Client) Catalog(ctx context.Context) (*Catalog, error) {
	// +1: an oversized document reaches ParseCatalog, so it is reported as a
	// catalog fault rather than a transport one.
	raw, err := c.http.Bytes(ctx, c.catalogURL, catalogMaxBytes+1)
	if err != nil {
		return nil, err
	}

	return ParseCatalog(bytes.NewReader(raw))
}

// Fetch streams one artifact into w, verifying its size and SHA-256 before
// reporting success; progress receives byte counts as the download advances.
func (c *Client) Fetch(
	ctx context.Context, name, rawURL, sha string, size int64, w io.Writer, progress Reporter,
) error {
	counted := &countingWriter{next: w, name: name, total: size, progress: progress}
	if err := c.http.Verified(ctx, rawURL, counted, fetch.Want{SHA256: sha, Size: size}); err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}

	return nil
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

// closeQuietly drops a best-effort close error on a resource being abandoned
// after a failure; success paths join close errors into their returns.
func closeQuietly(c io.Closer) {
	besteffort.Ignore(c.Close())
}

// requireHTTPSOrLoopback guards the catalog URL and every artifact URL it
// lists: https everywhere, plain http only toward a local test server. A
// rejected URL may carry userinfo or a presigned query, so it is named through
// redact.URL and the *url.Error wrapper is dropped — it stores the whole raw
// URL on every parse failure.
func requireHTTPSOrLoopback(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}

		return fmt.Errorf("parse %s: %w", redact.URL(rawURL), err)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && u.Hostname() == "127.0.0.1" {
		return nil
	}

	return fmt.Errorf("%s is not https", redact.URL(rawURL))
}
