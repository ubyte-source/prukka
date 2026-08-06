package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
)

// foreignTextError stands in for any error declaring text it did not author.
type foreignTextError struct {
	cause error
	tail  string
}

func (e *foreignTextError) Error() string     { return e.cause.Error() + "; stderr: " + e.tail }
func (e *foreignTextError) Unwrap() error     { return e.cause }
func (e *foreignTextError) Untrusted() string { return e.tail }

func TestRuntimeErrorIsBounded(t *testing.T) {
	t.Parallel()

	detail := sanitizeRuntimeError(errors.New("lane stalled\n" + strings.Repeat("x", 900)))

	if len(detail) > maxRuntimeErrorBytes {
		t.Fatalf("detail has %d bytes, limit is %d", len(detail), maxRuntimeErrorBytes)
	}
	if strings.Contains(detail, "\n") {
		t.Fatalf("detail kept a line break: %q", detail)
	}
	if !strings.HasSuffix(detail, runtimeErrorEllipsis) {
		t.Fatalf("truncated detail lost its ellipsis: %q", detail)
	}
}

// TestRuntimeErrorDropsDeclaredForeignText covers what no path pattern can: a
// child's own prose, naming a path that carries spaces on both platforms.
func TestRuntimeErrorDropsDeclaredForeignText(t *testing.T) {
	t.Parallel()

	tail := "whisper_init: failed to load /Users/alice/Secret Model/model.bin\n" +
		"fallback C:\\Users\\Alice\\Voice Models\\it.bin also failed\n" +
		"authorization=Bearer sk-live-secret"
	detail := sanitizeRuntimeError(fmt.Errorf("captions disabled: %w",
		&foreignTextError{cause: errors.New("native stt helper: signal: killed"), tail: tail}))

	for _, secret := range []string{
		"alice", "Alice", "Secret Model", "model.bin", "Voice Models", "it.bin", "sk-live-secret",
	} {
		if strings.Contains(detail, secret) {
			t.Fatalf("detail exposes %q: %q", secret, detail)
		}
	}
	for _, kept := range []string{"captions disabled", "native stt helper: signal: killed", foreignTextLabel} {
		if !strings.Contains(detail, kept) {
			t.Fatalf("detail lost %q: %q", kept, detail)
		}
	}
}

// TestRuntimeErrorRedactsPathErrorsAndFormatControls covers the paths this
// program never chose to render: the stdlib puts them in a typed field.
func TestRuntimeErrorRedactsPathErrorsAndFormatControls(t *testing.T) {
	t.Parallel()

	posix := "/Users/alice/My Secret, take (final):1.wav"
	windows := `C:\Users\Alice\secret.wav`
	err := errors.Join(
		fmt.Errorf("open: %w", &os.PathError{Op: "open", Path: posix, Err: errors.New("denied")}),
		fmt.Errorf("read %q: %w", windows, &os.PathError{
			Op:   "read",
			Path: windows,
			Err:  errors.New("denied"),
		}),
		fmt.Errorf("link: %w", &os.LinkError{
			Op:  "rename",
			Old: posix,
			New: windows,
			Err: errors.New("denied"),
		}),
		errors.New("\x1b[31m\u202Espoof\u2066"),
	)

	detail := sanitizeRuntimeError(err)
	for _, secret := range []string{
		"/Users", "alice", "Alice", "My Secret", "secret.wav", "\x1b", "\u202E", "\u2066",
	} {
		if strings.Contains(detail, secret) {
			t.Fatalf("detail exposes %q: %q", secret, detail)
		}
	}
	if !strings.Contains(detail, localPathLabel) {
		t.Fatalf("detail lost the path label: %q", detail)
	}
}

func TestRuntimeErrorRedactsEveryJoinedPathBranch(t *testing.T) {
	t.Parallel()

	first := "/Users/alice/First Folder/voice, take (one).wav"
	second := `/Users/bob/Second "Quoted" Folder/final:2.wav`
	err := errors.Join(
		fmt.Errorf("first branch: %w", &os.PathError{
			Op:   "open",
			Path: first,
			Err:  errors.New("permission denied"),
		}),
		fmt.Errorf("nested branch printed as %q: %w", second, fmt.Errorf("read: %w", &os.PathError{
			Op:   "open",
			Path: second,
			Err:  errors.New("permission denied"),
		})),
	)

	detail := sanitizeRuntimeError(err)
	for _, secret := range []string{
		"/Users", "alice", "bob", "First Folder", "Second", "Quoted", "final:2.wav",
	} {
		if strings.Contains(detail, secret) {
			t.Fatalf("joined error exposes %q from a non-source branch: %q", secret, detail)
		}
	}
	if count := strings.Count(detail, localPathLabel); count < 2 {
		t.Fatalf("joined error redacted %d paths, want at least two: %q", count, detail)
	}
}

// TestRuntimeErrorKeepsProseIntact pins that a request path or a scheme list is
// prose, not a secret: the runtime status must render it unchanged.
func TestRuntimeErrorKeepsProseIntact(t *testing.T) {
	t.Parallel()

	for _, prose := range []string{
		"GET /healthz returned 503",
		"endpoint /v1/status unreachable",
		"supported schemes are file, rtmp, srt and device",
	} {
		if detail := sanitizeRuntimeError(errors.New(prose)); detail != prose {
			t.Fatalf("sanitize rewrote prose\n got: %q\nwant: %q", detail, prose)
		}
	}
}

func TestStatusEventOwnsItsSessionSnapshot(t *testing.T) {
	t.Parallel()

	store := NewStore()
	candidate := testSession("status-owned")
	candidate.Subs = SubsVTT
	candidate.DubLangs = DubOnly("en")
	mustCreateSession(t, store, candidate)
	stored := mustGetSession(t, store, candidate.Slug)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	events := store.Subscribe(ctx)
	if !store.bindRuntime(incarnation(&stored, 1)) {
		t.Fatal("bindRuntime rejected current definition")
	}
	if !store.setRuntime(incarnation(&stored, 1), StateFailed, errors.New("boom")) {
		t.Fatal("setRuntime rejected current generation")
	}

	event := receiveStatusEvent(t, events)
	event.Session.Subs = SubsOff
	event.Session.DubLangs.langs[0] = "it"
	after := mustGetSession(t, store, candidate.Slug)
	if after.Subs != SubsVTT || after.DubLangs.langs[0] != "en" || after.Runtime().State != StateFailed {
		t.Fatalf("event aliased stored session: subs=%q dub=%v runtime=%+v",
			after.Subs, after.DubLangs.langs, after.Runtime())
	}
}

func mustCreateSession(t *testing.T, store *Store, candidate *Session) {
	t.Helper()

	if err := store.Create(candidate); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
}

func mustGetSession(t *testing.T, store *Store, slug string) Session {
	t.Helper()

	stored, err := store.Get(slug)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}

	return stored
}

func receiveStatusEvent(t *testing.T, events <-chan Event) Event {
	t.Helper()

	select {
	case event := <-events:
		if event.Type != EventStatus || event.Session.Runtime().State != StateFailed {
			t.Fatalf("event = %s/%s, want status/failed", event.Type, event.Session.Runtime().State)
		}

		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for status event")

		return Event{}
	}
}

// incarnation names the lane identity of s at one generation.
func incarnation(s *Session, gen generation) laneID {
	return laneID{slug: s.Slug, revision: s.revision, gen: gen}
}

// TestStaleRuntimeWritersAreRejected pins which value guards what: the
// revision guards the definition, the generation the incarnation.
func TestStaleRuntimeWritersAreRejected(t *testing.T) {
	t.Parallel()

	store := NewStore()
	if err := store.Create(testSession("generation")); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	first, err := store.Get("generation")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if !store.bindRuntime(incarnation(&first, 1)) {
		t.Fatal("first generation did not bind")
	}

	updated, err := store.UpdateLangs(first.Slug, []core.Lang{"fr"}, nil)
	if err != nil {
		t.Fatalf("UpdateLangs returned error: %v", err)
	}
	if store.setRuntime(incarnation(&first, 1), StateRunning, nil) {
		t.Fatal("old revision overwrote its replacement")
	}
	if !store.bindRuntime(incarnation(&updated, 2)) {
		t.Fatal("replacement generation did not bind")
	}
	if store.setRuntime(incarnation(&updated, 1), StateRunning, nil) {
		t.Fatal("old generation overwrote its replacement")
	}

	if err := store.Delete(updated.Slug); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	if store.setRuntime(incarnation(&updated, 2), StateRunning, nil) {
		t.Fatal("deleted lane recreated runtime state")
	}
}
