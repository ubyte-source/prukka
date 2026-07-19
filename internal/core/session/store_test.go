package session_test

import (
	"context"
	"errors"
	"slices"
	"strings"
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

func TestCreateEnforcesConfiguredCapacityAtomically(t *testing.T) {
	t.Parallel()

	st := session.NewStore(session.WithMaxSessions(2))
	for _, slug := range []string{"one", "two"} {
		if err := st.Create(demo(slug)); err != nil {
			t.Fatalf("Create(%s) returned error: %v", slug, err)
		}
	}
	if err := st.Create(demo("three")); !errors.Is(err, session.ErrCapacity) {
		t.Fatalf("third Create error = %v, want ErrCapacity", err)
	}
	if got := st.Count(); got != 2 {
		t.Fatalf("Count = %d after rejection, want 2", got)
	}
	if err := st.Create(demo("one")); !errors.Is(err, session.ErrExists) {
		t.Fatalf("duplicate at capacity error = %v, want ErrExists", err)
	}
	if err := st.Delete("one"); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	if err := st.Create(demo("three")); err != nil {
		t.Fatalf("Create after Delete returned error: %v", err)
	}
}

func TestCreateValidatesSourceURLWithoutEchoingSecrets(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		url  string
	}{
		{name: "empty", url: ""},
		{name: "unsupported", url: "https://example.test/private"},
		{name: "uppercase scheme", url: "RTMP://live.example/in/key"},
		{name: "missing host", url: "rtmp:///in/key"},
		{name: "missing stream path", url: "rtmp://live.example"},
		{name: "missing device id", url: "device://audio"},
		{name: "unsupported device kind", url: "device://video/0"},
		{name: "unpaired AV device", url: "device://av/0"},
		{name: "control character", url: "srt://relay.example:9000\nsecret"},
		{name: "malformed query", url: "file:///tmp/take.wav?loop=%zz"},
		{name: "invalid loop", url: "file:///tmp/take.wav?loop=yes"},
		{name: "duplicate loop", url: "file:///tmp/take.wav?loop=true&loop=false"},
		{name: "unsupported file query", url: "file:///tmp/take.wav?repeat=true"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			candidate := demo("bad-source")
			candidate.Source.URL = tc.url
			err := session.NewStore().Create(candidate)
			if !errors.Is(err, session.ErrInvalidSource) {
				t.Fatalf("Create error = %v, want ErrInvalidSource", err)
			}
			if tc.url != "" && strings.Contains(err.Error(), tc.url) {
				t.Fatalf("validation error echoed source URL: %v", err)
			}
		})
	}
}

func TestCreateAcceptsLiteralPercentInFilePath(t *testing.T) {
	t.Parallel()

	candidate := demo("percent-path")
	candidate.Source.URL = "file:///tmp/100%mix.wav?loop=false"
	if err := session.NewStore().Create(candidate); err != nil {
		t.Fatalf("Create rejected a literal percent in a file path: %v", err)
	}
}

func TestCreateAcceptsPlatformDeviceIdentifiers(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		"device://audio/Microphone (USB)? #1",
		"device://av//dev/video0|alsa_input.usb-mic",
	} {
		candidate := demo("device-source")
		candidate.Source.URL = source
		if err := session.NewStore().Create(candidate); err != nil {
			t.Errorf("Create rejected %q: %v", source, err)
		}
	}
}

func TestCreateAcceptsCredentialBearingSource(t *testing.T) {
	t.Parallel()

	candidate := demo("credentials")
	candidate.Source.URL = "rtmp://user:pass@live.example/in/stream-key?token=secret#fragment"
	if err := session.NewStore().Create(candidate); err != nil {
		t.Fatalf("Create rejected a valid credential-bearing URL: %v", err)
	}
}

func TestGetReturnsIsolatedCopy(t *testing.T) {
	t.Parallel()

	st := session.NewStore()
	input := demo("demo")
	input.Flags = map[string]string{"subs": "vtt"}
	if err := st.Create(input); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	input.Langs[0] = "fr"
	input.Flags["subs"] = "off"

	first, err := st.Get("demo")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	assertOwnedSession(t, "Create input", &first)

	first.Langs[0] = "xx"
	first.Flags["subs"] = "off"

	second, err := st.Get("demo")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}

	assertOwnedSession(t, "Get output", &second)

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

func TestUpdateLangsCheckedRejectsWithoutMutationOrEvent(t *testing.T) {
	t.Parallel()

	st := session.NewStore()
	if err := st.Create(demo("checked")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	events := st.Subscribe(t.Context())
	rejected := errors.New("unsupported provider capability")
	if _, err := st.UpdateLangsChecked("checked", []core.Lang{"de"}, nil, func(candidate session.Session) error {
		candidate.Langs[0] = "fr"
		return rejected
	}); !errors.Is(err, rejected) {
		t.Fatalf("UpdateLangsChecked error = %v, want rejection", err)
	}

	got, err := st.Get("checked")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if want := []core.Lang{"it", "en"}; !slices.Equal(got.Langs, want) {
		t.Fatalf("rejected candidate changed languages to %v, want %v", got.Langs, want)
	}
	select {
	case event := <-events:
		t.Fatalf("rejected update emitted event %+v", event)
	default:
	}
}

func TestUpdateLangsPrunesTheDubbedSubset(t *testing.T) {
	t.Parallel()

	st := session.NewStore()
	s := demo("subset")
	// Non-canonical spellings must survive pruning as canonical tags.
	s.Flags = map[string]string{"dub_langs": "IT,en"}
	if err := st.Create(s); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	got, err := st.UpdateLangs("subset", []core.Lang{"de"}, []core.Lang{"en"})
	if err != nil {
		t.Fatalf("UpdateLangs returned error: %v", err)
	}
	if got.Flags["dub_langs"] != "it" {
		t.Fatalf("dub_langs = %q, want removed language pruned", got.Flags["dub_langs"])
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

func TestDeleteRemovesOnlyAReciprocalPair(t *testing.T) {
	t.Parallel()

	st := session.NewStore()
	inbound := demo("meeting")
	inbound.Flags = map[string]string{"pair": "meeting-out"}
	outbound := demo("meeting-out")
	outbound.Flags = map[string]string{"pair": "meeting"}
	unrelated := demo("other")
	unrelated.Flags = map[string]string{"pair": "meeting"}
	for _, candidate := range []*session.Session{inbound, outbound, unrelated} {
		if err := st.Create(candidate); err != nil {
			t.Fatalf("Create(%s): %v", candidate.Slug, err)
		}
	}

	if err := st.Delete("meeting"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if st.Count() != 1 {
		t.Fatalf("Count = %d, want only the unrelated session", st.Count())
	}
	if _, err := st.Get("other"); err != nil {
		t.Fatalf("unrelated session was removed: %v", err)
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

func TestSubscriberEventsDoNotAlias(t *testing.T) {
	t.Parallel()

	st := session.NewStore()
	ctx := t.Context()

	first := st.Subscribe(ctx)
	second := st.Subscribe(ctx)
	candidate := demo("isolated-events")
	candidate.Flags = map[string]string{"subs": "vtt"}
	if err := st.Create(candidate); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	firstEvent := <-first
	secondEvent := <-second
	firstEvent.Session.Langs[0] = "fr"
	firstEvent.Session.Flags["subs"] = "off"

	assertOwnedSession(t, "second event", &secondEvent.Session)

	stored, err := st.Get(candidate.Slug)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	assertOwnedSession(t, "stored session", &stored)
}

func assertOwnedSession(t *testing.T, label string, got *session.Session) {
	t.Helper()

	if got.Langs[0] != "it" {
		t.Fatalf("%s language aliases caller data: %+v", label, got)
	}
	if got.Flags["subs"] != "vtt" {
		t.Fatalf("%s flags alias caller data: %+v", label, got)
	}
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
