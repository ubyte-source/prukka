package main

import (
	"io"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/observability"
)

// rootFlags carries the persistent flags shared by every subcommand.
type rootFlags struct {
	config   string
	logLevel string
}

// load resolves a one-shot configuration snapshot and logger from the
// persistent flags.
func (f *rootFlags) load() (*config.Config, *slog.Logger, error) {
	cfg, err := config.Load(f.config)
	if err != nil {
		return nil, nil, err
	}

	log, logErr := f.logger()
	if logErr != nil {
		return nil, nil, logErr
	}

	return cfg, log, nil
}

// holder resolves a live, reloadable configuration for the daemon.
func (f *rootFlags) holder() (*config.Holder, *slog.Logger, error) {
	h, err := config.NewHolder(f.config)
	if err != nil {
		return nil, nil, err
	}

	log, logErr := f.logger()
	if logErr != nil {
		return nil, nil, logErr
	}

	return h, log, nil
}

// logger builds the process logger from the persistent flags.
func (f *rootFlags) logger() (*slog.Logger, error) {
	return f.loggerTo(os.Stderr)
}

// loggerTo builds the process logger onto w with the configured level.
func (f *rootFlags) loggerTo(w io.Writer) (*slog.Logger, error) {
	level, err := observability.ParseLevel(f.logLevel)
	if err != nil {
		return nil, err
	}

	return observability.NewLogger(w, level, "prukka"), nil
}

// newRootCmd builds the CLI tree.
func newRootCmd() *cobra.Command {
	flags := &rootFlags{}

	cmd := &cobra.Command{
		Use:   "prukka",
		Short: "Every stream, every language — one bridge.",
		Long: "Prukka is a real-time multilingual dubbing and interpretation engine.\n" +
			"Docs: https://github.com/ubyte-source/prukka",
		Version:      version + " (" + commit + ")",
		SilenceUsage: true,
	}
	// Without this, cobra's Print* fall back to stderr, so every command RESULT
	// lands there.
	cmd.SetOut(os.Stdout)

	// pflag reads the FIRST back-quoted span of a usage string as the flag's
	// value name; every later pair stays literal prose.
	cmd.PersistentFlags().StringVar(&flags.config, "config", "",
		"`path` to the config file (default: the platform location, `prukka doctor` prints it)")
	cmd.PersistentFlags().StringVar(&flags.logLevel, "log-level", "info", "log level: debug, info, warn or error")

	cmd.AddCommand(
		newDaemonCmd(flags),
		newUpCmd(flags),
		newTrayCmd(flags),
		newSessionCmd(flags),
		newDoctorCmd(flags),
		newServiceCmd(flags),
		newDevicesCmd(),
		newStatsCmd(flags),
		newSetupCmd(flags),
		newUpdateCmd(),
		newVersionCmd(),
	)
	cmd.AddCommand(newEngineCmds()...)

	return cmd
}

// must panics on a flag-registration error, at startup rather than at use.
func must(err error) {
	if err != nil {
		panic(err)
	}
}
