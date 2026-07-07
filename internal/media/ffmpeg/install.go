package ffmpeg

import (
	"archive/tar"
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/ulikunitz/xz"
)

// maxArchive bounds downloads and keeps archive extraction from becoming a
// decompression bomb (gosec G110 stays enforced here).
const maxArchive = 1 << 30

// maxBinary bounds the extracted ffmpeg binary.
const maxBinary = 512 << 20

// Resolve returns a usable ffmpeg: $PATH first, then the state-dir install.
// It never downloads — Install does, on explicit request (`prukka setup`).
func Resolve(stateDir string) (string, error) {
	if path, err := exec.LookPath("ffmpeg"); err == nil {
		return path, nil
	}

	installed := installedPath(stateDir)
	if _, err := os.Stat(installed); err == nil {
		return installed, nil
	}

	return "", errors.New("ffmpeg not found — run `prukka setup` to install it automatically")
}

// Install downloads the pinned build, verifies its checksum and unpacks
// the binary into stateDir/bin.
func Install(ctx context.Context, stateDir string, progress io.Writer) (string, error) {
	if path, err := Resolve(stateDir); err == nil {
		sayf(progress, "ffmpeg already available: %s", path)

		return path, nil
	}

	b, err := platformBuild()
	if err != nil {
		return "", err
	}

	sayf(progress, "downloading ffmpeg (pinned static build)…\n  %s", b.url)

	archive, err := download(ctx, &b)
	if err != nil {
		return "", err
	}

	defer func() {
		if rmErr := os.Remove(archive); rmErr != nil {
			sayf(progress, "note: temp archive not removed: %v", rmErr)
		}
	}()

	dest := installedPath(stateDir)
	if mkErr := os.MkdirAll(filepath.Dir(dest), 0o700); mkErr != nil {
		return "", fmt.Errorf("create install dir: %w", mkErr)
	}

	sayf(progress, "verifying checksum and unpacking…")

	if unpackErr := unpack(archive, b.kind, dest); unpackErr != nil {
		return "", unpackErr
	}

	sayf(progress, "installed: %s", dest)

	return dest, nil
}

// installedPath is where the managed binary lives.
func installedPath(stateDir string) string {
	return filepath.Join(stateDir, "bin", binaryName())
}

// sayf writes one progress line; progress is best-effort by contract.
func sayf(w io.Writer, format string, args ...any) {
	if w == nil {
		return
	}

	if _, err := fmt.Fprintf(w, format+"\n", args...); err != nil {
		return
	}
}

// downloadClient bounds the connect and header phases only: slow links are
// legitimate for a huge body, Ctrl-C interrupts via ctx.
var downloadClient = &http.Client{
	Transport: &http.Transport{
		ResponseHeaderTimeout: 30 * time.Second,
		Proxy:                 http.ProxyFromEnvironment,
	},
}

// download streams the archive to a temp file while hashing it, then
// verifies the pinned checksum before anything is trusted.
func download(ctx context.Context, b *build) (path string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.url, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}

	resp, doErr := downloadClient.Do(req)
	if doErr != nil {
		return "", fmt.Errorf("download ffmpeg: %w", doErr)
	}

	defer func() { err = errors.Join(err, resp.Body.Close()) }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download ffmpeg: http %d", resp.StatusCode)
	}

	tmp, tmpErr := os.CreateTemp("", "prukka-ffmpeg-*")
	if tmpErr != nil {
		return "", fmt.Errorf("temp file: %w", tmpErr)
	}

	hash := sha256.New()

	_, copyErr := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(resp.Body, maxArchive))
	closeErr := tmp.Close()

	if streamErr := errors.Join(copyErr, closeErr); streamErr != nil {
		return "", fmt.Errorf("save archive: %w", streamErr)
	}

	if got := hex.EncodeToString(hash.Sum(nil)); got != b.sha256 {
		return "", fmt.Errorf("checksum mismatch for %s: got %s — refusing to install", b.url, got)
	}

	return tmp.Name(), nil
}

// unpack extracts the ffmpeg binary from the verified archive to dest.
func unpack(archive, kind, dest string) error {
	switch kind {
	case kindZip:
		return unpackZip(archive, dest)
	case kindTarXz:
		return unpackTarXz(archive, dest)
	default:
		return fmt.Errorf("unknown archive kind %q", kind)
	}
}

// unpackZip finds the ffmpeg entry in a zip archive.
func unpackZip(archive, dest string) (err error) {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}

	defer func() { err = errors.Join(err, r.Close()) }()

	for _, f := range r.File {
		if filepath.Base(f.Name) != binaryName() || f.FileInfo().IsDir() {
			continue
		}

		rc, openErr := f.Open()
		if openErr != nil {
			return fmt.Errorf("open %s in archive: %w", f.Name, openErr)
		}

		writeErr := writeBinary(dest, rc)

		return errors.Join(writeErr, rc.Close())
	}

	return errors.New("archive holds no ffmpeg binary")
}

// unpackTarXz finds the ffmpeg entry in a tar.xz archive.
func unpackTarXz(archive, dest string) (err error) {
	f, err := os.Open(filepath.Clean(archive))
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}

	defer func() { err = errors.Join(err, f.Close()) }()

	xr, xzErr := xz.NewReader(f)
	if xzErr != nil {
		return fmt.Errorf("open xz stream: %w", xzErr)
	}

	tr := tar.NewReader(xr)

	for {
		hdr, nextErr := tr.Next()
		if errors.Is(nextErr, io.EOF) {
			return errors.New("archive holds no ffmpeg binary")
		}

		if nextErr != nil {
			return fmt.Errorf("read archive: %w", nextErr)
		}

		if hdr.Typeflag == tar.TypeReg && filepath.Base(hdr.Name) == binaryName() {
			return writeBinary(dest, tr)
		}
	}
}

// writeBinary lands the executable atomically: bounded copy to a sibling
// temp file (0700 — this package's authorization), then rename.
func writeBinary(dest string, src io.Reader) (err error) {
	tmp := dest + ".partial"

	out, err := os.OpenFile(filepath.Clean(tmp), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		return fmt.Errorf("create binary: %w", err)
	}

	_, copyErr := io.Copy(out, io.LimitReader(src, maxBinary))
	closeErr := out.Close()

	if streamErr := errors.Join(copyErr, closeErr); streamErr != nil {
		return fmt.Errorf("write binary: %w", streamErr)
	}

	if renameErr := os.Rename(tmp, dest); renameErr != nil {
		return fmt.Errorf("activate binary: %w", renameErr)
	}

	return nil
}
