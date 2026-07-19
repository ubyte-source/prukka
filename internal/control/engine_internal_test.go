package control

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/speech"

	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
)

// fakeEngineInstaller scripts installer behavior for the engine surface.
type fakeEngineInstaller struct {
	state      *speech.State
	gate       chan struct{}
	installErr error
	installed  []string
	removed    []string
	mu         sync.Mutex
}

func (f *fakeEngineInstaller) State() (*speech.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state == nil {
		return nil, speech.ErrNotInstalled
	}

	return f.state, nil
}

func (f *fakeEngineInstaller) EnsureRuntime(context.Context, *speech.Catalog) (bool, error) {
	return true, nil
}

func (f *fakeEngineInstaller) InstallPack(_ context.Context, _ *speech.Catalog, id string) error {
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installed = append(f.installed, id)

	return f.installErr
}

func (f *fakeEngineInstaller) RemovePack(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, id)

	return nil
}

// fakeCatalogSource serves one fixed catalog or one fixed error.
type fakeCatalogSource struct {
	catalog *speech.Catalog
	err     error
}

func (f *fakeCatalogSource) Catalog(context.Context) (*speech.Catalog, error) {
	return f.catalog, f.err
}

func engineTestCatalog() *speech.Catalog {
	return &speech.Catalog{
		Schema: "prukka.engine.catalog", Version: 1, Protocol: speech.SupportedProtocol,
		Runtimes: []speech.Runtime{{OS: "any", Arch: "any", URL: "https://x/r", SHA256: "0", Size: 1}},
		Packs: []speech.Pack{
			{ID: speech.PackIDSTTCore, Kind: speech.PackSTT, URL: "https://x/s", SHA256: "0", Size: 1},
			{ID: "mt-it-en", Kind: speech.PackMT, From: "it", To: "en", URL: "https://x/m", SHA256: "0", Size: 1},
			{
				ID: "voice-it", Kind: speech.PackVoice, Lang: "it", Voice: "models/tts/custom-it.onnx",
				URL: "https://x/v", SHA256: "0", Size: 1,
			},
		},
	}
}

func engineTestHolder(t *testing.T) *config.Holder {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	holder, err := config.NewHolder(path)
	if err != nil {
		t.Fatalf("holder: %v", err)
	}

	return holder
}

func newTestEngine(t *testing.T, installer EngineInstaller, source CatalogSource) *Engine {
	t.Helper()

	return NewEngine(installer, source, engineTestHolder(t), slog.New(slog.DiscardHandler))
}

// waitEngineEvent reads events until the wanted phase or a timeout.
func waitEngineEvent(t *testing.T, events <-chan wireEngineEvent, phase string) wireEngineEvent {
	t.Helper()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-events:
			if e.Phase == phase {
				return e
			}
		case <-deadline:
			t.Fatalf("no %s engine event", phase)
		}
	}
}

func TestGetEngineMergesCatalogAndInventory(t *testing.T) {
	t.Parallel()

	installer := &fakeEngineInstaller{state: &speech.State{
		Packs: []speech.InstalledPack{{ID: speech.PackIDSTTCore, Kind: speech.PackSTT}},
	}}
	engine := newTestEngine(t, installer, &fakeCatalogSource{catalog: engineTestCatalog()})

	reply, err := engine.GetEngine(context.Background(), &v1.GetEngineRequest{})
	if err != nil {
		t.Fatalf("GetEngine: %v", err)
	}
	got := reply.GetEngine()
	if !got.GetInstalled() || got.GetCatalogError() != "" {
		t.Fatalf("status: %+v", got)
	}
	byID := map[string]*v1.EnginePack{}
	for _, p := range got.GetPacks() {
		byID[p.GetId()] = p
	}
	if len(byID) != 3 || !byID[speech.PackIDSTTCore].GetInstalled() || byID["voice-it"].GetInstalled() {
		t.Fatalf("packs merged wrong: %+v", got.GetPacks())
	}
}

func TestGetEngineSurvivesCatalogOutage(t *testing.T) {
	t.Parallel()

	installer := &fakeEngineInstaller{state: &speech.State{
		Packs: []speech.InstalledPack{{ID: "voice-it", Kind: speech.PackVoice, Lang: "it"}},
	}}
	engine := newTestEngine(t, installer, &fakeCatalogSource{err: errors.New("offline")})

	reply, err := engine.GetEngine(context.Background(), &v1.GetEngineRequest{})
	if err != nil {
		t.Fatalf("GetEngine: %v", err)
	}
	got := reply.GetEngine()
	if got.GetCatalogError() == "" {
		t.Fatal("catalog outage not reported")
	}
	if len(got.GetPacks()) != 1 || !got.GetPacks()[0].GetInstalled() {
		t.Fatalf("installed inventory lost offline: %+v", got.GetPacks())
	}
}

func TestInstallEnginePackExtendsConfiguration(t *testing.T) {
	t.Parallel()

	installer := &fakeEngineInstaller{state: &speech.State{}}
	engine := newTestEngine(t, installer, &fakeCatalogSource{catalog: engineTestCatalog()})
	events := engine.Subscribe(t.Context())

	if _, err := engine.InstallEnginePack(
		context.Background(), &v1.InstallEnginePackRequest{Id: "voice-it"},
	); err != nil {
		t.Fatalf("install: %v", err)
	}
	waitEngineEvent(t, events, speech.PhaseDone)

	voices := engine.holder.Current().Providers.Local.TTS.Voices
	found := false
	for _, voice := range voices {
		if string(voice.Language) == "it" && voice.Voice == "models/tts/custom-it.onnx" {
			found = true
		}
	}
	if !found {
		t.Fatalf("voice capability not extended: %+v", voices)
	}
}

func TestInstallEnginePackRejectsUnknownAndBusy(t *testing.T) {
	t.Parallel()

	installer := &fakeEngineInstaller{state: &speech.State{}, gate: make(chan struct{})}
	engine := newTestEngine(t, installer, &fakeCatalogSource{catalog: engineTestCatalog()})
	events := engine.Subscribe(t.Context())

	if _, err := engine.InstallEnginePack(
		context.Background(), &v1.InstallEnginePackRequest{Id: "nope"},
	); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unknown pack: %v", err)
	}

	if _, err := engine.InstallEnginePack(
		context.Background(), &v1.InstallEnginePackRequest{Id: "voice-it"},
	); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if _, err := engine.InstallEnginePack(
		context.Background(), &v1.InstallEnginePackRequest{Id: "mt-it-en"},
	); status.Code(err) != codes.Aborted {
		t.Fatalf("busy install not rejected: %v", err)
	}

	close(installer.gate)
	waitEngineEvent(t, events, speech.PhaseDone)
}

func TestRemoveEnginePackRetiresConfiguration(t *testing.T) {
	t.Parallel()

	installer := &fakeEngineInstaller{state: &speech.State{
		Packs: []speech.InstalledPack{{ID: "mt-it-en", Kind: speech.PackMT, From: "it", To: "en"}},
	}}
	engine := newTestEngine(t, installer, &fakeCatalogSource{catalog: engineTestCatalog()})

	if _, err := engine.RemoveEnginePack(
		context.Background(), &v1.RemoveEnginePackRequest{Id: speech.PackIDSTTCore},
	); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("stt-core removal must be refused: %v", err)
	}

	if _, err := engine.RemoveEnginePack(
		context.Background(), &v1.RemoveEnginePackRequest{Id: "voice-en"},
	); status.Code(err) != codes.NotFound {
		t.Fatalf("missing pack removal: %v", err)
	}

	if _, err := engine.RemoveEnginePack(
		context.Background(), &v1.RemoveEnginePackRequest{Id: "mt-it-en"},
	); err != nil {
		t.Fatalf("remove: %v", err)
	}
	for _, pair := range engine.holder.Current().Providers.Local.MT.Pairs {
		if string(pair.From) == "it" && string(pair.To) == "en" {
			t.Fatal("route capability not retired")
		}
	}
	if len(installer.removed) != 1 || installer.removed[0] != "mt-it-en" {
		t.Fatalf("installer removals: %v", installer.removed)
	}
}

func TestInstallEngineRuntimeRequiresCatalog(t *testing.T) {
	t.Parallel()

	engine := newTestEngine(t, &fakeEngineInstaller{}, &fakeCatalogSource{err: errors.New("offline")})
	if _, err := engine.InstallEngineRuntime(
		context.Background(), &v1.InstallEngineRuntimeRequest{},
	); status.Code(err) != codes.Unavailable {
		t.Fatalf("runtime install without catalog: %v", err)
	}
}

func TestEngineProgressReachesSubscribers(t *testing.T) {
	t.Parallel()

	installer := &fakeEngineInstaller{state: &speech.State{}}
	engine := newTestEngine(t, installer, &fakeCatalogSource{catalog: engineTestCatalog()})
	events := engine.Subscribe(t.Context())

	if err := engine.begin(engineOpInstallPack, "voice-it"); err != nil {
		t.Fatalf("begin: %v", err)
	}
	engine.Progress(speech.Progress{Phase: speech.PhaseDownload, Item: "voice-it", DoneBytes: 5, TotalBytes: 10})

	event := waitEngineEvent(t, events, speech.PhaseDownload)
	if event.PackID != "voice-it" || event.DoneBytes != 5 || event.TotalBytes != 10 {
		t.Fatalf("progress event: %+v", event)
	}
	engine.finish(engineOpInstallPack, "voice-it", nil)
	waitEngineEvent(t, events, speech.PhaseDone)
}
