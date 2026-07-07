package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

	"github.com/spf13/cobra"

	"github.com/ubyte-source/prukka/internal/control"
	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/core/session"
	"github.com/ubyte-source/prukka/internal/dispatch"
	"github.com/ubyte-source/prukka/internal/doctor"
	"github.com/ubyte-source/prukka/internal/media/discover"
	"github.com/ubyte-source/prukka/internal/media/egress/audio"
	"github.com/ubyte-source/prukka/internal/media/egress/hls"
	"github.com/ubyte-source/prukka/internal/media/egress/vtt"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
	"github.com/ubyte-source/prukka/internal/observability"
)

// daemonName is the daemon's command and doctor-check identity.
const daemonName = "daemon"

func newDaemonCmd(flags *rootFlags) *cobra.Command {
	var pprofAddr, logFile string

	cmd := &cobra.Command{
		Use:   daemonName,
		Short: "Run the Prukka daemon in the foreground",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			holder, log, err := flags.holder()
			if err != nil {
				return err
			}

			if logFile != "" {
				teed, closeLog, teeErr := teeLogger(flags, logFile)
				if teeErr != nil {
					return teeErr
				}

				log = teed

				defer func() {
					if closeErr := closeLog(); closeErr != nil {
						log.Warn("log file close", "err", closeErr)
					}
				}()
			}

			return runDaemon(cmd.Context(), holder, log, pprofAddr)
		},
	}

	cmd.Flags().StringVar(&pprofAddr, "pprof", "",
		"serve pprof on this loopback address (e.g. 127.0.0.1:6060) to capture a PGO profile; off by default")
	cmd.Flags().StringVar(&logFile, "log-file", "",
		"also append the daemon log to this file; the Windows service uses it because scheduled tasks capture no output")

	return cmd
}

// teeLogger duplicates the daemon log into path, creating its directory.
// launchd and journald capture the daemon's stderr on the other platforms;
// a Windows scheduled task discards it, so the service passes --log-file.
func teeLogger(flags *rootFlags, path string) (*slog.Logger, func() error, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, nil, fmt.Errorf("create log directory: %w", err)
	}

	f, err := os.OpenFile(filepath.Clean(path), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open log file: %w", err)
	}

	log, logErr := flags.loggerTo(io.MultiWriter(os.Stderr, f))
	if logErr != nil {
		return nil, nil, errors.Join(logErr, f.Close())
	}

	return log, f.Close, nil
}

// runDaemon wires the daemon and serves until interrupted. Listeners read
// the startup snapshot; doctor and lane starts read the live one.
func runDaemon(ctx context.Context, holder *config.Holder, log *slog.Logger, pprofAddr string) error {
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if !isLoopback(pprofAddr) && pprofAddr != "" {
		return fmt.Errorf("--pprof %q must be a loopback address; profiling stays on this host", pprofAddr)
	}

	daemon := newDaemonRuntime(sigCtx, holder, log)
	defer daemon.pool.Close()

	log.Info("prukka daemon starting", "version", version, "commit", commit)

	g, gctx := errgroup.WithContext(sigCtx)
	mediaReady := make(chan struct{})
	g.Go(func() error {
		return daemon.server.RunAfterBind(gctx, func() error {
			if err := resetMediaRoot(daemon.mediaRoot); err != nil {
				return err
			}
			close(mediaReady)

			return nil
		})
	})
	g.Go(func() error {
		select {
		case <-mediaReady:
			return daemon.lanes.Run(gctx)
		case <-gctx.Done():
			return nil
		}
	})

	g.Go(func() error {
		return servePprof(gctx, pprofAddr, log)
	})

	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}

	log.Info("prukka daemon stopped")

	return nil
}

type daemonRuntime struct {
	server    *control.Server
	lanes     *session.Runtime
	pool      *dispatch.Pool
	mediaRoot string
}

func newDaemonRuntime(ctx context.Context, holder *config.Holder, log *slog.Logger) *daemonRuntime {
	// Output targets rebind by label at start: positional device indexes
	// reshuffle whenever any audio device appears or vanishes.
	ffmpeg.SetOutputIndexResolver(discover.OutputIndex)

	dcfg := holder.Current().Providers.Dispatch
	store := session.NewStore(session.WithMaxSessions(dcfg.MaxSessions))
	registry := vtt.NewRegistry()
	metrics := observability.NewMetrics()
	mediaRoot := filepath.Join(config.StateDir(), "media")
	hlsStore := hls.NewStore(mediaRoot, log)
	audioReg := audio.NewRegistry(ctx, muxSupervisor(log), hlsStore, log)

	// One dispatcher caps provider concurrency daemon-wide; sizing changes
	// need a restart.
	pool := dispatch.New(dcfg.Workers, dcfg.Queue)
	laneSlots := semaphore.NewWeighted(int64(dcfg.MaxLanes))

	// Surface the pool's saturation so operators can size workers/queue.
	metrics.RegisterDispatchQueue(
		func() float64 { return float64(pool.Metrics().Size) },
		func() float64 { return float64(pool.Metrics().LogicalCapacity) },
	)

	settings := control.NewSettings(holder)
	svc := control.NewService(store, version,
		func() []doctor.Check { return doctor.Run(holder.Current()) },
		func() control.SessionDefaults { return sessionDefaults(holder.Current()) },
		deviceList(log), configuredDubbedLanguages(holder), configuredSessionCapability(holder), audioReg, settings)
	server := control.NewServer(holder.Current(), store, svc,
		control.DataPlane{Docs: registry, Streams: audioReg, Media: hlsStore, Log: log}, metrics.Handler(), log)
	starter := newLaneStarter(holder, registry, audioReg, hlsStore, pool, laneSlots, metrics, log)

	// A lane that ended on its own keeps its outputs downloadable until the
	// session is deleted or this daemon stops.
	cleanup := func(slug string) {
		registry.Drop(slug)
		audioReg.Drop(slug)
		hlsStore.Drop(slug)
	}
	lanes := session.NewRuntime(store, starter, cleanup, log)
	// A settings save only restarts live media when the lane-relevant
	// config actually changed; otherwise a call keeps running untouched
	// instead of dropping its clone cache and voice references.
	laneChange := reconfigureOnLaneChange(holder, lanes.Reconfigure)
	settings.SetChangeHook(laneChange)

	// Daemon-lifetime goroutines: SIGHUP reload, session gauge.
	go watchReload(ctx, holder, laneChange, log)
	go trackSessions(ctx, store, metrics)

	return &daemonRuntime{server: server, lanes: lanes, pool: pool, mediaRoot: mediaRoot}
}

func resetMediaRoot(path string) error {
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove stale media outputs %s: %w", path, err)
	}

	return nil
}

// reconfigureOnLaneChange wraps the reconfigure signal so a settings save
// restarts live media only when the lane-relevant config actually changed.
func reconfigureOnLaneChange(holder *config.Holder, reconfigure func()) func() {
	var (
		mu   sync.Mutex
		last = holder.Current().LaneFingerprint()
	)

	return func() {
		mu.Lock()
		current := holder.Current().LaneFingerprint()
		changed := current != last
		last = current
		mu.Unlock()

		if changed {
			reconfigure()
		}
	}
}

// watchReload applies SIGHUP hot-reloads until ctx ends. Windows
// never delivers SIGHUP; registering it there is harmless.
func watchReload(ctx context.Context, holder *config.Holder, reconfigure func(), log *slog.Logger) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)

	defer signal.Stop(hup)

	for {
		select {
		case <-ctx.Done():
			return
		case <-hup:
			reloadConfig(holder, reconfigure, log)
		}
	}
}

func reloadConfig(holder *config.Holder, reconfigure func(), log *slog.Logger) {
	notes, err := holder.Reload()
	if err != nil {
		log.Warn("config reload failed; keeping the previous configuration", "err", err)

		return
	}

	reconfigure()
	log.Info("config reloaded", "restart_required", notes)
}

// trackSessions keeps bounded registry/state gauges current from store events.
func trackSessions(ctx context.Context, store *session.Store, metrics *observability.Metrics) {
	events := store.Subscribe(ctx)

	publishSessionCounts(store, metrics)

	for range events {
		// Subscribers may drop bursts by contract, so every notification
		// triggers a full bounded-registry resync instead of applying a delta.
		publishSessionCounts(store, metrics)
	}
}

func publishSessionCounts(store *session.Store, metrics *observability.Metrics) {
	sessions := store.List()
	counts := observability.SessionCounts{Registered: len(sessions)}

	for i := range sessions {
		switch sessions[i].Runtime().State {
		case session.StateStarting:
			counts.Starting++
		case session.StateRunning:
			counts.Running++
		case session.StateFinished:
			counts.Finished++
		case session.StateFailed:
			counts.Failed++
		}
	}

	metrics.SetSessionCounts(counts)
}

// deviceListTimeout bounds one enumeration pass; listings spawn ffmpeg.
const deviceListTimeout = 5 * time.Second

// deviceList wires the picker enumeration: best-effort, bounded, and
// degrading to an empty list (manual entry) rather than failing.
func deviceList(log *slog.Logger) control.DevicesFunc {
	return func(ctx context.Context) []discover.Device {
		ctx, cancel := context.WithTimeout(ctx, deviceListTimeout)
		defer cancel()

		bin, binErr := ffmpeg.Resolve(config.StateDir())
		if binErr != nil {
			bin = "" // capture listing needs ffmpeg; native playback enumeration still works
		}

		devices, err := discover.Devices(ctx, bin)
		if err != nil {
			log.Warn("device enumeration", "err", err)
		}

		return devices
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
		Langs: slices.Clone(cfg.Defaults.Langs),
		Subs:  cfg.Defaults.Subs,
		Bed:   cfg.Defaults.Bed,
		Delay: cfg.Defaults.Delay.Std(),
	}
}
