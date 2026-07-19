// The engine surface: the dashboard and CLI inspect the managed speech
// engine, start pack installs with streamed progress, and remove packs.
// Successful pack operations extend or retire the daemon's own capability
// configuration in the same validated transaction path settings use.

package control

import (
	"context"
	"errors"
	"fmt"
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

	// engineCatalogTTL bounds how stale the cached pack catalog may get.
	// One refresh is bounded by speech.CatalogTimeout — so GetEngine stays
	// snappy offline — and one asynchronous install by
	// speech.OperationTimeout: shared constants, so this surface and
	// `prukka setup` cannot drift apart on the same operations.
	engineCatalogTTL = 15 * time.Minute

	// engineEventBuffer absorbs progress bursts per SSE subscriber; a full
	// buffer drops intermediate progress, never blocking the installer.
	engineEventBuffer = 16
)

// EngineInstaller abstracts the speech installer for tests. The operations
// take their progress sink per call, so the installer needs no reference to
// the engine that drives it.
type EngineInstaller interface {
	State() (*speech.State, error)
	EnsureRuntime(ctx context.Context, catalog *speech.Catalog, progress speech.Reporter) (bool, error)
	InstallPack(ctx context.Context, catalog *speech.Catalog, id string, progress speech.Reporter) error
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
	change    func(config.Transition)

	// life is the engine's own lifetime, not a stored request context:
	// Shutdown ends it to cancel the in-flight operation's context.
	life context.Context
	halt context.CancelFunc

	// op keeps the last operation's record — terminal phase included, the
	// dashboard polls it after the fact — while opActive alone says whether
	// an operation still holds the single slot begin claims.
	op          *v1.EngineOperation
	catalog     *speech.Catalog
	subscribers map[chan wireEngineEvent]struct{}
	catalogAt   time.Time
	catalogErr  string
	ops         sync.WaitGroup
	mu          sync.Mutex
	opActive    bool
}

// NewEngine wires the engine surface fully constructed: change is the
// daemon's live-reconfiguration signal, handed the ordered config transition
// of a pack REMOVAL, which may strand a running lane. A pack install is
// suppressed by provenance — strictly additive, it must not restart live
// lanes, and classifying its own fingerprint pair would wrongly report one.
// change may be nil.
func NewEngine(
	installer EngineInstaller, source CatalogSource, holder *config.Holder,
	change func(config.Transition), log *slog.Logger,
) *Engine {
	life, halt := context.WithCancel(context.Background())

	return &Engine{
		installer:   installer,
		source:      source,
		holder:      holder,
		log:         log,
		change:      change,
		life:        life,
		halt:        halt,
		subscribers: map[chan wireEngineEvent]struct{}{},
	}
}

// subscribe registers one SSE connection for engine progress events until
// ctx ends, then closes the channel. Unregistering under the same lock
// broadcast sends under guarantees no send can race the close — the same
// discipline as session.Store.Subscribe.
func (e *Engine) subscribe(ctx context.Context) <-chan wireEngineEvent {
	ch := make(chan wireEngineEvent, engineEventBuffer)
	e.mu.Lock()
	e.subscribers[ch] = struct{}{}
	e.mu.Unlock()

	go func() {
		<-ctx.Done()

		e.mu.Lock()
		delete(e.subscribers, ch)
		e.mu.Unlock()

		close(ch)
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
		_, ensureErr := e.installer.EnsureRuntime(opCtx, catalog, e.Progress)

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
		if installErr := e.installer.InstallPack(opCtx, catalog, pack.ID, e.Progress); installErr != nil {
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

// Shutdown refuses new operations, cancels the in-flight one and joins it,
// bounded by ctx. The join lets the installer's deferred unlock release the
// on-disk operation lock before the process exits; a lock that survives the
// exit blocks every install until it goes stale.
func (e *Engine) Shutdown(ctx context.Context) error {
	// Ending the lifetime under the slot lock serializes it against begin:
	// no operation can register once the join below has started.
	e.mu.Lock()
	e.halt()
	e.mu.Unlock()

	drained := make(chan struct{})
	go func() {
		e.ops.Wait()
		close(drained)
	}()

	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		// The abandoned waiter ends with the operation's own timeout; the
		// process is exiting anyway.
		return fmt.Errorf("join the in-flight engine operation: %w", ctx.Err())
	}
}

// Progress adapts installer reporting onto the running operation and its
// subscribers; it is safe from any goroutine. The installer reports done
// once per artifact, before the operation's config step has run — the
// operation's terminal event is finish's alone, so those reports are
// dropped rather than replayed as a premature ending.
func (e *Engine) Progress(p speech.Progress) {
	if p.Phase == speech.PhaseDone {
		return
	}

	e.mu.Lock()
	if !e.opActive {
		e.mu.Unlock()

		return
	}
	e.op.Phase = p.Phase
	e.op.DoneBytes = p.DoneBytes
	e.op.TotalBytes = p.TotalBytes
	event := wireEngineEvent{
		Kind: e.op.GetKind(), PackID: e.op.GetPackId(), Phase: p.Phase,
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

// begin claims the single operation slot and registers the operation for
// Shutdown's join; only finish releases both.
func (e *Engine) begin(kind, packID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.life.Err() != nil {
		return status.Error(codes.Unavailable, "the engine is shutting down")
	}
	if e.opActive {
		return status.Error(codes.Aborted, speech.ErrBusy.Error())
	}
	e.opActive = true
	e.ops.Add(1)
	e.op = &v1.EngineOperation{Kind: kind, PackId: packID, Phase: speech.PhaseDownload}

	return nil
}

// runOperation drives one asynchronous operation to its terminal event. The
// operation inherits the request's values but not its cancellation — an
// install must outlive the HTTP call that accepted it — while Shutdown
// still reaches it through the lifetime hook.
func (e *Engine) runOperation(ctx context.Context, kind, packID string, run func(context.Context) error) {
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), speech.OperationTimeout)
	defer cancel()

	stop := context.AfterFunc(e.life, cancel)
	defer stop()

	e.finish(kind, packID, run(opCtx))
}

// finish releases the operation slot, records the terminal phase on the
// operation's record and notifies subscribers. It deliberately raises no
// configuration signal: retireConfig already emits e.changed for removals,
// while an install must stay silent because classifying its own fingerprint
// pair would report a restart of live lanes that never happened.
func (e *Engine) finish(kind, packID string, err error) {
	defer e.ops.Done()

	phase := speech.PhaseDone
	detail := ""
	if err != nil {
		phase = enginePhaseError
		detail = err.Error()
		e.log.Warn("engine operation failed", "kind", kind, "pack", packID, "err", err)
	}

	e.mu.Lock()
	e.opActive = false
	e.op.Phase = phase
	e.op.Error = detail
	e.mu.Unlock()

	e.broadcast(&wireEngineEvent{Kind: kind, PackID: packID, Phase: phase, Error: detail})
}

// extendConfig grows the daemon capability the pack just installed. Its
// transition is discarded by provenance: an install is strictly additive and
// must never restart live lanes.
func (e *Engine) extendConfig(pack *speech.Pack) error {
	_, err := e.holder.Update(func(c *config.Config) { extendConfigForPack(c, pack) })

	return err
}

// retireConfig removes the capability of the pack just removed. A revoked
// capability can strand a lane whose languages resolved to it, so the
// ordered transition goes straight to the reconfiguration signal.
func (e *Engine) retireConfig(pack *speech.InstalledPack) error {
	transition, err := e.holder.Update(func(c *config.Config) { retireConfigForPack(c, pack) })
	if err != nil {
		return err
	}
	e.changed(transition)

	return nil
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

	fetchCtx, cancel := context.WithTimeout(ctx, speech.CatalogTimeout)
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

func (e *Engine) changed(t config.Transition) {
	if e.change != nil {
		e.change(t)
	}
}
