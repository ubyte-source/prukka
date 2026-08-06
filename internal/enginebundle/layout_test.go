package enginebundle_test

import (
	"path/filepath"
	"testing"

	"github.com/ubyte-source/prukka/internal/enginebundle"
)

// These pin the layout the installer writes and the engine helpers read: a
// rename here is a deliberate, reviewed change to the on-disk contract.

func TestMTPackNameAndDirShareTheIdentifier(t *testing.T) {
	t.Parallel()

	if got := enginebundle.MTPackName("it", "en"); got != "mt-it-en" {
		t.Fatalf("MTPackName = %q, want mt-it-en", got)
	}
	if got := enginebundle.MTModelDir("it", "en"); got != filepath.Join("models", "mt-it-en") {
		t.Fatalf("MTModelDir = %q, want models/mt-it-en", got)
	}
}

func TestPiperIsUnderItsOwnDirectory(t *testing.T) {
	t.Parallel()

	if got := enginebundle.Piper(); got != filepath.Join("piper", "piper") {
		t.Fatalf("Piper = %q, want piper/piper", got)
	}
}
