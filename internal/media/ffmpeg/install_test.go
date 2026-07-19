package ffmpeg_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
)

func TestResolveEmptyStateShowsSetup(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no system ffmpeg visible

	if _, err := ffmpeg.Resolve(t.TempDir()); err == nil ||
		!strings.Contains(err.Error(), "prukka setup") {
		t.Fatalf("empty state resolved to (%v), want the setup hint", err)
	}
}

// TestResolveRejectsLegacyManagedInstall: an executable from the old flat
// layout has no provenance manifest and must be replaced explicitly.
func TestResolveRejectsLegacyManagedInstall(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	state := t.TempDir()
	managed := filepath.Join(state, "bin")
	if err := os.MkdirAll(managed, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	name := "ffmpeg"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(managed, name), []byte("#!"), 0o700); err != nil {
		t.Fatalf("plant %s: %v", name, err)
	}

	if path, err := ffmpeg.Resolve(state); err == nil || path != "" ||
		!strings.Contains(err.Error(), "verified manifest") ||
		!strings.Contains(err.Error(), "prukka setup") {
		t.Fatalf("legacy Resolve = (%q, %v), want a provenance migration error", path, err)
	}
}

// TestResolveFallsBackToPATH: a system ffmpeg works without a managed install.
func TestResolveFallsBackToPATH(t *testing.T) {
	bin := t.TempDir()

	// LookPath on Windows resolves executables by PATHEXT suffix.
	fake := filepath.Join(bin, "ffmpeg")
	if runtime.GOOS == "windows" {
		fake += ".exe"
	}

	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("plant fake ffmpeg: %v", err)
	}

	t.Setenv("PATH", bin)

	path, err := ffmpeg.Resolve(t.TempDir())
	if err != nil || path != fake {
		t.Fatalf("Resolve = (%q, %v), want the PATH binary %q", path, err, fake)
	}
}
