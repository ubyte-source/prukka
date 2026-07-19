package ffmpeg

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ulikunitz/xz"
)

// maxArchive bounds the compressed download.
const maxArchive = 1 << 30

// maxExpandedArchive bounds bytes consumed from an XZ stream while tar walks
// entries, including entries that precede ffmpeg.
const maxExpandedArchive = 2 << 30

// maxBinary bounds the extracted ffmpeg binary.
const maxBinary = 512 << 20

const managedRuntimeDir = "ffmpeg-runtime"

const abandonedInstallAge = time.Hour

// Resolve returns the verified managed ffmpeg, then falls back to $PATH.
// It never downloads — Install does, on explicit request (`prukka setup`).
func Resolve(stateDir string) (string, error) {
	b, buildErr := platformBuild()
	if buildErr == nil {
		complete, err := managedInstallComplete(stateDir, platformKey(), &b)
		if err != nil {
			return "", err
		}
		if complete {
			return installedPathFor(stateDir, &b), nil
		}
		present, err := installPathPresent(installDirFor(stateDir, &b))
		if err != nil {
			return "", err
		}
		if present {
			return "", errors.New("managed ffmpeg failed integrity verification — run `prukka setup` to repair it")
		}
	}
	legacy := legacyInstalledPath(stateDir)
	if present, err := installPathPresent(legacy); err != nil {
		return "", err
	} else if present {
		return "", errors.New("legacy managed ffmpeg has no verified manifest — run `prukka setup` to replace it")
	}
	if path, err := exec.LookPath("ffmpeg"); err == nil {
		return path, nil
	}

	if buildErr != nil {
		return "", buildErr
	}

	return "", errors.New("ffmpeg not found — run `prukka setup` to install it automatically")
}

// Install downloads the pinned build, verifies its checksum and unpacks
// the binary into stateDir/bin.
func Install(ctx context.Context, stateDir string, progress io.Writer) (string, error) {
	b, buildErr := platformBuild()
	if buildErr != nil {
		if path, err := exec.LookPath("ffmpeg"); err == nil {
			sayf(progress, "ffmpeg already available: %s", path)

			return path, nil
		}

		return "", buildErr
	}

	return installBuild(ctx, stateDir, platformKey(), &b, progress)
}

func installBuild(
	ctx context.Context,
	stateDir string,
	platform string,
	b *build,
	progress io.Writer,
) (string, error) {
	complete, err := managedInstallComplete(stateDir, platform, b)
	if err != nil {
		return "", err
	}
	if complete {
		noteCleanup(cleanupManagedInstalls(stateDir), progress)

		return installedPathFor(stateDir, b), nil
	}

	sayf(progress, "downloading ffmpeg (pinned static build)…\n  %s", b.binaryURL)

	archive, err := download(ctx, b)
	if err != nil {
		return "", err
	}

	defer removeInstallPath(archive, false, progress)

	return installArchive(stateDir, platform, archive, b, progress)
}

func installArchive(stateDir, platform, archive string, b *build, progress io.Writer) (string, error) {
	root := managedRoot(stateDir)
	if mkErr := os.MkdirAll(root, 0o700); mkErr != nil {
		return "", fmt.Errorf("create install dir: %w", mkErr)
	}
	stage, err := os.MkdirTemp(root, ".install-*")
	if err != nil {
		return "", fmt.Errorf("stage ffmpeg install: %w", err)
	}
	defer removeInstallPath(stage, true, progress)

	sayf(progress, "verifying checksum and unpacking…")

	stagedBinary := filepath.Join(stage, binaryName())
	if unpackErr := unpack(archive, b.kind, stagedBinary); unpackErr != nil {
		return "", unpackErr
	}
	executableSHA, err := fileSHA256(stagedBinary)
	if err != nil {
		return "", err
	}
	manifest := manifestFor(platform, b, executableSHA)
	if metadataErr := writeInstallMetadata(stage, &manifest); metadataErr != nil {
		return "", metadataErr
	}
	if syncErr := syncDir(stage); syncErr != nil {
		return "", fmt.Errorf("sync ffmpeg install directory: %w", syncErr)
	}

	finalDir := installDirFor(stateDir, b)
	if publishErr := publishInstall(stage, finalDir, platform, b); publishErr != nil {
		return "", publishErr
	}
	noteCleanup(cleanupManagedInstalls(stateDir), progress)
	dest := filepath.Join(finalDir, binaryName())

	sayf(progress, "installed: %s", dest)

	return dest, nil
}

func removeInstallPath(path string, recursive bool, progress io.Writer) {
	var err error
	if recursive {
		err = os.RemoveAll(path)
	} else {
		err = os.Remove(path)
	}
	if err != nil {
		sayf(progress, "note: temporary ffmpeg path not removed: %v", err)
	}
}

func noteCleanup(err error, progress io.Writer) {
	if err != nil {
		sayf(progress, "note: superseded ffmpeg install not removed: %v", err)
	}
}

func installedPathFor(stateDir string, b *build) string {
	return filepath.Join(installDirFor(stateDir, b), binaryName())
}

func installDirFor(stateDir string, b *build) string {
	return filepath.Join(managedRoot(stateDir), b.archiveSHA256)
}

func managedRoot(stateDir string) string {
	return filepath.Join(stateDir, "bin", managedRuntimeDir)
}

func legacyInstalledPath(stateDir string) string {
	return filepath.Join(stateDir, "bin", binaryName())
}

func platformKey() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}

func managedInstallComplete(stateDir, platform string, b *build) (bool, error) {
	return installComplete(installDirFor(stateDir, b), platform, b)
}

func installPathPresent(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect managed ffmpeg install: %w", err)
	}
	return true, nil
}

func installComplete(dir, platform string, b *build) (complete bool, err error) {
	executable := filepath.Join(dir, binaryName())
	valid, err := validInstallExecutable(executable)
	if err != nil || !valid {
		return false, err
	}
	exact, err := exactInstallLayout(dir)
	if err != nil || !exact {
		return false, err
	}
	executableSHA, err := fileSHA256(executable)
	if err != nil {
		return false, err
	}

	manifest := manifestFor(platform, b, executableSHA)
	manifestData, err := marshalManifest(&manifest)
	if err != nil {
		return false, err
	}
	expected := []struct {
		name string
		data []byte
	}{
		{name: manifestName, data: manifestData},
		{name: noticeName, data: noticeFor(&manifest)},
		{name: licenseName, data: gpl3License},
	}
	return installMetadataComplete(dir, expected)
}

func validInstallExecutable(executable string) (bool, error) {
	info, err := os.Lstat(executable)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// The execute bit is a property of the host filesystem, not of the
	// build's target platform: Windows never encodes one.
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxBinary ||
		runtime.GOOS != osWindows && info.Mode().Perm()&0o111 == 0 {
		return false, nil
	}
	return true, nil
}

func installMetadataComplete(
	dir string,
	expected []struct {
		name string
		data []byte
	},
) (complete bool, err error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return false, fmt.Errorf("open managed ffmpeg directory: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()

	for _, file := range expected {
		actual, readErr := root.ReadFile(file.name)
		if errors.Is(readErr, os.ErrNotExist) {
			return false, nil
		}
		if readErr != nil {
			return false, fmt.Errorf("read managed ffmpeg metadata: %w", readErr)
		}
		if !bytes.Equal(actual, file.data) {
			return false, nil
		}
	}

	return true, nil
}

func exactInstallLayout(dir string) (bool, error) {
	want := map[string]struct{}{
		binaryName(): {}, manifestName: {}, noticeName: {}, licenseName: {},
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, fmt.Errorf("read managed ffmpeg directory: %w", err)
	}
	if len(entries) != len(want) {
		return false, nil
	}
	for _, entry := range entries {
		if _, ok := want[entry.Name()]; !ok {
			return false, nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return false, fmt.Errorf("inspect managed ffmpeg file: %w", infoErr)
		}
		if !info.Mode().IsRegular() {
			return false, nil
		}
	}
	return true, nil
}

func writeInstallMetadata(dir string, manifest *installManifest) error {
	manifestData, err := marshalManifest(manifest)
	if err != nil {
		return err
	}

	files := []struct {
		name string
		data []byte
	}{
		{name: manifestName, data: manifestData},
		{name: noticeName, data: noticeFor(manifest)},
		{name: licenseName, data: gpl3License},
	}
	for _, file := range files {
		if writeErr := writeSynced(filepath.Join(dir, file.name), file.data, 0o600); writeErr != nil {
			return writeErr
		}
	}

	return nil
}

func writeSynced(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create managed ffmpeg metadata: %w", err)
	}

	_, copyErr := io.Copy(file, bytes.NewReader(data))
	syncErr := file.Sync()
	closeErr := file.Close()
	if err = errors.Join(copyErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("write managed ffmpeg metadata: %w", err)
	}

	return nil
}

func fileSHA256(path string) (digest string, err error) {
	info, err := os.Lstat(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("managed ffmpeg executable is not a regular file")
	}
	if info.Size() <= 0 || info.Size() > maxBinary {
		return "", fmt.Errorf("managed ffmpeg executable exceeds %d bytes", maxBinary)
	}

	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, file.Close()) }()

	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash managed ffmpeg: %w", err)
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

func publishInstall(stage, finalDir, platform string, b *build) error {
	renameErr := os.Rename(stage, finalDir)
	if renameErr == nil {
		return syncPublishedInstall(finalDir)
	}
	if complete, checkErr := installComplete(finalDir, platform, b); checkErr != nil {
		return checkErr
	} else if complete {
		return nil
	}

	if _, err := os.Stat(finalDir); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("activate ffmpeg install: %w", renameErr)
	} else if err != nil {
		return fmt.Errorf("inspect previous ffmpeg install: %w", err)
	}
	if err := os.RemoveAll(finalDir); err != nil {
		return fmt.Errorf("remove incomplete ffmpeg install: %w", err)
	}
	if err := os.Rename(stage, finalDir); err != nil {
		return fmt.Errorf("activate ffmpeg install: %w", err)
	}

	return syncPublishedInstall(finalDir)
}

func syncPublishedInstall(finalDir string) error {
	if err := syncDir(filepath.Dir(finalDir)); err != nil {
		return fmt.Errorf("sync ffmpeg install root: %w", err)
	}
	return nil
}

func cleanupManagedInstalls(stateDir string) error {
	root := managedRoot(stateDir)
	entries, err := os.ReadDir(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read managed ffmpeg installs: %w", err)
	}

	var cleanupErr error
	now := time.Now()
	for _, entry := range entries {
		cleanupErr = errors.Join(cleanupErr, cleanupManagedEntry(root, entry, now))
	}
	legacy := legacyInstalledPath(stateDir)
	if err = os.Remove(legacy); err != nil && !errors.Is(err, os.ErrNotExist) {
		cleanupErr = errors.Join(cleanupErr, err)
	}

	return cleanupErr
}

func cleanupManagedEntry(root string, entry os.DirEntry, now time.Time) error {
	if !entry.IsDir() {
		return nil
	}
	name := entry.Name()
	path := filepath.Join(root, name)
	if isSHA256(name) {
		return nil
	}
	if !strings.HasPrefix(name, ".install-") {
		return nil
	}
	if strings.HasSuffix(name, ".previous") {
		return os.RemoveAll(path)
	}
	info, err := entry.Info()
	if err != nil {
		return err
	}
	if now.Sub(info.ModTime()) < abandonedInstallAge {
		return nil
	}
	if removeErr := os.RemoveAll(path); removeErr != nil {
		return fmt.Errorf("remove abandoned ffmpeg stage %s: %w", name, removeErr)
	}

	return nil
}

func isSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)

	return err == nil && value == strings.ToLower(value)
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
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ResponseHeaderTimeout:  30 * time.Second,
		TLSHandshakeTimeout:    30 * time.Second,
		IdleConnTimeout:        90 * time.Second,
		ExpectContinueTimeout:  time.Second,
		MaxResponseHeaderBytes: 1 << 20,
		ForceAttemptHTTP2:      true,
		Proxy:                  http.ProxyFromEnvironment,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many ffmpeg download redirects")
		}
		if req.URL.Scheme != "https" {
			return errors.New("ffmpeg download redirect is not HTTPS")
		}
		return nil
	},
}

// download streams the archive to a temp file while hashing it, then
// verifies the pinned checksum before anything is trusted.
func download(ctx context.Context, b *build) (path string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.binaryURL, http.NoBody)
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
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			err = errors.Join(err, os.Remove(tmpPath))
		}
	}()

	hash := sha256.New()

	_, copyErr := copyBounded(io.MultiWriter(tmp, hash), resp.Body, maxArchive)
	closeErr := tmp.Close()

	if streamErr := errors.Join(copyErr, closeErr); streamErr != nil {
		return "", fmt.Errorf("save archive: %w", streamErr)
	}

	if got := hex.EncodeToString(hash.Sum(nil)); got != b.archiveSHA256 {
		return "", fmt.Errorf("checksum mismatch for %s: got %s — refusing to install", b.binaryURL, got)
	}

	keep = true

	return tmpPath, nil
}

// unpack extracts the ffmpeg binary from the verified archive to dest.
func unpack(archive, kind, dest string) error {
	info, err := os.Lstat(filepath.Clean(archive))
	if err != nil {
		return fmt.Errorf("inspect archive: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxArchive {
		return errors.New("ffmpeg archive is not a bounded regular file")
	}
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

	candidate, err := findZIPBinary(r.File)
	if err != nil {
		return err
	}
	rc, err := candidate.Open()
	if err != nil {
		return fmt.Errorf("open %s in archive: %w", candidate.Name, err)
	}
	return errors.Join(writeBinary(dest, rc), rc.Close())
}

func findZIPBinary(files []*zip.File) (*zip.File, error) {
	var candidate *zip.File
	for _, f := range files {
		if filepath.Base(f.Name) != binaryName() || f.FileInfo().IsDir() {
			continue
		}
		if !f.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is not a regular file", f.Name)
		}
		if candidate != nil {
			return nil, errors.New("archive contains duplicate ffmpeg binaries")
		}
		if f.UncompressedSize64 == 0 || f.UncompressedSize64 > maxBinary {
			return nil, fmt.Errorf("%s exceeds %d bytes", f.Name, maxBinary)
		}
		candidate = f
	}
	if candidate == nil {
		return nil, errors.New("archive holds no ffmpeg binary")
	}
	return candidate, nil
}

// unpackTarXz finds the ffmpeg entry in a tar.xz archive.
func unpackTarXz(archive, dest string) (err error) {
	return unpackTarXzBounded(archive, dest, maxExpandedArchive)
}

func unpackTarXzBounded(archive, dest string, expandedLimit int64) (err error) {
	if expandedLimit < 1 {
		return errors.New("expanded archive limit must be positive")
	}

	f, err := os.Open(filepath.Clean(archive))
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}

	defer func() { err = errors.Join(err, f.Close()) }()

	xr, xzErr := xz.NewReader(f)
	if xzErr != nil {
		return fmt.Errorf("open xz stream: %w", xzErr)
	}

	tr := tar.NewReader(&expandedArchiveReader{source: xr, remaining: expandedLimit})

	for {
		hdr, nextErr := tr.Next()
		if nextErr != nil {
			return tarReadError(nextErr, expandedLimit)
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != binaryName() {
			continue
		}

		return extractTarBinary(tr, hdr, dest, expandedLimit)
	}
}

func tarReadError(err error, expandedLimit int64) error {
	if errors.Is(err, io.EOF) {
		return errors.New("archive holds no ffmpeg binary")
	}
	if errors.Is(err, errExpandedArchive) {
		return fmt.Errorf("expanded archive exceeds %d bytes: %w", expandedLimit, err)
	}

	return fmt.Errorf("read archive: %w", err)
}

func extractTarBinary(tr io.Reader, hdr *tar.Header, dest string, expandedLimit int64) error {
	if hdr.Size <= 0 || hdr.Size > maxBinary {
		return fmt.Errorf("%s exceeds %d bytes", hdr.Name, maxBinary)
	}
	err := writeBinary(dest, tr)
	if errors.Is(err, errExpandedArchive) {
		return fmt.Errorf("expanded archive exceeds %d bytes: %w", expandedLimit, err)
	}

	return err
}

var errExpandedArchive = errors.New("expanded archive exceeds limit")

type expandedArchiveReader struct {
	source    io.Reader
	remaining int64
}

func (r *expandedArchiveReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.remaining < 0 {
		return 0, errExpandedArchive
	}

	readSize := min(int64(len(p)), r.remaining+1)
	n, err := r.source.Read(p[:readSize])
	if int64(n) > r.remaining {
		allowed := int(r.remaining)
		r.remaining = -1

		return allowed, errExpandedArchive
	}
	r.remaining -= int64(n)

	return n, err
}

// writeBinary lands the executable atomically: bounded copy to a unique
// sibling temp file (0700 — this package's authorization), then rename.
// A unique name keeps concurrent installers off each other's half-written
// file, and failures remove it instead of leaving debris.
func writeBinary(dest string, src io.Reader) (err error) {
	out, err := os.CreateTemp(filepath.Dir(dest), ".prukka-ffmpeg-*")
	if err != nil {
		return fmt.Errorf("create binary: %w", err)
	}

	tmp := out.Name()
	_, copyErr := copyBounded(out, src, maxBinary)
	chmodErr := os.Chmod(tmp, 0o700)
	syncErr := out.Sync()
	closeErr := out.Close()

	if streamErr := errors.Join(copyErr, chmodErr, syncErr, closeErr); streamErr != nil {
		return errors.Join(fmt.Errorf("write binary: %w", streamErr), os.Remove(tmp))
	}

	if renameErr := os.Rename(tmp, dest); renameErr != nil {
		return errors.Join(fmt.Errorf("activate binary: %w", renameErr), os.Remove(tmp))
	}

	return nil
}

// copyBounded observes at most limit+1 bytes so an exact-size payload is
// distinguishable from a truncated oversized one.
func copyBounded(dst io.Writer, src io.Reader, limit int64) (int64, error) {
	written, err := io.Copy(dst, io.LimitReader(src, limit+1))
	if err != nil {
		return written, err
	}
	if written > limit {
		return written, fmt.Errorf("payload exceeds %d bytes", limit)
	}

	return written, nil
}
