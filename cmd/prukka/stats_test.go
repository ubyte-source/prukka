package main

import (
	"strings"
	"testing"
)

func TestStatsNeedsAnInitializedInstall(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	cmd := newStatsCmd(&rootFlags{})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "control token") {
		t.Fatalf("stats without a token = %v, want the missing-token hint", err)
	}
}
