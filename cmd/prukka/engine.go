package main

import (
	"github.com/spf13/cobra"

	"github.com/ubyte-source/prukka/internal/engine"
)

// newEngineCmds builds the hidden helper subcommands the daemon self-executes
// for native STT, MT and TTS. Each parses its own flags (DisableFlagParsing)
// and runs no daemon initialization, so re-executing the prukka binary as a
// helper has no side effect beyond serving the requested stdio protocol.
func newEngineCmds() []*cobra.Command {
	return []*cobra.Command{
		newEngineCmd("stt", engine.RunSTT),
		newEngineCmd("mt", engine.RunMT),
		newEngineCmd("tts", engine.RunTTS),
	}
}

// newEngineCmd wraps one engine entrypoint as a hidden pass-through command.
func newEngineCmd(verb string, run func([]string) error) *cobra.Command {
	return &cobra.Command{
		Use:                verb,
		Hidden:             true,
		DisableFlagParsing: true,
		SilenceUsage:       true,
		RunE: func(_ *cobra.Command, args []string) error {
			return run(args)
		},
	}
}
