// The engine surface: the dashboard and CLI inspect the managed speech
// engine, start pack installs with streamed progress, and remove packs.
// Successful pack operations extend or retire the daemon's own capability
// configuration in the same validated transaction path settings use.

package control

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/speech"

	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
)

// Engine operation kinds mirrored on the wire.
const (
	engineOpInstallRuntime = "install-runtime"
	engineOpInstallPack    = "install-pack"
	engineOpRemovePack     = "remove-pack"

	enginePhaseError = "error"

	// engineCatalogTTL bounds how stale the cached pack catalog may get;
	// engineCatalogTimeout bounds one refresh so GetEngine stays snappy
	// offline.
	engineCatalogTTL     = 15 * time.Minute
	engineCatalogTimeout = 8 * time.Second

	// engineOpTimeout bounds one asynchronous install: the largest artifact
	// on a slow line still finishes well inside it.
	engineOpTimeout = 45 * time.Minute

	// engineEventBuffer absorbs progress bursts per SSE subscriber; a full
	// buffer drops intermediate progress, never blocking the installer.
	engineEventBuffer = 16
)

// EngineInstaller abstracts the speech installer for tests.
type EngineInstaller interface {
	State() (*speech.State, error)
	EnsureRuntime(ctx context.Context, catalog *speech.Catalog) (bool, error)
	InstallPack(ctx context.Context, catalog *speech.Catalog, id string) error
	RemovePack(id string) error
}

// CatalogSource abstracts the catalog fetch for tests.
type CatalogSource interface {
	Catalog(ctx context.Context) (*speech.Catalog, error)
}

// wireEngineEvent is the SSE progress payload, camelCase like the gateway.
type wireEngineEvent struct {
	Kind       string `json:"kind"`
	PackID     string `json:"packId"`
	Phase      string `json:"phase"`
	Error      string `json:"error,omitempty"`
	DoneBytes  int64  `json:"doneBytes"`
	TotalBytes int64  `json:"totalBytes"`
}

// Engine implements the engine RPCs over the speech installer.
type Engine struct {
	installer EngineInstaller
	source    CatalogSource
	holder    *config.Holder
	log       *slog.Logger
	change    func()
	baseline  func()

	op          *v1.EngineOperation
	catalog     *speech.Catalog
	subscribers map[chan wireEngineEvent]struct{}
	catalogAt   time.Time
	catalogErr  string
	mu          sync.Mutex
}

// NewEngine wires the engine surface.
func NewEngine(installer EngineInstaller, source CatalogSource, holder *config.Holder, log *slog.Logger) *Engine {
	return &Engine{
		installer:   installer,
		source:      source,
		holder:      holder,
		log:         log,
		subscribers: map[chan wireEngineEvent]struct{}{},
	}
}

// SetChangeHook registers the daemon's live-reconfiguration signal, called
// after a pack removal that may strand a running lane.
func (e *Engine) SetChangeHook(change func()) {
	e.change = change
}

// SetBaselineHook registers the signal that absorbs a strictly-additive
// capability change (a pack install) into the reconfigure baseline without
// restarting live lanes.
func (e *Engine) SetBaselineHook(baseline func()) {
	e.baseline = baseline
}

// Subscribe registers one SSE connection for engine progress events until
// ctx ends. A nil engine returns a nil channel, which blocks forever in the
// stream's select: wiring without an engine simply emits no engine events.
func (e *Engine) Subscribe(ctx context.Context) <-chan wireEngineEvent {
	if e == nil {
		return nil
	}
	ch := make(chan wireEngineEvent, engineEventBuffer)
	e.mu.Lock()
	e.subscribers[ch] = struct{}{}
	e.mu.Unlock()

	go func() {
		<-ctx.Done()
		e.mu.Lock()
		delete(e.subscribers, ch)
		e.mu.Unlock()
	}()

	return ch
}

// GetEngine implements prukka.v1.Control.
func (e *Engine) GetEngine(ctx context.Context, _ *v1.GetEngineRequest) (*v1.GetEngineResponse, error) {
	engine, err := e.status(ctx)
	if err != nil {
		return nil, err
	}

	return &v1.GetEngineResponse{Engine: engine}, nil
}

// InstallEngineRuntime implements prukka.v1.Control: it accepts the install
// and streams progress over the events channel.
func (e *Engine) InstallEngineRuntime(
	ctx context.Context, _ *v1.InstallEngineRuntimeRequest,
) (*v1.InstallEngineRuntimeResponse, error) {
	catalog, err := e.requireCatalog(ctx)
	if err != nil {
		return nil, err
	}
	if beginErr := e.begin(engineOpInstallRuntime, ""); beginErr != nil {
		return nil, beginErr
	}

	go e.runOperation(ctx, engineOpInstallRuntime, "", func(opCtx context.Context) error {
		_, ensureErr := e.installer.EnsureRuntime(opCtx, catalog)

		return ensureErr
	})

	engine, err := e.status(ctx)
	if err != nil {
		return nil, err
	}

	return &v1.InstallEngineRuntimeResponse{Engine: engine}, nil
}

// InstallEnginePack implements prukka.v1.Control: it validates the pack,
// accepts the install and extends the configuration on success.
func (e *Engine) InstallEnginePack(
	ctx context.Context, req *v1.InstallEnginePackRequest,
) (*v1.InstallEnginePackResponse, error) {
	catalog, err := e.requireCatalog(ctx)
	if err != nil {
		return nil, err
	}
	pack, err := catalog.PackByID(req.GetId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if _, stateErr := e.installer.State(); stateErr != nil {
		return nil, status.Error(codes.FailedPrecondition, "install the engine runtime first")
	}
	if beginErr := e.begin(engineOpInstallPack, pack.ID); beginErr != nil {
		return nil, beginErr
	}

	go e.runOperation(ctx, engineOpInstallPack, pack.ID, func(opCtx context.Context) error {
		if installErr := e.installer.InstallPack(opCtx, catalog, pack.ID); installErr != nil {
			return installErr
		}

		return e.extendConfig(&pack)
	})

	engine, err := e.status(ctx)
	if err != nil {
		return nil, err
	}

	return &v1.InstallEnginePackResponse{Engine: engine}, nil
}

// RemoveEnginePack implements prukka.v1.Control: removal is fast and
// synchronous, and retires the capability from the configuration.
func (e *Engine) RemoveEnginePack(
	ctx context.Context, req *v1.RemoveEnginePackRequest,
) (*v1.RemoveEnginePackResponse, error) {
	id := req.GetId()
	if id == speech.PackIDSTTCore {
		return nil, status.Error(codes.InvalidArgument, "the stt-core pack is required and cannot be removed")
	}
	state, err := e.installer.State()
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	installed, ok := state.Pack(id)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "pack %q is not installed", id)
	}
	if beginErr := e.begin(engineOpRemovePack, id); beginErr != nil {
		return nil, beginErr
	}

	removeErr := e.installer.RemovePack(id)
	if removeErr == nil {
		removeErr = e.retireConfig(&installed)
	}
	e.finish(engineOpRemovePack, id, removeErr)
	if removeErr != nil {
		return nil, status.Error(codes.Internal, removeErr.Error())
	}

	engine, err := e.status(ctx)
	if err != nil {
		return nil, err
	}

	return &v1.RemoveEnginePackResponse{Engine: engine}, nil
}

// Progress adapts installer reporting onto the running operation and its
// subscribers; it is safe from any goroutine.
func (e *Engine) Progress(p speech.Progress) {
	e.mu.Lock()
	op := e.op
	if op == nil {
		e.mu.Unlock()

		return
	}
	op.Phase = p.Phase
	op.DoneBytes = p.DoneBytes
	op.TotalBytes = p.TotalBytes
	event := wireEngineEvent{
		Kind: op.GetKind(), PackID: op.GetPackId(), Phase: p.Phase,
		DoneBytes: p.DoneBytes, TotalBytes: p.TotalBytes,
	}
	e.mu.Unlock()

	e.broadcast(&event)
}

// status renders the merged engine snapshot.
func (e *Engine) status(ctx context.Context) (*v1.EngineStatus, error) {
	installed := true
	state, err := e.installer.State()
	if errors.Is(err, speech.ErrNotInstalled) {
		installed = false
		state = &speech.State{}
	} else if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	catalog, catalogErr := e.cachedCatalog(ctx)

	out := &v1.EngineStatus{
		Installed:    installed,
		Protocol:     speech.SupportedProtocol,
		CatalogError: catalogErr,
		Packs:        mergePacks(catalog, state),
	}
	e.mu.Lock()
	if e.op != nil {
		out.Operation = &v1.EngineOperation{
			Kind: e.op.GetKind(), PackId: e.op.GetPackId(), Phase: e.op.GetPhase(),
			DoneBytes: e.op.GetDoneBytes(), TotalBytes: e.op.GetTotalBytes(), Error: e.op.GetError(),
		}
	}
	e.mu.Unlock()

	return out, nil
}

// mergePacks joins the catalog offer with the installed inventory: catalog
// order first, then installed packs the catalog no longer lists.
func mergePacks(catalog *speech.Catalog, state *speech.State) []*v1.EnginePack {
	var out []*v1.EnginePack
	listed := map[string]bool{}
	if catalog != nil {
		for i := range catalog.Packs {
			p := &catalog.Packs[i]
			listed[p.ID] = true
			_, installed := state.Pack(p.ID)
			out = append(out, &v1.EnginePack{
				Id: p.ID, Kind: p.Kind, From: p.From, To: p.To, Lang: p.Lang,
				Installed: installed, SizeBytes: p.Size, License: p.License,
			})
		}
	}
	for i := range state.Packs {
		p := &state.Packs[i]
		if listed[p.ID] {
			continue
		}
		out = append(out, &v1.EnginePack{
			Id: p.ID, Kind: p.Kind, From: p.From, To: p.To, Lang: p.Lang, Installed: true,
		})
	}

	return out
}

// begin claims the single operation slot.
func (e *Engine) begin(kind, packID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.op != nil && !operationTerminal(e.op) {
		return status.Error(codes.Aborted, speech.ErrBusy.Error())
	}
	e.op = &v1.EngineOperation{Kind: kind, PackId: packID, Phase: speech.PhaseDownload}

	return nil
}

// runOperation drives one asynchronous operation to its terminal event. The
// operation inherits the request's values but not its cancellation: an
// install must outlive the HTTP call that accepted it.
func (e *Engine) runOperation(ctx context.Context, kind, packID string, run func(context.Context) error) {
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), engineOpTimeout)
	defer cancel()

	e.finish(kind, packID, run(opCtx))
}

// finish records the terminal phase, notifies subscribers and signals the
// daemon when the configuration changed.
func (e *Engine) finish(kind, packID string, err error) {
	phase := speech.PhaseDone
	detail := ""
	if err != nil {
		phase = enginePhaseError
		detail = err.Error()
		e.log.Warn("engine operation failed", "kind", kind, "pack", packID, "err", err)
	}

	e.mu.Lock()
	if e.op != nil {
		e.op.Phase = phase
		e.op.Error = detail
	}
	e.mu.Unlock()

	e.broadcast(&wireEngineEvent{Kind: kind, PackID: packID, Phase: phase, Error: detail})
	if err != nil {
		return
	}

	switch kind {
	case engineOpRemovePack:
		// A revoked capability can strand a lane whose languages resolved to
		// the removed pair or voice, so live lanes must reconfigure.
		e.changed()
	case engineOpInstallPack:
		// Additive: record the delta but keep in-progress calls running.
		e.baselineSynced()
	}
	// engineOpInstallRuntime touches no lane-relevant config, so it signals
	// neither path.
}

// extendConfig grows the daemon capability the pack just installed.
func (e *Engine) extendConfig(pack *speech.Pack) error {
	_, err := e.holder.Update(func(c *config.Config) { extendConfigForPack(c, pack) })

	return err
}

// retireConfig removes the capability of the pack just removed.
func (e *Engine) retireConfig(pack *speech.InstalledPack) error {
	_, err := e.holder.Update(func(c *config.Config) { retireConfigForPack(c, pack) })

	return err
}

// extendConfigForPack routes one installed pack to the config capability it
// grants; config owns the mutation invariants (dedup, one voice per language).
func extendConfigForPack(c *config.Config, pack *speech.Pack) {
	local := &c.Providers.Local
	switch pack.Kind {
	case speech.PackMT:
		local.MT.AddPair(core.Lang(pack.From), core.Lang(pack.To))
	case speech.PackVoice:
		local.TTS.SetVoice(core.Lang(pack.Lang), pack.Voice)
	}
}

// retireConfigForPack routes one removed pack to the config capability it
// revokes.
func retireConfigForPack(c *config.Config, pack *speech.InstalledPack) {
	local := &c.Providers.Local
	switch pack.Kind {
	case speech.PackMT:
		local.MT.RemovePair(core.Lang(pack.From), core.Lang(pack.To))
	case speech.PackVoice:
		local.TTS.RemoveVoice(core.Lang(pack.Lang))
	}
}

// requireCatalog fetches a fresh-enough catalog or fails the mutation: an
// install without a catalog cannot verify anything.
func (e *Engine) requireCatalog(ctx context.Context) (*speech.Catalog, error) {
	catalog, catalogErr := e.cachedCatalog(ctx)
	if catalog == nil {
		return nil, status.Errorf(codes.Unavailable, "engine catalog unavailable: %s", catalogErr)
	}

	return catalog, nil
}

// cachedCatalog serves the recent catalog or refreshes it, reporting the
// fetch error without failing the caller.
func (e *Engine) cachedCatalog(ctx context.Context) (catalog *speech.Catalog, fetchError string) {
	e.mu.Lock()
	if e.catalog != nil && time.Since(e.catalogAt) < engineCatalogTTL {
		cached := e.catalog
		e.mu.Unlock()

		return cached, ""
	}
	e.mu.Unlock()

	fetchCtx, cancel := context.WithTimeout(ctx, engineCatalogTimeout)
	defer cancel()

	catalog, err := e.source.Catalog(fetchCtx)

	e.mu.Lock()
	defer e.mu.Unlock()
	if err != nil {
		e.catalogErr = err.Error()

		// A stale catalog still names verifiable artifacts; keep serving it.
		if e.catalog != nil {
			return e.catalog, e.catalogErr
		}

		return nil, e.catalogErr
	}
	e.catalog = catalog
	e.catalogErr = ""
	e.catalogAt = time.Now()

	return catalog, ""
}

// broadcast fans one event out without ever blocking the installer.
func (e *Engine) broadcast(event *wireEngineEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()

	for ch := range e.subscribers {
		select {
		case ch <- *event:
		default:
		}
	}
}

func (e *Engine) changed() {
	if e.change != nil {
		e.change()
	}
}

// baselineSynced absorbs an additive capability change into the reconfigure
// baseline without restarting live lanes.
func (e *Engine) baselineSynced() {
	if e.baseline != nil {
		e.baseline()
	}
}

// operationTerminal reports whether the recorded operation has ended.
func operationTerminal(op *v1.EngineOperation) bool {
	return op.GetPhase() == speech.PhaseDone || op.GetPhase() == enginePhaseError
}
