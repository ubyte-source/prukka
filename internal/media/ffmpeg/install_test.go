package ffmpeg_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
)

// TestResolveFindsTheManagedInstall: with no ffmpeg on PATH, Resolve must
// return the state-dir binary when present and a setup hint when not.
func TestResolveFindsTheManagedInstall(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no system ffmpeg visible

	state := t.TempDir()

	if _, err := ffmpeg.Resolve(state); err == nil ||
		!strings.Contains(err.Error(), "prukka setup") {
		t.Fatalf("empty state resolved to (%v), want the setup hint", err)
	}

	managed := filepath.Join(state, "bin")
	if err := os.MkdirAll(managed, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	names, readErr := os.ReadDir(managed)
	if readErr != nil || len(names) != 0 {
		t.Fatalf("fresh managed dir not empty: %v %v", names, readErr)
	}

	// The managed name is platform-specific; plant both spellings.
	for _, name := range []string{"ffmpeg", "ffmpeg.exe"} {
		if err := os.WriteFile(filepath.Join(managed, name), []byte("#!"), 0o700); err != nil {
			t.Fatalf("plant %s: %v", name, err)
		}
	}

	path, err := ffmpeg.Resolve(state)
	if err != nil {
		t.Fatalf("Resolve with a managed install returned error: %v", err)
	}

	if !strings.HasPrefix(path, managed) {
		t.Fatalf("Resolve = %q, want the managed path under %q", path, managed)
	}
}

// TestResolvePrefersPATH: a system ffmpeg wins over the managed install.
func TestResolvePrefersPATH(t *testing.T) {
	bin := t.TempDir()
	fake := filepath.Join(bin, "ffmpeg")

	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("plant fake ffmpeg: %v", err)
	}

	t.Setenv("PATH", bin)

	path, err := ffmpeg.Resolve(t.TempDir())
	if err != nil || path != fake {
		t.Fatalf("Resolve = (%q, %v), want the PATH binary %q", path, err, fake)
	}
}
