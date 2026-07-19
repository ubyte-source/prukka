package ffmpeg

import (
	"encoding/hex"
	"net/url"
	"strings"
	"testing"
)

func TestBuildMatrixIsCompleteAndImmutable(t *testing.T) {
	t.Parallel()

	want := []string{
		"darwin/amd64",
		"darwin/arm64",
		"linux/amd64",
		"linux/arm64",
		"windows/amd64",
		"windows/arm64",
	}
	if len(builds) != len(want) {
		t.Fatalf("build count = %d, want %d", len(builds), len(want))
	}

	for _, platform := range want {
		b, ok := builds[platform]
		if !ok {
			t.Fatalf("missing build for %s", platform)
		}
		validateBuild(t, platform, &b)
	}
}

func validateBuild(t *testing.T, platform string, b *build) {
	t.Helper()
	validateBuildFields(t, platform, b)
	validateDigest(t, platform+" archive", b.archiveSHA256)
	validateDigest(t, platform+" source", b.sourceSHA256)
	validateRevision(t, platform+" FFmpeg", b.commit)
	validateRevision(t, platform+" recipe", b.recipeRevision)
	validateBuildURLs(t, platform, b)

	if b.license != ffmpegLicense {
		t.Fatalf("%s license = %q, want %q", platform, b.license, ffmpegLicense)
	}
	if b.kind != kindZip && b.kind != kindTarXz {
		t.Fatalf("%s archive kind = %q", platform, b.kind)
	}
	if strings.Contains(strings.ToLower(b.binaryURL), "evermeet") {
		t.Fatalf("%s uses a binary without a pinned public build recipe", platform)
	}
}

func validateBuildFields(t *testing.T, platform string, b *build) {
	t.Helper()

	required := map[string]string{
		"vendor":          b.vendor,
		"version":         b.version,
		"commit":          b.commit,
		"license":         b.license,
		"binary URL":      b.binaryURL,
		"archive SHA-256": b.archiveSHA256,
		"source URL":      b.sourceURL,
		"source SHA-256":  b.sourceSHA256,
		"recipe URL":      b.recipeURL,
		"recipe revision": b.recipeRevision,
		"build info URL":  b.buildInfoURL,
		"build config":    b.buildConfig,
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			t.Fatalf("%s: %s is empty", platform, name)
		}
	}
}

func validateBuildURLs(t *testing.T, platform string, b *build) {
	t.Helper()

	validateHTTPS(t, platform+" binary", b.binaryURL)
	validateHTTPS(t, platform+" source", b.sourceURL)
	validateHTTPS(t, platform+" recipe", b.recipeURL)
	validateHTTPS(t, platform+" build info", b.buildInfoURL)

	if !strings.Contains(b.recipeURL, b.recipeRevision) {
		t.Fatalf("%s recipe URL does not pin revision %s", platform, b.recipeRevision)
	}
	for _, value := range []string{b.binaryURL, b.sourceURL, b.recipeURL, b.buildInfoURL} {
		if strings.Contains(value, "/latest") || strings.Contains(value, "/master") {
			t.Fatalf("%s contains a mutable URL: %s", platform, value)
		}
	}
}

func validateDigest(t *testing.T, name, digest string) {
	t.Helper()

	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("%s digest = %q", name, digest)
	}
}

func validateRevision(t *testing.T, name, revision string) {
	t.Helper()

	decoded, err := hex.DecodeString(revision)
	if err != nil || len(decoded) != 20 {
		t.Fatalf("%s revision = %q", name, revision)
	}
}

func validateHTTPS(t *testing.T, name, raw string) {
	t.Helper()

	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		t.Fatalf("%s URL = %q", name, raw)
	}
}

func TestPlatformBuildAndBinaryName(t *testing.T) {
	t.Parallel()

	if _, err := platformBuild(); err != nil {
		t.Fatalf("platformBuild returned error on a supported host: %v", err)
	}

	if name := binaryName(); name != "ffmpeg" && name != "ffmpeg.exe" {
		t.Fatalf("binaryName = %q", name)
	}
}
