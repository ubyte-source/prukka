package ffmpeg

import (
	"testing"
)

func TestPlatformBuildAndBinaryName(t *testing.T) {
	t.Parallel()

	// The host running the tests is one of the six supported targets.
	b, err := platformBuild()
	if err != nil {
		t.Fatalf("platformBuild returned error on a supported host: %v", err)
	}

	if b.url == "" || b.sha256 == "" || (b.kind != kindZip && b.kind != kindTarXz) {
		t.Fatalf("platform build is incomplete: %+v", b)
	}

	if name := binaryName(); name != "ffmpeg" && name != "ffmpeg.exe" {
		t.Fatalf("binaryName = %q", name)
	}
}
