package control_test

import (
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/control"
	"github.com/ubyte-source/prukka/internal/core/config"
)

// TestDialNeedsAnInitializedInstall: without the per-install token, Dial
// must point the user at starting the daemon instead of dialing blind.
func TestDialNeedsAnInitializedInstall(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	if _, err := control.Dial(config.Default()); err == nil ||
		!strings.Contains(err.Error(), "control token") {
		t.Fatalf("Dial without a token = %v, want the missing-token hint", err)
	}
}

// TestDialReturnsALazyConnection: connections are lazy — the RPC, not
// Dial, fails when nobody listens.
func TestDialReturnsALazyConnection(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	if _, err := control.LoadOrCreateToken(config.TokenPath()); err != nil {
		t.Fatalf("mint token: %v", err)
	}

	conn, err := control.Dial(config.Default())
	if err != nil {
		t.Fatalf("Dial with a token returned error: %v", err)
	}

	if closeErr := conn.Close(); closeErr != nil {
		t.Fatalf("closing the lazy connection: %v", closeErr)
	}
}
