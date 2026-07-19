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

func TestRuntimeErrorIsBoundedAndSanitized(t *testing.T) {
	t.Parallel()

	err := errors.New("open rtmp://user:pass@live.example/in/stream-key?token=secret#frag\n" +
		"authorization=Bearer-secret token=private " + strings.Repeat("x", 900))
	detail := sanitizeRuntimeError(err)

	if len(detail) > maxRuntimeErrorBytes {
		t.Fatalf("detail has %d bytes, limit is %d", len(detail), maxRuntimeErrorBytes)
	}
	for _, secret := range []string{"user", "pass", "stream-key", "secret", "private", "\n"} {
		if strings.Contains(detail, secret) {
			t.Fatalf("detail exposes %q: %q", secret, detail)
		}
	}
	if !strings.Contains(detail, "rtmp://live.example") {
		t.Fatalf("detail lost the safe endpoint label: %q", detail)
	}
}

func TestRuntimeErrorRedactsLocalPathsAndFormatControls(t *testing.T) {
	t.Parallel()

	detail := sanitizeRuntimeError(errors.New(
		"open /Users/alice/private.wav: denied; read C:\\Users\\Alice\\secret.wav: denied; " +
			`open \\server\private\voice.wav: denied` + "\x1b[31m\u202Espoof\u2066",
	))

	for _, secret := range []string{"/Users", "alice", "Alice", "private.wav", "secret.wav", `\\server`,
		"\x1b", "\u202E", "\u2066"} {
		if strings.Contains(detail, secret) {
			t.Fatalf("detail exposes %q: %q", secret, detail)
		}
	}
	if count := strings.Count(detail, "[local-path]"); count != 3 {
		t.Fatalf("redacted path count = %d, want 3: %q", count, detail)
	}
}

func TestRuntimeErrorRedactsBearerAndSourcePathsWithSpaces(t *testing.T) {
	t.Parallel()

	path := "/Users/alice/My Secret, take (final):1.wav"
	err := fmt.Errorf("Authorization: Bearer sk-live-secret: %w", &os.PathError{
		Op: "open", Path: path, Err: errors.New("permission denied"),
	})
	detail := sanitizeRuntimeError(err, "file://"+path+"?loop=true")
	for _, secret := range []string{"sk-live-secret", "alice", "My Secret", "final", ":1.wav"} {
		if strings.Contains(detail, secret) {
			t.Fatalf("detail exposes %q: %q", secret, detail)
		}
	}
	if !strings.Contains(detail, "Authorization=[redacted]") || !strings.Contains(detail, "[local-path]") {
		t.Fatalf("detail lost safe markers: %q", detail)
	}
}

func TestRuntimeErrorRedactsEveryJoinedPathBranch(t *testing.T) {
	t.Parallel()

	first := "/Users/alice/First Folder/voice, take (one).wav"
	second := `/Users/bob/Second "Quoted" Folder/final:2.wav`
	err := errors.Join(
		fmt.Errorf("first branch: %w", &os.PathError{
			Op: "open", Path: first, Err: errors.New("permission denied"),
		}),
		fmt.Errorf("nested branch printed as %q: %w", second, fmt.Errorf("read: %w", &os.PathError{
			Op: "open", Path: second, Err: errors.New("permission denied"),
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
	if count := strings.Count(detail, "[local-path]"); count < 2 {
		t.Fatalf("joined error redacted %d paths, want at least two: %q", count, detail)
	}
}

func TestRuntimeErrorSanitizesNativeHelperStderr(t *testing.T) {
	t.Parallel()

	detail := sanitizeRuntimeError(errors.New(
		"native stt helper: broken pipe; stderr: token=private " +
			`open "/Users/alice/Secret Model/model.bin" ` +
			"https://user:pass@engine.example/private?secret=value\x1b[31m\u202Espoof",
	))

	for _, secret := range []string{
		"private", "value", "user", "pass", "/Users", "alice", "Secret Model",
		"\x1b", "\u202E",
	} {
		if strings.Contains(detail, secret) {
			t.Fatalf("helper stderr exposes %q: %q", secret, detail)
		}
	}
	for _, safe := range []string{
		"stderr:", "token=[redacted]", `"[local-path]"`, "https://engine.example",
	} {
		if !strings.Contains(detail, safe) {
			t.Fatalf("helper stderr lost %q: %q", safe, detail)
		}
	}
}

func TestStatusEventOwnsItsSessionSnapshot(t *testing.T) {
	t.Parallel()

	store := NewStore()
	candidate := testSession("status-owned")
	candidate.Flags = map[string]string{"subs": "vtt"}
	mustCreateSession(t, store, candidate)
	stored := mustGetSession(t, store, candidate.Slug)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	events := store.Subscribe(ctx)
	if !store.bindRuntime(stored.Slug, stored.revision, 1) {
		t.Fatal("bindRuntime rejected current definition")
	}
	if !store.setRuntime(stored.Slug, stored.revision, 1, StateFailed, errors.New("boom")) {
		t.Fatal("setRuntime rejected current generation")
	}

	event := receiveStatusEvent(t, events)
	event.Session.Flags["subs"] = "off"
	after := mustGetSession(t, store, candidate.Slug)
	if after.Flags["subs"] != "vtt" || after.Runtime().State != StateFailed {
		t.Fatalf("event aliased stored session: flags=%v runtime=%+v", after.Flags, after.Runtime())
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
	if !store.bindRuntime(first.Slug, first.revision, 1) {
		t.Fatal("first generation did not bind")
	}

	updated, err := store.UpdateLangs(first.Slug, []core.Lang{"fr"}, nil)
	if err != nil {
		t.Fatalf("UpdateLangs returned error: %v", err)
	}
	if store.setRuntime(first.Slug, first.revision, 1, StateRunning, nil) {
		t.Fatal("old revision overwrote its replacement")
	}
	if !store.bindRuntime(updated.Slug, updated.revision, 2) {
		t.Fatal("replacement generation did not bind")
	}
	if store.setRuntime(updated.Slug, updated.revision, 1, StateRunning, nil) {
		t.Fatal("old generation overwrote its replacement")
	}

	if err := store.Delete(updated.Slug); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	if store.setRuntime(updated.Slug, updated.revision, 2, StateRunning, nil) {
		t.Fatal("deleted lane recreated runtime state")
	}
}
