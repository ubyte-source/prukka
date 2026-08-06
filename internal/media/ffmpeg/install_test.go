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

func TestResolveRejectsBareInstall(t *testing.T) {
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
		t.Fatalf("bare-install Resolve = (%q, %v), want a fail-closed setup instruction", path, err)
	}
}

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
