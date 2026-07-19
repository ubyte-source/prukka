package main

import (
	"strings"
	"testing"
)

// TestTrayCommandShape: tray is wired, documented and argument-free; its
// body needs a windowed session, not a test.
func TestTrayCommandShape(t *testing.T) {
	t.Parallel()

	cmd := newTrayCmd(&rootFlags{})

	if cmd.Use != "tray" || cmd.RunE == nil {
		t.Fatalf("tray command miswired: Use=%q, RunE nil", cmd.Use)
	}

	if !strings.Contains(strings.ToLower(cmd.Short), "tray") {
		t.Fatalf("tray Short %q does not describe itself", cmd.Short)
	}

	if err := cmd.Args(cmd, []string{"extra"}); err == nil {
		t.Fatal("tray accepted positional arguments")
	}
}
