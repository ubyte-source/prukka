package control_test

import (
	"errors"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/control"
	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/lang"
	"github.com/ubyte-source/prukka/internal/core/session"
	"github.com/ubyte-source/prukka/internal/doctor"
	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
)

// stubPusher records push requests for the push RPC test.
type stubPusher struct {
	calls  [][4]string
	failed bool
}

// Push implements control.Pusher.
func (p *stubPusher) Push(slug, tag, target, subs string) error {
	if p.failed {
		return errors.New("no dubbed audio")
	}

	p.calls = append(p.calls, [4]string{slug, tag, target, subs})

	return nil
}

// newTestService wires a Service with inert doctor/rate, fixed defaults and
// the given pusher; its settings surface edits a throwaway config file.
func newTestService(t *testing.T, store *session.Store, pusher control.Pusher) *control.Service {
	t.Helper()

	return control.NewService(store, "test",
		func() []doctor.Check { return nil },
		func() float64 { return 0 },
		func() control.SessionDefaults {
			return control.SessionDefaults{
				Subs:             "vtt",
				Bed:              "-15dB",
				BudgetEURPerHour: 3,
				Delay:            8 * time.Second,
			}
		},
		pusher, newTestSettings(t))
}

func TestCreateSessionAppliesDefaults(t *testing.T) {
	t.Parallel()

	svc := newTestService(t, session.NewStore(), &stubPusher{})

	// The wizard omits budget, delay and bed; the server must seed them.
	reply, err := svc.CreateSession(t.Context(), &v1.CreateSessionRequest{Session: &v1.Session{
		Slug:      "wiz",
		Profile:   "broadcast",
		SourceUrl: "file:///tmp/x.wav",
		Langs:     []string{"it", "en"},
		Flags:     map[string]string{"subs": "off"},
	}})
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}

	got := reply.GetSession()

	if got.GetBudgetEurPerHour() != 3 || got.GetDelaySeconds() != 8 {
		t.Fatalf("budget/delay = %v/%v, want defaults 3/8", got.GetBudgetEurPerHour(), got.GetDelaySeconds())
	}

	if got.GetFlags()["subs"] != "off" || got.GetFlags()["bed"] != "-15dB" {
		t.Fatalf("flags = %v, want explicit subs kept and bed defaulted", got.GetFlags())
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
	if err := store.Create(&session.Session{
		Slug:    "demo",
		Profile: session.ProfileBroadcast,
		Source:  core.SourceSpec{URL: "file:///tmp/x.wav"},
		Langs:   []core.Lang{"it", "en"},
	}); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

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
}
