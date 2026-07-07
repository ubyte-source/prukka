package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"golang.org/x/sync/errgroup"

	"github.com/spf13/cobra"

	"github.com/ubyte-source/prukka/internal/control"
	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/core/meter"
	"github.com/ubyte-source/prukka/internal/core/session"
	"github.com/ubyte-source/prukka/internal/dispatch"
	"github.com/ubyte-source/prukka/internal/doctor"
	"github.com/ubyte-source/prukka/internal/media/egress/audio"
	"github.com/ubyte-source/prukka/internal/media/egress/hls"
	"github.com/ubyte-source/prukka/internal/media/egress/vtt"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
	"github.com/ubyte-source/prukka/internal/observability"
	"github.com/ubyte-source/prukka/internal/secret"
)

// daemonName is the daemon's command and doctor-check identity.
const daemonName = "daemon"

func newDaemonCmd(flags *rootFlags) *cobra.Command {
	var pprofAddr string

	cmd := &cobra.Command{
		Use:   daemonName,
		Short: "Run the Prukka daemon in the foreground",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			holder, log, err := flags.holder()
			if err != nil {
				return err
			}

			return runService(cmd.Context(), func(ctx context.Context) error {
				return runDaemon(ctx, holder, log, pprofAddr)
			})
		},
	}

	cmd.Flags().StringVar(&pprofAddr, "pprof", "",
		"serve pprof on this loopback address (e.g. 127.0.0.1:6060) to capture a PGO profile; off by default")

	return cmd
}

// runDaemon wires the daemon and serves until interrupted. Listeners read
// the startup snapshot; doctor and lane starts read the live one.
func runDaemon(ctx context.Context, holder *config.Holder, log *slog.Logger, pprofAddr string) error {
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if !isLoopback(pprofAddr) && pprofAddr != "" {
		return fmt.Errorf("--pprof %q must be a loopback address; profiling stays on this host", pprofAddr)
	}

	store := session.NewStore()
	book := meter.NewBook(nil)
	registry := vtt.NewRegistry()
	metrics := observability.NewMetrics()
	fallback := observability.NewFallbackState(metrics)
	hlsStore := hls.NewStore(filepath.Join(config.StateDir(), "media"), log)
	audioReg := audio.NewRegistry(sigCtx, muxSupervisor(log), hlsStore, log)

	// One dispatcher caps provider concurrency daemon-wide; sizing changes
	// need a restart.
	dcfg := holder.Current().Providers.Dispatch
	pool := dispatch.New(dcfg.Workers, dcfg.Queue)
	defer pool.Close()

	// Surface the pool's saturation so operators can size workers/queue.
	metrics.RegisterDispatchQueue(
		func() float64 { return float64(pool.Metrics().Size) },
		func() float64 { return float64(pool.Metrics().LogicalCapacity) },
	)

	settings := control.NewSettings(holder, daemonKeychain{})
	svc := control.NewService(store, version,
		func() []doctor.Check { return doctor.Run(holder.Current()) }, book.TotalRate,
		func() control.SessionDefaults { return sessionDefaults(holder.Current()) }, audioReg, settings)
	server := control.NewServer(holder.Current(), store, svc,
		control.DataPlane{Docs: registry, Streams: audioReg, Media: hlsStore}, metrics.Handler(), log)
	starter := newLaneStarter(holder, book, registry, audioReg, hlsStore, pool, metrics, fallback, log)
	lanes := session.NewRuntime(store, starter, log)

	// Daemon-lifetime goroutines: ledger cleanup, SIGHUP reload, gauge.
	go forgetDeleted(sigCtx, store, book)
	go watchReload(sigCtx, holder, log)
	go trackSessions(sigCtx, store, metrics)

	log.Info("prukka daemon starting", "version", version, "commit", commit)

	g, gctx := errgroup.WithContext(sigCtx)
	g.Go(func() error { return server.Run(gctx) })
	g.Go(func() error { return lanes.Run(gctx) })

	// Profiling is an operator opt-in and must never take the daemon down,
	// so its failures are logged, not fatal.
	go func() {
		if err := servePprof(gctx, pprofAddr, log); err != nil {
			log.Warn("pprof server stopped", "err", err)
		}
	}()

	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}

	log.Info("prukka daemon stopped")

	return nil
}

// watchReload applies SIGHUP hot-reloads until ctx ends. Windows
// never delivers SIGHUP; registering it there is harmless.
func watchReload(ctx context.Context, holder *config.Holder, log *slog.Logger) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)

	defer signal.Stop(hup)

	for {
		select {
		case <-ctx.Done():
			return
		case <-hup:
			notes, err := holder.Reload()
			if err != nil {
				log.Warn("config reload failed; keeping the previous configuration", "err", err)

				continue
			}

			log.Info("config reloaded", "restart_required", notes)
		}
	}
}

// trackSessions keeps the sessions-active gauge current from store events
// until ctx ends.
func trackSessions(ctx context.Context, store *session.Store, metrics *observability.Metrics) {
	events := store.Subscribe(ctx)

	metrics.SetSessionsActive(store.Count())

	for range events {
		metrics.SetSessionsActive(store.Count())
	}
}

// muxSupervisor builds the shared TS-mux supervisor when ffmpeg exists;
// without one, audio streaming reports unavailable and captions still work.
func muxSupervisor(log *slog.Logger) *ffmpeg.Supervisor {
	bin, err := ffmpeg.Resolve(config.StateDir())
	if err != nil {
		return nil
	}

	return ffmpeg.NewSupervisor(bin, log)
}

// sessionDefaults projects the config defaults onto the control plane's
// seeding contract.
func sessionDefaults(cfg *config.Config) control.SessionDefaults {
	return control.SessionDefaults{
		Subs:             cfg.Defaults.Subs,
		Bed:              cfg.Defaults.Bed,
		BudgetEURPerHour: cfg.Budgets.PerSessionEURPerHour,
		Delay:            cfg.Defaults.Delay.Std(),
	}
}

// daemonKeychain wires the settings surface to the same OS keychain
// `prukka key` writes.
type daemonKeychain struct{}

// Store implements control.Keychain.
func (daemonKeychain) Store(ref, value string) error { return secret.Store(ref, value) }

// Resolve implements control.Keychain.
func (daemonKeychain) Resolve(ref string) (string, error) { return secret.Resolve(ref) }

// forgetDeleted drops meter ledgers for deleted sessions.
func forgetDeleted(ctx context.Context, store *session.Store, book *meter.Book) {
	for e := range store.Subscribe(ctx) {
		if e.Type == session.EventDeleted {
			book.Forget(e.Session.Slug)
		}
	}
}
