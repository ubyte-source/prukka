package main

import (
	"strings"
	"testing"
)

// TestUpdateCommandShape: update is wired, explicit and argument-free.
func TestUpdateCommandShape(t *testing.T) {
	t.Parallel()

	cmd := newUpdateCmd()

	if cmd.Use != "update" || cmd.RunE == nil {
		t.Fatalf("update command miswired: Use=%q, RunE nil", cmd.Use)
	}

	if !strings.Contains(strings.ToLower(cmd.Short), "update") {
		t.Fatalf("update Short %q does not describe itself", cmd.Short)
	}

	if err := cmd.Args(cmd, []string{"extra"}); err == nil {
		t.Fatal("update accepted positional arguments")
	}
}
