package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
	"github.com/ubyte-source/prukka/internal/paths"
	"github.com/ubyte-source/prukka/internal/speech"
)

type setupInstallFunc func(context.Context, string, io.Writer) (string, error)

// setupEngineFunc installs the managed speech engine and the model packs
// the configuration needs; injected so tests never touch the network.
type setupEngineFunc func(ctx context.Context, cfg *config.Config, progress io.Writer) error

// newSetupCmd installs every runtime dependency: the pinned, checksum-verified
// static ffmpeg, then the managed speech engine with the configured models.
func newSetupCmd(flags *rootFlags) *cobra.Command {
	return newSetupCommand(flags, ffmpeg.Install, installEngine)
}

func newSetupCommand(flags *rootFlags, install setupInstallFunc, engine setupEngineFunc) *cobra.Command {
	var printPath bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Install the managed FFmpeg and speech-engine dependencies",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			progress := cmd.OutOrStdout()
			if printPath {
				progress = io.Discard
			}
			path, err := install(cmd.Context(), paths.StateDir(), progress)
			if err != nil {
				return err
			}
			if printPath {
				// Scripts capture stdout; cobra's own Println would land
				// on stderr. The engine phase stays out of this machine
				// path: CI probes ffmpeg without pulling models.
				if _, printErr := fmt.Fprintln(cmd.OutOrStdout(), path); printErr != nil {
					return printErr
				}

				return nil
			}
			cmd.Printf("ready — ffmpeg at %s\n", path)

			cfg, _, err := flags.load()
			if err != nil {
				return err
			}
			if err := engine(cmd.Context(), cfg, progress); err != nil {
				return err
			}
			cmd.Printf("ready — speech engine at %s\n", enginePathLabel(cfg))

			return nil
		},
	}
	cmd.Flags().BoolVar(&printPath, "print-path", false, "print only the resolved ffmpeg path")

	return cmd
}

// installEngine downloads the managed engine runtime and every model pack
// the configuration requires. An operator-managed bundle (explicit
// providers.local.bin) opts out of the managed install entirely.
func installEngine(ctx context.Context, cfg *config.Config, progress io.Writer) error {
	if strings.TrimSpace(cfg.Providers.Local.Bin) != "" {
		fprintQuietly(progress, "speech engine: managed install skipped (providers.local.bin is set)\n")

		return nil
	}

	catalogURL, err := speech.CatalogURL(version)
	if err != nil {
		return err
	}
	client := speech.NewClient(catalogURL)
	installer := speech.NewInstaller(paths.StateDir(), client, speech.WriterReporter(progress))
	catalog, err := client.Catalog(ctx)
	if err != nil {
		return fmt.Errorf("fetch engine catalog: %w", err)
	}
	if _, err := installer.EnsureRuntime(ctx, catalog); err != nil {
		return fmt.Errorf("install engine runtime: %w", err)
	}
	for _, id := range requiredPackIDs(cfg) {
		if err := installer.InstallPack(ctx, catalog, id); err != nil {
			return fmt.Errorf("install %s: %w", id, err)
		}
	}

	return nil
}

// requiredPackIDs maps the configured capabilities onto catalog packs: the
// mandatory STT models, one pack per translation route, one per voice.
func requiredPackIDs(cfg *config.Config) []string {
	local := &cfg.Providers.Local
	ids := []string{speech.PackIDSTTCore}
	for _, pair := range local.MT.Pairs {
		ids = append(ids, speech.MTPackID(string(pair.From), string(pair.To)))
	}
	if cfg.Providers.Voices == config.VoicesLocal {
		for _, voice := range local.TTS.Voices {
			ids = append(ids, speech.VoicePackID(string(voice.Language)))
		}
	}

	return ids
}

// enginePathLabel names the engine the daemon will spawn after this setup.
func enginePathLabel(cfg *config.Config) string {
	if bin := strings.TrimSpace(cfg.Providers.Local.Bin); bin != "" {
		return bin
	}

	return speech.BundleRoot(paths.StateDir())
}

// fprintQuietly writes best-effort progress prose.
func fprintQuietly(w io.Writer, message string) {
	if w == nil {
		return
	}
	if _, err := io.WriteString(w, message); err != nil {
		return
	}
}
