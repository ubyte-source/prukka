package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ubyte-source/prukka/internal/besteffort"
	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
	"github.com/ubyte-source/prukka/internal/paths"
	"github.com/ubyte-source/prukka/internal/speech"
)

type setupInstallFunc func(context.Context, string, io.Writer) (string, error)

// setupEngineFunc installs the managed speech engine and the model packs the
// configuration needs.
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
			// A hung mirror must fail this step by deadline, not hang setup.
			installCtx, cancel := context.WithTimeout(cmd.Context(), speech.OperationTimeout)
			defer cancel()
			path, err := install(installCtx, paths.StateDir(), progress)
			if err != nil {
				return err
			}
			if printPath {
				// The engine phase stays out of this machine path: CI probes
				// ffmpeg without pulling models.
				cmd.Println(path)

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

// installEngine downloads the managed engine runtime and every model pack the
// configuration requires; an explicit providers.local.bin opts out entirely.
func installEngine(ctx context.Context, cfg *config.Config, progress io.Writer) error {
	return installEngineWithin(ctx, cfg, progress, speech.CatalogTimeout, speech.OperationTimeout)
}

// installEngineWithin is installEngine with injectable deadlines; the
// production bounds are minutes long.
func installEngineWithin(
	ctx context.Context, cfg *config.Config, progress io.Writer, catalogTimeout, opTimeout time.Duration,
) error {
	if strings.TrimSpace(cfg.Providers.Local.Bin) != "" {
		besteffort.Linef(progress, "speech engine: managed install skipped (providers.local.bin is set)")

		return nil
	}

	catalogURL, err := speech.CatalogURL(version)
	if err != nil {
		return err
	}
	client := speech.NewClient(catalogURL)
	installer := speech.NewInstaller(paths.StateDir(), client)
	reporter := speech.WriterReporter(progress)
	var catalog *speech.Catalog
	if err := within(ctx, catalogTimeout, func(fetchCtx context.Context) error {
		var fetchErr error
		catalog, fetchErr = client.Catalog(fetchCtx)

		return fetchErr
	}); err != nil {
		return fmt.Errorf("fetch engine catalog: %w", err)
	}
	if err := within(ctx, opTimeout, func(opCtx context.Context) error {
		_, runtimeErr := installer.EnsureRuntime(opCtx, catalog, reporter)

		return runtimeErr
	}); err != nil {
		return fmt.Errorf("install engine runtime: %w", err)
	}
	for _, id := range requiredPackIDs(cfg) {
		if err := within(ctx, opTimeout, func(opCtx context.Context) error {
			return installer.InstallPack(opCtx, catalog, id, reporter)
		}); err != nil {
			return fmt.Errorf("install %s: %w", id, err)
		}
	}

	return nil
}

// within runs one chain step under its own deadline.
func within(ctx context.Context, timeout time.Duration, step func(context.Context) error) error {
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return step(stepCtx)
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
			// The catalog names voice packs by BASE language only: a regional
			// tag ("de-CH") is valid config but names a pack that cannot exist.
			ids = append(ids, speech.VoicePackID(string(voice.Language.Base())))
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
