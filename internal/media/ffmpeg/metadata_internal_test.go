package ffmpeg

import (
	"bytes"
	"testing"
)

func TestMetadataAssetsAreDeterministic(t *testing.T) {
	t.Parallel()

	const officialGPLSHA256 = "3972dc9744f6499f0f9b2dbf76696f2ae7ad8af9b23dde66d6af86c9dfb36986"
	if got := sha256Hex(gpl3License); got != officialGPLSHA256 {
		t.Fatalf("embedded GPL-3.0 digest = %s, want %s", got, officialGPLSHA256)
	}

	b := testBuild("https://example.test/ffmpeg.zip", []byte("archive"))
	manifest := manifestFor("test/amd64", &b, sha256Hex([]byte("executable")))
	first, err := marshalManifest(&manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	second, err := marshalManifest(&manifest)
	if err != nil {
		t.Fatalf("marshal manifest again: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("manifest rendering is not deterministic")
	}
	firstNotice := noticeFor(&manifest)
	secondNotice := noticeFor(&manifest)
	if !bytes.Equal(firstNotice, secondNotice) {
		t.Fatal("notice rendering is not deterministic")
	}
	if manifest.DistributionMode != distributionMode || manifest.MirrorStatus != mirrorStatus {
		t.Fatalf("distribution metadata = (%q, %q)", manifest.DistributionMode, manifest.MirrorStatus)
	}
}
