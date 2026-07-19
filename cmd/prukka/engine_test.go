package main

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestEngineCommandsAreHiddenPassThroughHelpers(t *testing.T) {
	t.Parallel()

	cmds := newEngineCmds()
	want := map[string]bool{"stt": true, "mt": true, "tts": true}
	if len(cmds) != len(want) {
		t.Fatalf("engine commands = %d, want %d", len(cmds), len(want))
	}
	for _, c := range cmds {
		if !want[c.Use] {
			t.Fatalf("unexpected engine command %q", c.Use)
		}
		if !c.Hidden || !c.DisableFlagParsing {
			t.Fatalf("%s: Hidden=%v DisableFlagParsing=%v, want both true", c.Use, c.Hidden, c.DisableFlagParsing)
		}
	}
}

// The helper commands delegate to the engine entrypoints: an unparseable flag
// must surface as an error, not be swallowed by cobra's own flag handling.
func TestEngineCommandsDelegateErrors(t *testing.T) {
	t.Parallel()

	byVerb := map[string]*cobra.Command{}
	for _, c := range newEngineCmds() {
		byVerb[c.Use] = c
	}
	for _, verb := range []string{"stt", "mt", "tts"} {
		cmd := byVerb[verb]
		if err := cmd.RunE(cmd, []string{"--not-a-real-flag"}); err == nil {
			t.Fatalf("%s: unknown flag must error", verb)
		}
	}
}
