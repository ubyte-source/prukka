package main

import (
	"strings"
	"testing"
)

// TestSetupCommandShape: setup is wired, documented and argument-free.
func TestSetupCommandShape(t *testing.T) {
	t.Parallel()

	cmd := newSetupCmd(&rootFlags{})

	if cmd.Use != "setup" || cmd.RunE == nil {
		t.Fatalf("setup command miswired: Use=%q, RunE nil", cmd.Use)
	}

	if !strings.Contains(strings.ToLower(cmd.Short), "ffmpeg") {
		t.Fatalf("setup Short %q does not say what it installs", cmd.Short)
	}

	if err := cmd.Args(cmd, []string{"extra"}); err == nil {
		t.Fatal("setup accepted positional arguments")
	}
}
