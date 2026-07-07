// Package update implements the explicit self-update: fetch, verify
// against published checksums, replace atomically. Never automatic.
package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
)

// maxArchiveBytes bounds downloads and extraction (decompression safety).
const maxArchiveBytes = 200 << 20

// requestTimeout bounds each release-API and download call.
const requestTimeout = 5 * time.Minute

// ErrUpToDate reports that the running version is the latest release.
var ErrUpToDate = errors.New("already up to date")

// Release is one published version and its downloadable assets.
type Release struct {
	assets map[string]string // asset name → download URL
	Tag    string
}

// Client talks to a GitHub-style releases API; base is injectable so tests
// run against a local server.
type Client struct {
	http *http.Client
	base string
}

// New wires a client for the given API base
// (e.g. https://api.github.com/repos/ubyte-source/prukka).
func New(base string) *Client {
	return &Client{base: strings.TrimSuffix(base, "/"), http: &http.Client{Timeout: requestTimeout}}
}

// Latest fetches the newest release and its asset index.
func (c *Client) Latest(ctx context.Context) (Release, error) {
	body, err := c.get(ctx, c.base+"/releases/latest")
	if err != nil {
		return Release{}, err
	}

	var wire struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}

	if decodeErr := json.Unmarshal(body, &wire); decodeErr != nil {
		return Release{}, fmt.Errorf("release response: %w", decodeErr)
	}

	release := Release{Tag: wire.Tag, assets: make(map[string]string, len(wire.Assets))}
	for _, a := range wire.Assets {
		release.assets[a.Name] = a.URL
	}

	return release, nil
}

// Apply replaces the binary at dest after verifying against checksums.txt;
// a matching version returns ErrUpToDate.
func (c *Client) Apply(ctx context.Context, release Release, current, dest string) error {
	if strings.TrimPrefix(release.Tag, "v") == current {
		return ErrUpToDate
	}

	name := archiveName()

	archive, err := c.asset(ctx, release, name)
	if err != nil {
		return err
	}

	if sumErr := c.verify(ctx, release, name, archive); sumErr != nil {
		return sumErr
	}

	binary, extractErr := extract(archive)
	if extractErr != nil {
		return extractErr
	}

	return ffmpeg.ReplaceBinary(dest, binary)
}

// archiveName is goreleaser's platform artifact name.
func archiveName() string {
	ext := "tar.gz"
	if runtime.GOOS == "windows" {
		ext = "zip"
	}

	return fmt.Sprintf("prukka_%s_%s.%s", runtime.GOOS, runtime.GOARCH, ext)
}

// asset downloads one named release asset, size-capped.
func (c *Client) asset(ctx context.Context, release Release, name string) ([]byte, error) {
	url, ok := release.assets[name]
	if !ok {
		return nil, fmt.Errorf("release %s has no asset %q", release.Tag, name)
	}

	return c.get(ctx, url)
}

// verify checks the archive against checksums.txt: integrity, not
// authenticity — signatures are the release pipeline's job.
func (c *Client) verify(ctx context.Context, release Release, name string, archive []byte) error {
	sums, err := c.asset(ctx, release, "checksums.txt")
	if err != nil {
		return err
	}

	got := sha256.Sum256(archive)

	for line := range strings.Lines(string(sums)) {
		sum, file, found := strings.Cut(strings.TrimSpace(line), "  ")
		if found && file == name {
			if sum != hex.EncodeToString(got[:]) {
				return fmt.Errorf("checksum mismatch for %s", name)
			}

			return nil
		}
	}

	return fmt.Errorf("checksums.txt has no entry for %s", name)
}

// extract returns the prukka binary from the platform archive.
func extract(archive []byte) ([]byte, error) {
	want := "prukka"
	if runtime.GOOS == "windows" {
		return extractZip(archive, want+".exe")
	}

	return extractTar(archive, want)
}

func extractTar(archive []byte, want string) ([]byte, error) {
	gz, err := gzip.NewReader(strings.NewReader(string(archive)))
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}

	tr := tar.NewReader(gz)

	for {
		hdr, nextErr := tr.Next()
		if nextErr != nil {
			return nil, fmt.Errorf("binary %q not in archive: %w", want, nextErr)
		}

		if filepath.Base(hdr.Name) == want {
			return io.ReadAll(io.LimitReader(tr, maxArchiveBytes))
		}
	}
}

func extractZip(archive []byte, want string) ([]byte, error) {
	zr, err := zip.NewReader(strings.NewReader(string(archive)), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}

	for _, f := range zr.File {
		if filepath.Base(f.Name) != want {
			continue
		}

		rc, openErr := f.Open()
		if openErr != nil {
			return nil, fmt.Errorf("archive member: %w", openErr)
		}

		data, readErr := io.ReadAll(io.LimitReader(rc, maxArchiveBytes))

		return data, errors.Join(readErr, rc.Close())
	}

	return nil, fmt.Errorf("binary %q not in archive", want)
}

// get issues one bounded GET.
func (c *Client) get(ctx context.Context, url string) (data []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("update request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update fetch: %w", err)
	}

	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update fetch %s: http %d", url, resp.StatusCode)
	}

	return io.ReadAll(io.LimitReader(resp.Body, maxArchiveBytes))
}
