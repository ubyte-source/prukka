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
	"github.com/ubyte-source/prukka/internal/lane"
	"github.com/ubyte-source/prukka/internal/media/deviceurl"
	"github.com/ubyte-source/prukka/internal/media/discover"
	"github.com/ubyte-source/prukka/internal/media/egress/audio"
	"github.com/ubyte-source/prukka/internal/media/egress/hls"
	"github.com/ubyte-source/prukka/internal/media/egress/vtt"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
	"github.com/ubyte-source/prukka/internal/observability"
	"github.com/ubyte-source/prukka/internal/paths"
	"github.com/ubyte-source/prukka/internal/speech"
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

// runDaemon wires the daemon and serves until interrupted.
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

	g.Go(func() error {
		daemon.watchers(gctx)

		return nil
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
	store     *session.Store
	holder    *config.Holder
	metrics   *observability.Metrics
	log       *slog.Logger
	reload    func(config.Transition)
	mediaRoot string
}

func newDaemonRuntime(ctx context.Context, holder *config.Holder, log *slog.Logger) *daemonRuntime {
	dcfg := holder.Current().Providers.Dispatch
	store := session.NewStore(session.WithMaxSessions(dcfg.MaxSessions))
	registry := vtt.NewRegistry()
	metrics := observability.NewMetrics()
	mediaRoot := filepath.Join(paths.StateDir(), "media")
	hlsStore := hls.NewStore(mediaRoot, log)
	// Device sinks reopen when another application rewrites the device's nominal
	// configuration (OBS switches the rate on attach). Both playback paths bind
	// the device by name, not by index, which Continuity devices reshuffle.
	audioReg := audio.NewRegistry(ctx, muxSupervisor(log), hlsStore, log,
		audio.WithConfigStamp(deviceOutputStamp),
		audio.WithPlaybackHelper(lane.MicCaptureHelper),
		audio.WithOutputIndexResolver(discover.OutputIndex))

	// One dispatcher caps provider concurrency daemon-wide; sizing changes
	// need a restart.
	pool := dispatch.New(dcfg.Workers, dcfg.Queue)
	laneSlots := semaphore.NewWeighted(int64(dcfg.MaxLanes))

	metrics.RegisterDispatchQueue(
		func() float64 { return float64(pool.Metrics().Size) },
		func() float64 { return float64(pool.Metrics().Capacity) },
	)

	// A lane that ended on its own keeps its outputs downloadable until the
	// session is deleted or this daemon stops.
	outs := lane.NewOutputs(registry, audioReg, hlsStore)
	starter := lane.NewStarter(&lane.StarterDeps{
		Holder:  holder,
		Outputs: outs,
		Pool:    pool,
		Slots:   laneSlots,
		Metrics: metrics,
		Log:     log,
	})
	lanes := session.NewRuntime(&session.RuntimeDeps{
		Store:   store,
		Start:   starter,
		Outputs: outs,
		Log:     log,
	})
	laneCfg := &laneReconfigurer{reconfigure: lanes.Reconfigure}

	server := newControlServer(ctx, &controlDeps{
		Holder:  holder,
		Store:   store,
		Docs:    registry,
		Streams: audioReg,
		Media:   hlsStore,
		Metrics: metrics,
		Reload:  laneCfg.onChange,
		Log:     log,
	})

	return &daemonRuntime{
		server:    server,
		lanes:     lanes,
		pool:      pool,
		store:     store,
		holder:    holder,
		metrics:   metrics,
		log:       log,
		reload:    laneCfg.onChange,
		mediaRoot: mediaRoot,
	}
}

// controlDeps carries the daemon state the control plane serves.
type controlDeps struct {
	Holder  *config.Holder
	Store   *session.Store
	Docs    *vtt.Registry
	Streams *audio.Registry
	Media   *hls.Store
	Metrics *observability.Metrics
	Reload  func(config.Transition)
	Log     *slog.Logger
}

// newControlServer assembles the control plane over already-wired daemon
// state.
func newControlServer(ctx context.Context, deps *controlDeps) *control.Server {
	settings := control.NewSettings(deps.Holder)
	settings.SetChangeHook(deps.Reload)
	engine := managedEngine(deps.Holder, deps.Reload, deps.Log)
	svc := control.NewService(&control.ServiceDeps{
		Store:    deps.Store,
		Version:  version,
		Doctor:   func() []doctor.Check { return doctor.Run(deps.Holder.Current()) },
		Defaults: func() control.SessionDefaults { return sessionDefaults(deps.Holder.Current()) },
		Devices:  deviceList(ctx),
		Dubbed:   lane.DubbedLanguages(deps.Holder),
		Capable:  lane.SessionCapability(deps.Holder),
		Pusher:   deps.Streams,
		Settings: settings,
		Engine:   engine,
	})

	return control.NewServer(&control.ServerDeps{
		Config:  deps.Holder.Current(),
		Store:   deps.Store,
		Service: svc,
		Data:    control.DataPlane{Docs: deps.Docs, Streams: deps.Streams, Media: deps.Media, Log: deps.Log},
		Metrics: deps.Metrics.Handler(),
		Log:     deps.Log,
	})
}

// watchers runs the daemon-lifetime background loops and returns only once
// both have observed ctx ending.
func (d *daemonRuntime) watchers(ctx context.Context) {
	var wg sync.WaitGroup

	wg.Add(2)

	go func() {
		defer wg.Done()
		watchReload(ctx, d.holder, d.reload, d.log)
	}()
	go func() {
		defer wg.Done()
		trackSessions(ctx, d.store, d.metrics)
	}()

	wg.Wait()
}

// deviceOutputStamp fingerprints a device output target for the encoder
// watchdog; only labeled device targets are watchable.
func deviceOutputStamp(target string) (string, bool) {
	label := deviceurl.AudioLabel(target)
	if label == "" {
		return "", false
	}

	return discover.OutputStamp(label)
}

// managedEngine wires the speech-engine surface over the managed installer.
func managedEngine(
	holder *config.Holder, change func(config.Transition), log *slog.Logger,
) *control.Engine {
	catalogURL, err := speech.CatalogURL(version)
	if err != nil {
		log.Warn("managed speech-engine install unavailable", "reason", err)
	}
	client := speech.NewClient(catalogURL)
	installer := speech.NewInstaller(paths.StateDir(), client)

	return control.NewEngine(&control.EngineDeps{
		Installer: installer,
		Source:    client,
		Holder:    holder,
		Change:    change,
		Log:       log,
	})
}

func resetMediaRoot(path string) error {
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove stale media outputs %s: %w", path, err)
	}

	return nil
}

// laneReconfigurer restarts live media only when a config transition touches
// what a running lane depends on. Every writer classifies its OWN ordered
// transition, so there is no shared baseline a concurrent writer could
// clobber.
type laneReconfigurer struct {
	reconfigure func()
}

// onChange reconfigures live lanes when the transition changed the
// lane-relevant config.
func (l *laneReconfigurer) onChange(t config.Transition) {
	if t.Previous.LaneFingerprint() != t.Current.LaneFingerprint() {
		l.reconfigure()
	}
}

// watchReload applies SIGHUP hot-reloads until ctx ends; Windows never
// delivers SIGHUP.
func watchReload(
	ctx context.Context, holder *config.Holder, reconfigure func(config.Transition), log *slog.Logger,
) {
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

func reloadConfig(holder *config.Holder, reconfigure func(config.Transition), log *slog.Logger) {
	transition, err := holder.Reload()
	if err != nil {
		log.Warn("config reload failed; keeping the previous configuration", "err", err)

		return
	}

	reconfigure(transition)
	log.Info("config reloaded", "restart_required", transition.Notes)
}

// trackSessions keeps bounded registry/state gauges current from store events.
func trackSessions(ctx context.Context, store *session.Store, metrics *observability.Metrics) {
	events := store.Subscribe(ctx)

	publishSessionCounts(store, metrics)

	for range events {
		// Subscribers may drop bursts by contract, so every notification
		// triggers a full resync rather than a delta.
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

// deviceListTimeout bounds the enumeration itself; listings spawn ffmpeg.
const deviceListTimeout = 5 * time.Second

// deviceList wires the picker enumeration: best-effort, bounded, and degrading
// to an empty list rather than failing. Resolving ffmpeg re-verifies the
// managed install and costs seconds, so it stays outside the enumeration
// budget.
func deviceList(ctx context.Context) control.DevicesFunc {
	return discover.NewInventory(ctx, func() []discover.Device {
		bin, binErr := ffmpeg.Resolve(paths.StateDir())
		if binErr != nil {
			bin = "" // capture listing needs ffmpeg; native playback enumeration still works
		}

		passCtx, cancel := context.WithTimeout(ctx, deviceListTimeout)
		defer cancel()

		return discover.Devices(passCtx, bin)
	}).List
}

// muxSupervisor builds the shared TS-mux supervisor, or nil when ffmpeg is
// unresolvable.
func muxSupervisor(log *slog.Logger) *ffmpeg.Supervisor {
	bin, err := ffmpeg.Resolve(paths.StateDir())
	if err != nil {
		return nil
	}

	return ffmpeg.NewSupervisor(bin, log)
}

// sessionDefaults projects the config defaults onto the control plane.
func sessionDefaults(cfg *config.Config) control.SessionDefaults {
	return control.SessionDefaults{
		Langs: slices.Clone(cfg.Defaults.Langs),
		Subs:  cfg.Defaults.Subs,
		Bed:   cfg.Defaults.Bed,
		Delay: cfg.Defaults.Delay.Std(),
	}
}
