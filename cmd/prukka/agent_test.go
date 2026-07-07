package main

import (
	"strings"
	"testing"
)

// TestAgentIsAnHonestStub: until the desktop agent ships, running it must
// say so and point at the roadmap — never pretend to start.
func TestAgentIsAnHonestStub(t *testing.T) {
	t.Parallel()

	cmd := newAgentCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "later release") {
		t.Fatalf("agent = %v, want the honest not-yet error", err)
	}
}
