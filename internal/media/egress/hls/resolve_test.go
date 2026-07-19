package hls_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
)

func TestOpenServesOnlyCanonicalTreeFiles(t *testing.T) {
	t.Parallel()

	store := newStore(t)

	session, err := store.Create("demo", []core.Lang{"en"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Materialize a few canonical files the way the writers would.
	for _, path := range []string{
		filepath.Join(session.VideoDir(), "index.m3u8"),
		filepath.Join(session.VideoDir(), "seg00002.ts"),
		filepath.Join(session.AudioDir("en"), "seg00000.ts"),
	} {
		if writeErr := os.WriteFile(path, []byte("x"), 0o600); writeErr != nil {
			t.Fatalf("write %s: %v", path, writeErr)
		}
	}

	for _, ok := range []string{"video/index.m3u8", "video/seg00002.ts", "audio/en/seg00000.ts"} {
		f, found := store.Open("demo", ok)
		if !found {
			t.Errorf("Open(%q) = miss, want the file", ok)

			continue
		}

		if closeErr := f.Close(); closeErr != nil {
			t.Errorf("close %q: %v", ok, closeErr)
		}
	}

	rejected := []string{
		"video/../../../../etc/passwd",
		"video/..%2f..%2fetc/passwd",
		"audio/en/../../video/index.m3u8",
		"audio/xx/seg00000.ts",   // language the session does not carry
		"video/seg2.ts",          // non-canonical segment name
		"video/seg000002.ts",     // wrong width
		"video/seg-0002.ts",      // negative
		"video/evil.ts",          // arbitrary name
		"subs/en/../index.m3u8",  // climbs the tree
		"other/en/seg00000.ts",   // unknown kind
		"video/index.m3u8/extra", // too deep
	}

	for _, bad := range rejected {
		if _, found := store.Open("demo", bad); found {
			t.Errorf("Open(%q) succeeded; the tree must be traversal-proof", bad)
		}
	}

	if _, found := store.Open("ghost", "video/index.m3u8"); found {
		t.Error("Open on an unknown session succeeded")
	}
}
