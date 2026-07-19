//go:build darwin

package ffmpeg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNativeVideoFeederUsesInstalledApplication(t *testing.T) {
	t.Parallel()

	want := filepath.Join(darwinCameraApp, "Contents", "MacOS", "prukka-camfeed")
	if got := nativeVideoFeeder(); got != want {
		t.Fatalf("feeder = %q, want %q", got, want)
	}

	info, err := os.Stat(want)
	wantInstalled := err == nil && !info.IsDir() && info.Mode()&0o111 != 0
	if got := nativeVideoFeederInstalled(); got != wantInstalled {
		t.Fatalf("installed = %t, want %t", got, wantInstalled)
	}
}
