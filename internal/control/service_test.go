package control_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ubyte-source/prukka/internal/control"
	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/lang"
	"github.com/ubyte-source/prukka/internal/core/session"
	"github.com/ubyte-source/prukka/internal/doctor"
	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
	"github.com/ubyte-source/prukka/internal/media/discover"
	"github.com/ubyte-source/prukka/internal/testkit"
)

// stubPusher records push requests for the push RPC test.
type stubPusher struct {
	calls    [][4]string
	failed   bool
	attempts int
}

// Push implements control.Pusher.
func (p *stubPusher) Push(slug, tag, target, subs string) error {
	p.attempts++
	if p.failed {
		return fmt.Errorf("warming up: %w", core.ErrNotReady)
	}

	p.calls = append(p.calls, [4]string{slug, tag, target, subs})

	return nil
}

// newTestService wires a Service with an inert doctor, fixed defaults and
// the given pusher; its settings surface edits a throwaway config file.
func newTestService(t *testing.T, store *session.Store, pusher control.Pusher) *control.Service {
	t.Helper()

	return newTestServiceWithCapability(t, store, pusher, nil)
}

func newTestServiceWithCapability(
	t *testing.T, store *session.Store, pusher control.Pusher, capable control.SessionCapabilityFunc,
) *control.Service {
	t.Helper()

	return control.NewService(store, "test",
		func() []doctor.Check { return nil },
		func() control.SessionDefaults {
			return control.SessionDefaults{
				Langs: []core.Lang{"it", "en"},
				Subs:  "vtt",
				Bed:   "-15dB",
				Delay: 8 * time.Second,
			}
		},
		func(context.Context) []discover.Device {
			return []discover.Device{{URL: "device://audio/1", Label: "Test Mic", Kind: discover.AudioIn}}
		},
		nil, capable, pusher, newTestSettings(t), nil)
}

func TestCreateSessionAppliesDefaults(t *testing.T) {
	t.Parallel()

	svc := newTestService(t, session.NewStore(), &stubPusher{})

	// The wizard omits delay and bed; the server must seed them.
	reply, err := svc.CreateSession(t.Context(), &v1.CreateSessionRequest{Session: &v1.Session{
		Slug:      "wiz",
		Profile:   "broadcast",
		SourceUrl: "file:///tmp/x.wav",
		Flags:     map[string]string{"subs": "off"},
	}})
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}

	got := reply.GetSession()

	if got.GetDelaySeconds() != 8 {
		t.Fatalf("delay = %v, want default 8", got.GetDelaySeconds())
	}
	if gotLangs := got.GetLangs(); len(gotLangs) != 2 || gotLangs[0] != "it" || gotLangs[1] != "en" {
		t.Fatalf("langs = %v, want configured defaults [it en]", gotLangs)
	}

	if got.GetFlags()["subs"] != "off" || got.GetFlags()["bed"] != "-15dB" {
		t.Fatalf("flags = %v, want explicit subs kept and bed defaulted", got.GetFlags())
	}
	assertSourceProjection(t, got)
	assertStartingSession(t, got)
}

func TestCreateSessionRejectsUnavailableCapability(t *testing.T) {
	t.Parallel()

	store := session.NewStore()
	svc := newTestServiceWithCapability(t, store, &stubPusher{}, func(*session.Session) error {
		return errors.New("translation model unavailable for it to de")
	})

	_, err := svc.CreateSession(t.Context(), &v1.CreateSessionRequest{Session: &v1.Session{
		Slug:      "unsupported",
		Profile:   "broadcast",
		SourceUrl: "file:///tmp/x.wav",
		Langs:     []string{"de"},
		Flags:     map[string]string{"source": "it"},
	}})
	if status.Code(err) != codes.FailedPrecondition || store.Count() != 0 {
		t.Fatalf("CreateSession = (error %v, stored %d), want FailedPrecondition and no session", err, store.Count())
	}
}

func TestCreateSessionReportsStoreCapacity(t *testing.T) {
	t.Parallel()

	store := session.NewStore(session.WithMaxSessions(1))
	svc := newTestService(t, store, &stubPusher{})
	request := func(slug string) *v1.CreateSessionRequest {
		return &v1.CreateSessionRequest{Session: &v1.Session{
			Slug: slug, Profile: "broadcast", SourceUrl: "file:///tmp/x.wav", Langs: []string{"it"},
		}}
	}
	if _, err := svc.CreateSession(t.Context(), request("first")); err != nil {
		t.Fatalf("first CreateSession returned error: %v", err)
	}
	if _, err := svc.CreateSession(t.Context(), request("second")); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("second CreateSession error = %v, want ResourceExhausted", err)
	}
}

func TestCreateCallSessionDefaultsToZeroDelay(t *testing.T) {
	t.Parallel()

	svc := newTestService(t, session.NewStore(), &stubPusher{})

	// The configured delay aligns broadcast renditions; an omitted delay
	// on a live call must not postpone the interpreter by eight seconds.
	reply, err := svc.CreateSession(t.Context(), &v1.CreateSessionRequest{Session: &v1.Session{
		Slug:      "live-call",
		Profile:   "call",
		SourceUrl: "device://audio/1",
		Langs:     []string{"it"},
	}})
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}

	if got := reply.GetSession().GetDelaySeconds(); got != 0 {
		t.Fatalf("call delay = %vs, want 0 (immediate playout)", got)
	}
	flags := reply.GetSession().GetFlags()
	if flags["bed"] != "off" {
		t.Fatalf("call bed = %q, want off — the far side must hear only the translation", flags["bed"])
	}
	if flags["subs"] == "burn" {
		t.Fatalf("call subs = burn, want a non-video default")
	}
}

func assertSourceProjection(t *testing.T, got *v1.Session) {
	t.Helper()

	if got.GetSourceUrl() != "" || got.GetSourceLabel() != "file://[local]" {
		t.Fatalf("source read projection = %q/%q, want empty input and public label",
			got.GetSourceUrl(), got.GetSourceLabel())
	}
}

func assertStartingSession(t *testing.T, got *v1.Session) {
	t.Helper()

	if got.GetStatus() != "starting" || got.GetError() != "" {
		t.Fatalf("runtime = %q/%q, want starting without error", got.GetStatus(), got.GetError())
	}
}

func TestUpdateAndListSessionsExposeRuntimeState(t *testing.T) {
	t.Parallel()

	store := session.NewStore()
	svc := newTestService(t, store, &stubPusher{})
	if _, err := svc.CreateSession(t.Context(), &v1.CreateSessionRequest{Session: &v1.Session{
		Slug: "runtime", Profile: "broadcast", SourceUrl: "file:///tmp/x.wav", Langs: []string{"it"},
	}}); err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}

	updated, err := svc.UpdateSession(t.Context(), &v1.UpdateSessionRequest{
		Slug: "runtime", AddLangs: []string{"en"},
	})
	if err != nil {
		t.Fatalf("UpdateSession returned error: %v", err)
	}
	if got := updated.GetSession().GetStatus(); got != "starting" {
		t.Fatalf("updated status = %q, want starting", got)
	}

	listed, err := svc.ListSessions(t.Context(), &v1.ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions returned error: %v", err)
	}
	if len(listed.GetSessions()) != 1 || listed.GetSessions()[0].GetStatus() != "starting" {
		t.Fatalf("listed sessions = %+v, want one starting session", listed.GetSessions())
	}
	if got := listed.GetSessions()[0]; got.GetSourceUrl() != "" || got.GetSourceLabel() != "file://[local]" {
		t.Fatalf("listed source = %q/%q, want empty input and public label",
			got.GetSourceUrl(), got.GetSourceLabel())
	}
}

func TestUpdateSessionRejectsUnavailableCapabilityAtomically(t *testing.T) {
	t.Parallel()

	store := session.NewStore()
	svc := newTestServiceWithCapability(t, store, &stubPusher{}, func(candidate *session.Session) error {
		if slices.Contains(candidate.Langs, "de") {
			return errors.New("translation model unavailable for it to de")
		}

		return nil
	})
	if _, err := svc.CreateSession(t.Context(), &v1.CreateSessionRequest{Session: &v1.Session{
		Slug:      "supported-update",
		Profile:   "broadcast",
		SourceUrl: "file:///tmp/x.wav",
		Langs:     []string{"en"},
		Flags:     map[string]string{"source": "it"},
	}}); err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}

	_, err := svc.UpdateSession(t.Context(), &v1.UpdateSessionRequest{
		Slug: "supported-update", AddLangs: []string{"de"},
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("UpdateSession error = %v, want FailedPrecondition", err)
	}

	stored, getErr := store.Get("supported-update")
	if getErr != nil {
		t.Fatalf("Get returned error: %v", getErr)
	}
	if len(stored.Langs) != 1 || stored.Langs[0] != "en" {
		t.Fatalf("rejected update mutated session: %+v", stored)
	}
}

func TestCreateSessionPreservesExplicitZeroDelay(t *testing.T) {
	t.Parallel()

	zero := 0.0
	svc := newTestService(t, session.NewStore(), &stubPusher{})
	reply, err := svc.CreateSession(t.Context(), &v1.CreateSessionRequest{Session: &v1.Session{
		Slug:         "zero",
		Profile:      "broadcast",
		SourceUrl:    "file:///tmp/x.wav",
		Langs:        []string{"it"},
		DelaySeconds: &zero,
	}})
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	if reply.GetSession().GetDelaySeconds() != 0 {
		t.Fatalf("explicit zero became delay %v", reply.GetSession().GetDelaySeconds())
	}
}

func TestListLanguagesMirrorsTheRegistry(t *testing.T) {
	t.Parallel()

	svc := newTestService(t, session.NewStore(), &stubPusher{})

	reply, err := svc.ListLanguages(t.Context(), &v1.ListLanguagesRequest{})
	if err != nil {
		t.Fatalf("ListLanguages returned error: %v", err)
	}

	registry := lang.All()
	if len(reply.GetLanguages()) != len(registry) {
		t.Fatalf("wire registry has %d entries, want %d", len(reply.GetLanguages()), len(registry))
	}

	first := reply.GetLanguages()[0]
	want := registry[0]

	if first.GetTag() != string(want.Tag) || first.GetLabel() != want.Label() {
		t.Fatalf("first entry = %s/%s, want %s/%s", first.GetTag(), first.GetLabel(), want.Tag, want.Label())
	}
}

func TestPushValidatesAndForwards(t *testing.T) {
	t.Parallel()

	store := session.NewStore()
	startRunningSession(t, store, &session.Session{
		Slug:    "demo",
		Profile: session.ProfileBroadcast,
		Source:  core.SourceSpec{URL: "file:///tmp/x.wav"},
		Langs:   []core.Lang{"it", "en"},
	})

	pusher := &stubPusher{}
	svc := newTestService(t, store, pusher)

	// A target the session enables reaches the pusher, overlay mode intact.
	if _, err := svc.Push(t.Context(), &v1.PushRequest{
		Slug: "demo", Lang: "en", TargetUrl: "rtmp://example/live", Subs: "burn",
	}); err != nil {
		t.Fatalf("Push returned error: %v", err)
	}

	if len(pusher.calls) != 1 || pusher.calls[0] != [4]string{"demo", "en", "rtmp://example/live", "burn"} {
		t.Fatalf("pusher calls = %v, want one demo/en burn push", pusher.calls)
	}

	// A language the session does not target is rejected before the pusher.
	if _, err := svc.Push(t.Context(), &v1.PushRequest{
		Slug: "demo", Lang: "de", TargetUrl: "rtmp://example/live",
	}); err == nil {
		t.Fatal("Push accepted a non-target language")
	}

	// An unknown session is a not-found, not a push.
	if _, err := svc.Push(t.Context(), &v1.PushRequest{
		Slug: "ghost", Lang: "en", TargetUrl: "rtmp://example/live",
	}); err == nil {
		t.Fatal("Push accepted an unknown session")
	}

	if len(pusher.calls) != 1 {
		t.Fatalf("pusher saw %d calls, want the validation failures to short-circuit", len(pusher.calls))
	}

	pusher.failed = true
	if _, err := svc.Push(t.Context(), &v1.PushRequest{
		Slug: "demo", Lang: "en", TargetUrl: "rtmp://example/live",
	}); status.Code(err) != codes.Unavailable {
		t.Fatalf("not-ready push code = %v, want Unavailable", status.Code(err))
	}
}

func TestPushLetsTheMediaRegistryDecideStartingReadiness(t *testing.T) {
	t.Parallel()

	starting := session.NewStore()
	candidate := &session.Session{
		Slug: "starting", Profile: session.ProfileBroadcast,
		Source: core.SourceSpec{URL: "file:///tmp/x.wav"}, Langs: []core.Lang{"en"},
	}
	if err := starting.Create(candidate); err != nil {
		t.Fatalf("Create starting session: %v", err)
	}
	pusher := &stubPusher{failed: true}
	svc := newTestService(t, starting, pusher)
	_, err := svc.Push(t.Context(), &v1.PushRequest{
		Slug: "starting", Lang: "en", TargetUrl: "rtmp://live.example/out", Subs: "off",
	})
	if status.Code(err) != codes.Unavailable || pusher.attempts != 1 {
		t.Fatalf("starting push = %v with %d attempts, want media-owned Unavailable", err, pusher.attempts)
	}
}

func TestPushRejectsTerminalSessionsBeforeThePusher(t *testing.T) {
	t.Parallel()

	assertTerminalPushBlocked(t, "failed", func(context.Context, *session.Session, func()) error {
		return errors.New("provider unavailable")
	})
	assertTerminalPushBlocked(t, "finished", func(context.Context, *session.Session, func()) error {
		return nil
	})
}

func assertTerminalPushBlocked(t *testing.T, slug string, starter session.LaneStarter) {
	t.Helper()

	store := session.NewStore()
	ctx, cancel := context.WithCancel(t.Context())
	runtime := session.NewRuntime(store, starter, nil, nil, slog.New(slog.DiscardHandler))
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("runtime shutdown: %v", err)
		}
	})
	if err := store.Create(&session.Session{
		Slug: slug, Profile: session.ProfileBroadcast,
		Source: core.SourceSpec{URL: "file:///tmp/x.wav"}, Langs: []core.Lang{"en"},
	}); err != nil {
		t.Fatalf("Create %s session: %v", slug, err)
	}
	waitRuntimeState(t, store, slug, map[string]bool{slug: true})
	assertPushBlocked(t, store, slug, codes.FailedPrecondition)
}

func assertPushBlocked(t *testing.T, store *session.Store, slug string, want codes.Code) {
	t.Helper()

	pusher := &stubPusher{}
	svc := newTestService(t, store, pusher)
	started := time.Now()
	_, err := svc.Push(t.Context(), &v1.PushRequest{
		Slug: slug, Lang: "en", TargetUrl: "rtmp://live.example/out", Subs: "off",
	})
	if status.Code(err) != want {
		t.Fatalf("Push code = %v, want %v", status.Code(err), want)
	}
	if len(pusher.calls) != 0 {
		t.Fatalf("blocked push reached pusher: %v", pusher.calls)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("blocked push took %s, want immediate state rejection", elapsed)
	}
}

func startRunningSession(t *testing.T, store *session.Store, candidate *session.Session) {
	t.Helper()

	started := make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	runtime := session.NewRuntime(store, func(ctx context.Context, _ *session.Session, running func()) error {
		running()
		close(started)
		<-ctx.Done()

		return ctx.Err()
	}, nil, nil, slog.New(slog.DiscardHandler))
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("runtime shutdown: %v", err)
		}
	})
	if err := store.Create(candidate); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("session did not reach running")
	}
}

func waitRuntimeState(t *testing.T, store *session.Store, slug string, expected map[string]bool) {
	t.Helper()

	testkit.Eventually(t, time.Second, func() bool {
		stored, err := store.Get(slug)

		return err == nil && expected[string(stored.Runtime().State)]
	}, fmt.Sprintf("session %q reached one of %v", slug, expected))
}

// TestListDevicesConverts: the wired enumeration reaches the RPC intact.
func TestListDevicesConverts(t *testing.T) {
	t.Parallel()

	svc := newTestService(t, session.NewStore(), &stubPusher{})

	reply, err := svc.ListDevices(t.Context(), &v1.ListDevicesRequest{})
	if err != nil {
		t.Fatalf("ListDevices returned error: %v", err)
	}

	devices := reply.GetDevices()
	if len(devices) != 1 || devices[0].GetUrl() != "device://audio/1" || devices[0].GetKind() != "audio-in" {
		t.Fatalf("devices = %v, want the wired test mic", devices)
	}
}
