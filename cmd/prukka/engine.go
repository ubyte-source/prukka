package main

import (
	"github.com/spf13/cobra"

	"github.com/ubyte-source/prukka/internal/nativewire"
	"github.com/ubyte-source/prukka/internal/speechengine"
)

// newEngineCmds builds the hidden helper subcommands the daemon self-executes
// for native STT, MT and TTS; each runs no daemon initialization, so a re-exec
// has no side effect beyond serving the requested stdio protocol.
func newEngineCmds() []*cobra.Command {
	return []*cobra.Command{
		newEngineCmd(nativewire.SubSTT, speechengine.RunSTT),
		newEngineCmd(nativewire.SubMT, speechengine.RunMT),
		newEngineCmd(nativewire.SubTTS, speechengine.RunTTS),
	}
}

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
