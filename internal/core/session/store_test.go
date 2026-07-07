package session_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/session"
)

// demo returns a valid session for tests to mutate.
func demo(slug string) *session.Session {
	return &session.Session{
		Slug:    slug,
		Profile: session.ProfileBroadcast,
		Source:  core.SourceSpec{URL: "rtmp://0.0.0.0:1935/in/" + slug},
		Langs:   []core.Lang{"it", "en"},
	}
}

func TestCreateDuplicate(t *testing.T) {
	t.Parallel()

	st := session.NewStore()
	if err := st.Create(demo("demo")); err != nil {
		t.Fatalf("first Create returned error: %v", err)
	}

	if err := st.Create(demo("demo")); !errors.Is(err, session.ErrExists) {
		t.Fatalf("second Create error = %v, want ErrExists", err)
	}
}

func TestGetReturnsIsolatedCopy(t *testing.T) {
	t.Parallel()

	st := session.NewStore()
	if err := st.Create(demo("demo")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	first, err := st.Get("demo")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}

	first.Langs[0] = "xx"

	second, err := st.Get("demo")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}

	if second.Langs[0] != "it" {
		t.Fatal("mutating a returned session leaked into the store")
	}

	if _, err := st.Get("ghost"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("Get(ghost) error = %v, want ErrNotFound", err)
	}
}

func TestListOrdersBySlug(t *testing.T) {
	t.Parallel()

	st := session.NewStore()
	for _, slug := range []string{"zeta", "alpha", "mid"} {
		if err := st.Create(demo(slug)); err != nil {
			t.Fatalf("Create(%s) returned error: %v", slug, err)
		}
	}

	got := st.List()
	want := []string{"alpha", "mid", "zeta"}

	if len(got) != len(want) {
		t.Fatalf("List returned %d sessions, want %d", len(got), len(want))
	}

	for i, s := range got {
		if s.Slug != want[i] {
			t.Fatalf("List[%d].Slug = %q, want %q", i, s.Slug, want[i])
		}
	}

	if st.Count() != 3 {
		t.Fatalf("Count = %d, want 3", st.Count())
	}
}

func TestUpdateLangs(t *testing.T) {
	t.Parallel()

	st := session.NewStore()
	if err := st.Create(demo("demo")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	got, err := st.UpdateLangs("demo", []core.Lang{"fr", "it"}, []core.Lang{"en"})
	if err != nil {
		t.Fatalf("UpdateLangs returned error: %v", err)
	}

	want := []core.Lang{"it", "fr"}
	if len(got.Langs) != len(want) {
		t.Fatalf("Langs = %v, want %v", got.Langs, want)
	}

	for i := range want {
		if got.Langs[i] != want[i] {
			t.Fatalf("Langs = %v, want %v", got.Langs, want)
		}
	}

	if _, err := st.UpdateLangs("demo", nil, want); !errors.Is(err, session.ErrNoLanguages) {
		t.Fatalf("removing all languages error = %v, want ErrNoLanguages", err)
	}

	if _, err := st.UpdateLangs("ghost", []core.Lang{"fr"}, nil); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("UpdateLangs(ghost) error = %v, want ErrNotFound", err)
	}
}

func TestDelete(t *testing.T) {
	t.Parallel()

	st := session.NewStore()
	if err := st.Create(demo("demo")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	if err := st.Delete("demo"); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}

	if err := st.Delete("demo"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("second Delete error = %v, want ErrNotFound", err)
	}
}

func TestSubscribeReceivesLifecycleEvents(t *testing.T) {
	t.Parallel()

	st := session.NewStore()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := st.Subscribe(ctx)

	if err := st.Create(demo("demo")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	if _, err := st.UpdateLangs("demo", []core.Lang{"fr"}, nil); err != nil {
		t.Fatalf("UpdateLangs returned error: %v", err)
	}

	if err := st.Delete("demo"); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}

	want := []session.EventType{session.EventCreated, session.EventUpdated, session.EventDeleted}
	for _, wantType := range want {
		expectEvent(t, events, wantType)
	}

	cancel()
	expectClosed(t, events)
}

// expectEvent asserts the next event on the channel has the wanted type.
func expectEvent(t *testing.T, events <-chan session.Event, want session.EventType) {
	t.Helper()

	select {
	case e := <-events:
		if e.Type != want || e.Session.Slug != "demo" {
			t.Fatalf("event = %v/%s, want %v/demo", e.Type, e.Session.Slug, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %v event", want)
	}
}

// expectClosed asserts the store closes the channel after unsubscription,
// draining any still-buffered events first.
func expectClosed(t *testing.T, events <-chan session.Event) {
	t.Helper()

	deadline := time.After(time.Second)

	for {
		select {
		case _, open := <-events:
			if !open {
				return
			}
		case <-deadline:
			t.Fatal("subscriber channel not closed after cancellation")
		}
	}
}
