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

const (
	engineOpInstallRuntime = "install-runtime"
	engineOpInstallPack    = "install-pack"
	engineOpRemovePack     = "remove-pack"

	enginePhaseError = "error"

	// engineCatalogTTL bounds how stale the cached pack catalog may get.
	engineCatalogTTL = 15 * time.Minute

	// engineEventBuffer is per SSE subscriber; a full buffer drops
	// intermediate progress rather than blocking the installer.
	engineEventBuffer = 16
)

// EngineInstaller abstracts the speech installer for tests.
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

// wireEngineEvent is the SSE progress payload.
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

	// life is the engine's own lifetime, not a stored request context.
	life context.Context
	halt context.CancelFunc

	// op outlives its operation so the dashboard can poll the terminal
	// phase; opActive alone says whether the single slot is still held.
	op          *v1.EngineOperation
	catalog     *speech.Catalog
	subscribers map[chan wireEngineEvent]struct{}
	catalogAt   time.Time
	catalogErr  string
	ops         sync.WaitGroup
	mu          sync.Mutex
	opActive    bool
}

// EngineDeps carries the engine surface's collaborators; Change may be nil.
type EngineDeps struct {
	Installer EngineInstaller
	Source    CatalogSource
	Holder    *config.Holder
	Change    func(config.Transition)
	Log       *slog.Logger
}

// NewEngine wires the engine surface.
func NewEngine(deps *EngineDeps) *Engine {
	life, halt := context.WithCancel(context.Background())

	return &Engine{
		installer:   deps.Installer,
		source:      deps.Source,
		holder:      deps.Holder,
		log:         deps.Log,
		change:      deps.Change,
		life:        life,
		halt:        halt,
		subscribers: map[chan wireEngineEvent]struct{}{},
	}
}

// subscribe registers one SSE connection until ctx ends, unregistering it
// under the lock broadcast sends under so no send can race the close.
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
// and streams progress.
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

// InstallEnginePack implements prukka.v1.Control: it accepts the install and
// extends the configuration on success.
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

// RemoveEnginePack implements prukka.v1.Control, synchronously.
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

// Shutdown cancels the in-flight operation and joins it within ctx, so the
// installer releases its on-disk lock before the process exits.
func (e *Engine) Shutdown(ctx context.Context) error {
	// Under the slot lock: no operation can register once the join starts.
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
		return fmt.Errorf("join the in-flight engine operation: %w", ctx.Err())
	}
}

// Progress adapts installer reporting onto the running operation and its
// subscribers; it is safe from any goroutine.
func (e *Engine) Progress(p speech.Progress) {
	// The installer reports done once per artifact; the operation's terminal
	// event is finish's alone.
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
		Kind:       e.op.GetKind(),
		PackID:     e.op.GetPackId(),
		Phase:      p.Phase,
		DoneBytes:  p.DoneBytes,
		TotalBytes: p.TotalBytes,
	}
	e.mu.Unlock()

	e.broadcast(&event)
}

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
			Kind:       e.op.GetKind(),
			PackId:     e.op.GetPackId(),
			Phase:      e.op.GetPhase(),
			DoneBytes:  e.op.GetDoneBytes(),
			TotalBytes: e.op.GetTotalBytes(),
			Error:      e.op.GetError(),
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
				Id:        p.ID,
				Kind:      p.Kind,
				From:      p.From,
				To:        p.To,
				Lang:      p.Lang,
				Installed: installed,
				SizeBytes: p.Size,
				License:   p.License,
			})
		}
	}
	for i := range state.Packs {
		p := &state.Packs[i]
		if listed[p.ID] {
			continue
		}
		out = append(out, &v1.EnginePack{
			Id:        p.ID,
			Kind:      p.Kind,
			From:      p.From,
			To:        p.To,
			Lang:      p.Lang,
			Installed: true,
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

// runOperation drives one asynchronous operation to its terminal event; it
// inherits the request's values but not its cancellation, because an install
// must outlive the HTTP call that accepted it.
func (e *Engine) runOperation(ctx context.Context, kind, packID string, run func(context.Context) error) {
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), speech.OperationTimeout)
	defer cancel()

	stop := context.AfterFunc(e.life, cancel)
	defer stop()

	e.finish(kind, packID, run(opCtx))
}

// finish releases the operation slot, records the terminal phase and
// notifies subscribers.
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

// extendConfig grows the daemon capability the pack just installed; its
// transition is discarded because an install is strictly additive and must
// never restart live lanes.
func (e *Engine) extendConfig(pack *speech.Pack) error {
	_, err := e.holder.Update(func(c *config.Config) { extendConfigForPack(c, pack) })

	return err
}

// retireConfig revokes the removed pack's capability, which can strand a lane
// whose languages resolved to it, so its transition is signaled.
func (e *Engine) retireConfig(pack *speech.InstalledPack) error {
	transition, err := e.holder.Update(func(c *config.Config) { retireConfigForPack(c, pack) })
	if err != nil {
		return err
	}
	e.changed(transition)

	return nil
}

func extendConfigForPack(c *config.Config, pack *speech.Pack) {
	local := &c.Providers.Local
	switch pack.Kind {
	case speech.PackMT:
		local.MT.AddPair(core.Lang(pack.From), core.Lang(pack.To))
	case speech.PackVoice:
		local.TTS.SetVoice(core.Lang(pack.Lang), pack.Voice)
	}
}

func retireConfigForPack(c *config.Config, pack *speech.InstalledPack) {
	local := &c.Providers.Local
	switch pack.Kind {
	case speech.PackMT:
		local.MT.RemovePair(core.Lang(pack.From), core.Lang(pack.To))
	case speech.PackVoice:
		local.TTS.RemoveVoice(core.Lang(pack.Lang))
	}
}

// requireCatalog fetches a fresh-enough catalog or fails the mutation.
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
