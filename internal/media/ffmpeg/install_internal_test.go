package ffmpeg

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ulikunitz/xz"
)

func TestCopyBoundedDistinguishesExactAndOversizedPayloads(t *testing.T) {
	t.Parallel()

	var exact bytes.Buffer
	if n, err := copyBounded(&exact, strings.NewReader("1234"), 4); err != nil || n != 4 {
		t.Fatalf("exact copy = (%d, %v)", n, err)
	}

	var oversized bytes.Buffer
	if n, err := copyBounded(&oversized, strings.NewReader("12345"), 4); err == nil || n != 5 {
		t.Fatalf("oversized copy = (%d, %v), want five bytes observed and an error", n, err)
	}
}

func TestTarXZExpandedStreamIsBoundedBeforeFFmpeg(t *testing.T) {
	t.Parallel()

	archive := tarXZPayload(t, []tarEntry{
		{name: "padding", body: bytes.Repeat([]byte("x"), 4096)},
		{name: binaryName(), body: []byte("ffmpeg")},
	})
	archivePath := filepath.Join(t.TempDir(), "ffmpeg.tar.xz")
	if err := os.WriteFile(archivePath, archive, 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	dest := filepath.Join(t.TempDir(), binaryName())

	err := unpackTarXzBounded(archivePath, dest, 1024)
	if !errors.Is(err, errExpandedArchive) {
		t.Fatalf("unpackTarXzBounded error = %v, want expanded-size rejection", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("destination exists after expanded-size rejection: %v", statErr)
	}

	validDest := filepath.Join(t.TempDir(), binaryName())
	if err = unpackTarXzBounded(archivePath, validDest, 1<<20); err != nil {
		t.Fatalf("bounded valid archive: %v", err)
	}
	if got := readTestFile(t, validDest); string(got) != "ffmpeg" {
		t.Fatalf("valid extracted executable = %q", got)
	}
}

func TestFileSHA256RejectsOversizedManagedExecutable(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("open test root: %v", err)
	}
	file, err := root.Create(binaryName())
	if err != nil {
		t.Fatalf("create sparse executable: %v", err)
	}
	if err = file.Truncate(maxBinary + 1); err != nil {
		t.Fatalf("size sparse executable: %v", err)
	}
	if err = file.Close(); err != nil {
		t.Fatalf("close sparse executable: %v", err)
	}
	if err = root.Close(); err != nil {
		t.Fatalf("close test root: %v", err)
	}

	path := filepath.Join(dir, binaryName())
	if _, err = fileSHA256(path); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("fileSHA256 oversized error = %v", err)
	}
}

func TestResolveAndInstallPreferVerifiedManagedOverPATH(t *testing.T) {
	pathDir := t.TempDir()
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	if err = os.Symlink(testExecutable, filepath.Join(pathDir, binaryName())); err != nil {
		t.Fatalf("create PATH ffmpeg: %v", err)
	}
	t.Setenv("PATH", pathDir)

	b, err := platformBuild()
	if err != nil {
		t.Fatalf("platformBuild: %v", err)
	}
	stateDir := t.TempDir()
	want := installedPathFor(stateDir, &b)
	writeCompleteInstall(t, installDirFor(stateDir, &b), platformKey(), &b, []byte("verified executable"))

	got, err := Resolve(stateDir)
	if err != nil || got != want {
		t.Fatalf("Resolve = (%q, %v), want %q", got, err, want)
	}
	got, err = Install(context.Background(), stateDir, io.Discard)
	if err != nil || got != want {
		t.Fatalf("Install = (%q, %v), want %q", got, err, want)
	}
}

func TestManagedInstallRequiresExecutableMode(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == osWindows {
		t.Skip("Windows executable mode is not encoded in permission bits")
	}

	b := testBuild("https://example.test/ffmpeg.zip", []byte("archive"))
	dir := filepath.Join(t.TempDir(), "managed")
	writeCompleteInstall(t, dir, "test/amd64", &b, []byte("executable"))
	if err := os.Chmod(filepath.Join(dir, binaryName()), 0o600); err != nil {
		t.Fatalf("remove executable mode: %v", err)
	}
	complete, err := installComplete(dir, "test/amd64", &b)
	if err != nil || complete {
		t.Fatalf("non-executable managed install = (%v, %v), want incomplete", complete, err)
	}
}

func TestResolveRejectsCorruptManagedInstallBeforePATH(t *testing.T) {
	pathDir := t.TempDir()
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	if err = os.Symlink(testExecutable, filepath.Join(pathDir, binaryName())); err != nil {
		t.Fatalf("create PATH ffmpeg: %v", err)
	}
	t.Setenv("PATH", pathDir)

	b, err := platformBuild()
	if err != nil {
		t.Fatalf("platformBuild: %v", err)
	}
	stateDir := t.TempDir()
	dir := installDirFor(stateDir, &b)
	writeCompleteInstall(t, dir, platformKey(), &b, []byte("verified executable"))
	if err = os.WriteFile(filepath.Join(dir, "unexpected"), []byte("tamper"), 0o600); err != nil {
		t.Fatalf("tamper with managed install: %v", err)
	}

	path, err := Resolve(stateDir)
	if err == nil || path != "" || !strings.Contains(err.Error(), "integrity verification") {
		t.Fatalf("Resolve corrupt managed install = (%q, %v)", path, err)
	}
}

func TestManagedInstallRequiresExactNonemptyLayout(t *testing.T) {
	t.Parallel()
	b := testBuild("https://example.test/ffmpeg.zip", []byte("archive"))
	dir := filepath.Join(t.TempDir(), "managed")
	writeCompleteInstall(t, dir, "test/amd64", &b, []byte("executable"))
	if err := os.WriteFile(filepath.Join(dir, "unexpected"), []byte("extra"), 0o600); err != nil {
		t.Fatalf("write unexpected file: %v", err)
	}
	complete, err := installComplete(dir, "test/amd64", &b)
	if err != nil || complete {
		t.Fatalf("managed install with extra file = (%v, %v), want incomplete", complete, err)
	}

	if err = os.RemoveAll(dir); err != nil {
		t.Fatalf("remove managed install: %v", err)
	}
	writeCompleteInstall(t, dir, "test/amd64", &b, []byte("executable"))
	if err = os.WriteFile(filepath.Join(dir, binaryName()), nil, 0o600); err != nil {
		t.Fatalf("empty managed executable: %v", err)
	}
	complete, err = installComplete(dir, "test/amd64", &b)
	if err != nil || complete {
		t.Fatalf("empty managed executable = (%v, %v), want incomplete", complete, err)
	}
}

func TestUnpackZIPRejectsDuplicateBinary(t *testing.T) {
	t.Parallel()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, name := range []string{"first/" + binaryName(), "second/" + binaryName()} {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("create duplicate zip entry: %v", err)
		}
		if _, err = entry.Write([]byte("binary")); err != nil {
			t.Fatalf("write duplicate zip entry: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close duplicate zip: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "ffmpeg.zip")
	if err := os.WriteFile(archivePath, archive.Bytes(), 0o600); err != nil {
		t.Fatalf("write duplicate zip: %v", err)
	}
	if err := unpack(archivePath, kindZip, filepath.Join(t.TempDir(), binaryName())); err == nil ||
		!strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate zip error = %v", err)
	}
}

func TestDownloadRejectsHTTPRedirect(t *testing.T) {
	t.Parallel()
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(target.Close)
	redirect := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusFound))
	t.Cleanup(redirect.Close)

	b := testBuild(redirect.URL, []byte("archive"))
	if _, err := download(context.Background(), &b); err == nil || !strings.Contains(err.Error(), "not HTTPS") {
		t.Fatalf("HTTP redirect error = %v", err)
	}
}

func TestInstallBuildPublishesCompleteDeterministicMetadata(t *testing.T) {
	t.Parallel()

	executable := []byte("test ffmpeg executable")
	archive := zipPayload(t, binaryName(), executable)
	var requests atomic.Int32
	server := archiveServer(t, archive, &requests)

	b := testBuild(server.URL+"/ffmpeg.zip", archive)
	stateDir := t.TempDir()
	finalDir := installDirFor(stateDir, &b)
	plantInstallDebris(t, stateDir, finalDir)

	path, err := installBuild(context.Background(), stateDir, "test/amd64", &b, io.Discard)
	if err != nil {
		t.Fatalf("installBuild: %v", err)
	}
	if path != filepath.Join(finalDir, binaryName()) {
		t.Fatalf("installed path = %q", path)
	}
	assertInstalledMetadata(t, finalDir, executable, &b)

	if _, err = installBuild(context.Background(), stateDir, "test/amd64", &b, io.Discard); err != nil {
		t.Fatalf("repeat installBuild: %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("download requests = %d, want 1", got)
	}
	if _, err = os.Stat(legacyInstalledPath(stateDir)); !os.IsNotExist(err) {
		t.Fatalf("legacy install was not removed: %v", err)
	}
	assertManagedEntries(t, stateDir, b.archiveSHA256, strings.Repeat("a", 64))
}

func TestRecoverInterruptedPublication(t *testing.T) {
	t.Parallel()

	b := testBuild("https://example.test/ffmpeg.zip", []byte("archive"))
	stateDir := t.TempDir()
	root := managedRoot(stateDir)
	stage := filepath.Join(root, ".install-crashed")
	backup := stage + ".previous"
	if err := os.MkdirAll(backup, 0o700); err != nil {
		t.Fatalf("create interrupted backup: %v", err)
	}
	writeCompleteInstall(t, stage, "test/amd64", &b, []byte("recovered executable"))

	if err := recoverInterruptedInstall(stateDir, "test/amd64", &b); err != nil {
		t.Fatalf("recoverInterruptedInstall: %v", err)
	}
	complete, err := managedInstallComplete(stateDir, "test/amd64", &b)
	if err != nil || !complete {
		t.Fatalf("recovered install complete = (%v, %v)", complete, err)
	}
	if _, err = os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("stage remains after recovery: %v", err)
	}
	if _, err = os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("backup remains after recovery: %v", err)
	}
}

func TestInstallBuildFailurePreservesPreviousInstall(t *testing.T) {
	t.Parallel()

	archive := zipPayload(t, "not-ffmpeg", []byte("wrong entry"))
	var requests atomic.Int32
	server := archiveServer(t, archive, &requests)

	b := testBuild(server.URL+"/ffmpeg.zip", archive)
	stateDir := t.TempDir()
	finalDir := installDirFor(stateDir, &b)
	if err := os.MkdirAll(finalDir, 0o700); err != nil {
		t.Fatalf("create previous install: %v", err)
	}
	previous := []byte("previous executable")
	previousPath := filepath.Join(finalDir, binaryName())
	if err := os.WriteFile(previousPath, previous, 0o600); err != nil {
		t.Fatalf("write previous install: %v", err)
	}

	if _, err := installBuild(context.Background(), stateDir, "test/amd64", &b, io.Discard); err == nil {
		t.Fatal("installBuild accepted an archive without ffmpeg")
	}
	if got := readTestFile(t, previousPath); !bytes.Equal(got, previous) {
		t.Fatalf("previous executable after failure = %q", got)
	}
	assertManagedEntries(t, stateDir, b.archiveSHA256)
}

func archiveServer(t *testing.T, archive []byte, requests *atomic.Int32) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/zip")
		if _, err := w.Write(archive); err != nil {
			t.Errorf("serve test archive: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	return server
}

func plantInstallDebris(t *testing.T, stateDir, finalDir string) {
	t.Helper()

	if err := os.MkdirAll(finalDir, 0o700); err != nil {
		t.Fatalf("create incomplete install: %v", err)
	}
	if err := os.WriteFile(filepath.Join(finalDir, binaryName()), []byte("old"), 0o600); err != nil {
		t.Fatalf("write incomplete install: %v", err)
	}
	staleDir := filepath.Join(managedRoot(stateDir), strings.Repeat("a", 64))
	if err := os.MkdirAll(staleDir, 0o700); err != nil {
		t.Fatalf("create stale install: %v", err)
	}
	if err := os.WriteFile(legacyInstalledPath(stateDir), []byte("legacy"), 0o600); err != nil {
		t.Fatalf("write legacy install: %v", err)
	}
	abandoned := filepath.Join(managedRoot(stateDir), ".install-abandoned")
	if err := os.MkdirAll(abandoned, 0o700); err != nil {
		t.Fatalf("create abandoned stage: %v", err)
	}
	old := time.Now().Add(-2 * abandonedInstallAge)
	if err := os.Chtimes(abandoned, old, old); err != nil {
		t.Fatalf("age abandoned stage: %v", err)
	}
}

func assertInstalledMetadata(t *testing.T, dir string, executable []byte, b *build) {
	t.Helper()

	if got := readTestFile(t, filepath.Join(dir, binaryName())); !bytes.Equal(got, executable) {
		t.Fatalf("installed executable = %q", got)
	}
	manifestData := readTestFile(t, filepath.Join(dir, manifestName))
	var manifest installManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	want := manifestFor("test/amd64", b, sha256Hex(executable))
	if manifest != want {
		t.Fatalf("manifest = %+v", manifest)
	}
	if notice := readTestFile(t, filepath.Join(dir, noticeName)); !bytes.Equal(notice, noticeFor(&manifest)) {
		t.Fatal("installed notice is not the deterministic manifest rendering")
	}
	if license := readTestFile(t, filepath.Join(dir, licenseName)); !bytes.Equal(license, gpl3License) {
		t.Fatal("installed GPL text differs from embedded source")
	}
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()

	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	data, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if err = errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return data
}

func testBuild(binaryURL string, archive []byte) build {
	return build{
		vendor:         "Test Builder",
		version:        "1.2.3",
		commit:         strings.Repeat("1", 40),
		license:        ffmpegLicense,
		binaryURL:      binaryURL,
		archiveSHA256:  sha256Hex(archive),
		kind:           kindZip,
		sourceURL:      "https://example.test/ffmpeg-1.2.3.tar.xz",
		sourceSHA256:   strings.Repeat("2", 64),
		recipeURL:      "https://example.test/recipe/" + strings.Repeat("3", 40),
		recipeRevision: strings.Repeat("3", 40),
		buildInfoURL:   "https://example.test/build/1.2.3/info.txt",
		buildConfig:    "./build.sh release",
	}
}

func zipPayload(t *testing.T, name string, body []byte) []byte {
	t.Helper()

	var out bytes.Buffer
	archive := zip.NewWriter(&out)
	entry, err := archive.Create(name)
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err = entry.Write(body); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
	if err = archive.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}

	return out.Bytes()
}

type tarEntry struct {
	name string
	body []byte
}

func tarXZPayload(t *testing.T, entries []tarEntry) []byte {
	t.Helper()

	var out bytes.Buffer
	xzWriter, err := xz.NewWriter(&out)
	if err != nil {
		t.Fatalf("create xz writer: %v", err)
	}
	tarWriter := tar.NewWriter(xzWriter)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Mode: 0o600, Size: int64(len(entry.body))}
		if err = tarWriter.WriteHeader(header); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if _, err = tarWriter.Write(entry.body); err != nil {
			t.Fatalf("write tar body: %v", err)
		}
	}
	if err = tarWriter.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err = xzWriter.Close(); err != nil {
		t.Fatalf("close xz writer: %v", err)
	}

	return out.Bytes()
}

func writeCompleteInstall(t *testing.T, dir, platform string, b *build, executable []byte) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create complete install: %v", err)
	}
	executablePath := filepath.Join(dir, binaryName())
	if err := writeBinary(executablePath, bytes.NewReader(executable)); err != nil {
		t.Fatalf("write complete executable: %v", err)
	}
	manifest := manifestFor(platform, b, sha256Hex(executable))
	if err := writeInstallMetadata(dir, &manifest); err != nil {
		t.Fatalf("write complete metadata: %v", err)
	}
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)

	return hex.EncodeToString(digest[:])
}

func assertManagedEntries(t *testing.T, stateDir string, expected ...string) {
	t.Helper()

	entries, err := os.ReadDir(managedRoot(stateDir))
	if err != nil {
		t.Fatalf("read managed root: %v", err)
	}
	want := make(map[string]struct{}, len(expected))
	for _, name := range expected {
		want[name] = struct{}{}
	}
	if len(entries) != len(want) {
		t.Fatalf("managed root entries = %v, want %v", entries, expected)
	}
	for _, entry := range entries {
		if _, ok := want[entry.Name()]; !ok {
			t.Fatalf("unexpected managed root entry %s; want %v", entry.Name(), expected)
		}
	}
}
