// Package update implements the explicit self-update: fetch, verify
// against published checksums, replace atomically. Never automatic.
package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ubyte-source/prukka/internal/fetch"
	"github.com/ubyte-source/prukka/internal/hostos"
)

// maxArchiveBytes bounds downloads and extraction (decompression safety).
const maxArchiveBytes = 200 << 20

// requestTimeout bounds each release-API and download call.
const requestTimeout = 5 * time.Minute

// ErrUpToDate reports that the running version is the latest release.
var ErrUpToDate = errors.New("already up to date")

// ErrChecksumMismatch reports a downloaded archive whose SHA-256 disagrees
// with the release's checksums.txt entry.
var ErrChecksumMismatch = errors.New("checksum mismatch")

// ErrChecksumMissing reports a checksums.txt with no entry for the platform
// archive.
var ErrChecksumMissing = errors.New("checksums.txt has no entry")

// Release is one published version and its downloadable assets.
type Release struct {
	assets map[string]string // asset name → download URL
	Tag    string
}

// Client talks to a GitHub-style releases API.
type Client struct {
	http *fetch.Client
	base string
}

// New wires a client for the given API base
// (e.g. https://api.github.com/repos/ubyte-source/prukka).
func New(base string) *Client {
	return &Client{base: strings.TrimSuffix(base, "/"), http: fetch.New()}
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
	if imageErr := validateExecutable(binary); imageErr != nil {
		return imageErr
	}

	return replaceBinary(dest, binary)
}

func validateExecutable(binary []byte) error {
	valid := false
	switch runtime.GOOS {
	case hostos.Linux:
		valid = len(binary) >= 4 && bytes.Equal(binary[:4], []byte{'\x7f', 'E', 'L', 'F'})
	case hostos.Windows:
		valid = len(binary) >= 2 && bytes.Equal(binary[:2], []byte{'M', 'Z'})
	case hostos.Darwin:
		valid = validMachOMagic(binary)
	}
	if !valid {
		return fmt.Errorf("release archive does not contain a valid %s executable", runtime.GOOS)
	}

	return nil
}

func validMachOMagic(binary []byte) bool {
	if len(binary) < 4 {
		return false
	}

	magic := [4]byte(binary[:4])
	switch magic {
	case [4]byte{0xce, 0xfa, 0xed, 0xfe}, [4]byte{0xcf, 0xfa, 0xed, 0xfe},
		[4]byte{0xfe, 0xed, 0xfa, 0xce}, [4]byte{0xfe, 0xed, 0xfa, 0xcf},
		[4]byte{0xca, 0xfe, 0xba, 0xbe}, [4]byte{0xbe, 0xba, 0xfe, 0xca},
		[4]byte{0xca, 0xfe, 0xba, 0xbf}, [4]byte{0xbf, 0xba, 0xfe, 0xca}:
		return true
	default:
		return false
	}
}

// archiveName is goreleaser's platform artifact name.
func archiveName() string {
	ext := "tar.gz"
	if runtime.GOOS == hostos.Windows {
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
				return fmt.Errorf("%w for %s", ErrChecksumMismatch, name)
			}

			return nil
		}
	}

	return fmt.Errorf("%w for %s", ErrChecksumMissing, name)
}

// extract returns the prukka binary from the platform archive.
func extract(archive []byte) ([]byte, error) {
	want := "prukka"
	if runtime.GOOS == hostos.Windows {
		return extractZip(archive, want+".exe")
	}

	return extractTar(archive, want)
}

func extractTar(archive []byte, want string) (data []byte, err error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	defer func() {
		err = errors.Join(err, gz.Close())
	}()

	tr := tar.NewReader(io.LimitReader(gz, maxArchiveBytes+1))

	for {
		hdr, nextErr := tr.Next()
		// io.EOF is the clean end of the archive, not a read failure.
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return nil, fmt.Errorf("read release archive: %w", nextErr)
		}

		if filepath.Base(hdr.Name) != want {
			continue
		}
		if !regularTarMember(hdr) {
			return nil, fmt.Errorf("archive member %q is not a regular file", hdr.Name)
		}
		if hdr.Size > maxArchiveBytes {
			return nil, fmt.Errorf("archive member %q exceeds %d bytes", hdr.Name, maxArchiveBytes)
		}

		binary, readErr := readBounded(tr, maxArchiveBytes)
		if readErr != nil {
			return nil, readErr
		}
		if int64(len(binary)) != hdr.Size {
			return nil, fmt.Errorf("archive member %q is truncated", hdr.Name)
		}

		return binary, nil
	}

	return nil, fmt.Errorf("binary %q not in archive", want)
}

func regularTarMember(hdr *tar.Header) bool {
	return hdr.Size >= 0 && hdr.Typeflag == tar.TypeReg
}

func extractZip(archive []byte, want string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}

	for _, f := range zr.File {
		if filepath.Base(f.Name) != want {
			continue
		}
		if !f.Mode().IsRegular() {
			return nil, fmt.Errorf("archive member %q is not a regular file", f.Name)
		}
		if f.UncompressedSize64 > maxArchiveBytes {
			return nil, fmt.Errorf("archive member %q exceeds %d bytes", f.Name, maxArchiveBytes)
		}

		rc, openErr := f.Open()
		if openErr != nil {
			return nil, fmt.Errorf("archive member: %w", openErr)
		}

		data, readErr := readBounded(rc, maxArchiveBytes)

		return data, errors.Join(readErr, rc.Close())
	}

	return nil, fmt.Errorf("binary %q not in archive", want)
}

// get issues one bounded GET under the per-call request budget.
func (c *Client) get(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	data, err := c.http.Bytes(ctx, url, maxArchiveBytes)
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}

	return data, nil
}

func readBounded(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("payload exceeds %d bytes", limit)
	}

	return data, nil
}
