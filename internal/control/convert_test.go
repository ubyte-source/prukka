package control

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/session"
	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
)

func TestSessionRoundTrip(t *testing.T) {
	t.Parallel()
	delay := 8.0

	in := &v1.Session{
		Slug:         "demo",
		Profile:      "broadcast",
		SourceUrl:    "rtmp://0.0.0.0:1935/in/demo",
		Langs:        []string{"it", "de-ch"},
		Flags:        map[string]string{"subs": "vtt"},
		DelaySeconds: &delay,
	}

	sess, err := sessionFromProto(in)
	if err != nil {
		t.Fatalf("sessionFromProto returned error: %v", err)
	}

	// Language tags are normalized through the registry at the boundary.
	if len(sess.Langs) != 2 || sess.Langs[1] != "de-CH" {
		t.Fatalf("langs = %v, want normalized [it de-CH]", sess.Langs)
	}

	if sess.Delay != 8*time.Second {
		t.Fatalf("delay = %v, want 8s", sess.Delay)
	}

	assertProtoRoundTrip(t, sess)
}

func TestPublicSourceLabelRedactsSecretsAndPaths(t *testing.T) {
	t.Parallel()

	cases := []struct {
		raw  string
		want string
	}{
		{raw: "rtmp://user:pa" + "ss@live.example/in/stream-key?token=se" + "cret#fragment", want: "rtmp://live.example"},
		{raw: "srt://relay.example:9000?passphrase=secret", want: "srt://relay.example:9000"},
		{raw: "file:///Users/alice/private.wav", want: "file://[local]"},
		{raw: "device://audio/private-device-id", want: "device://audio"},
		{raw: "not a url with token=secret", want: "[source]"},
	}

	for _, tc := range cases {
		if got := PublicSourceLabel(tc.raw); got != tc.want {
			t.Errorf("PublicSourceLabel(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestSessionToProtoSeparatesWriteOnlySourceFromPublicLabel(t *testing.T) {
	t.Parallel()

	wire := sessionToProto(&session.Session{Source: core.SourceSpec{
		URL: "rtmp://user:pa" + "ss@live.example/in/stream-key?token=se" + "cret#fragment",
	}})
	if got := wire.GetSourceUrl(); got != "" {
		t.Fatalf("source_url = %q, want empty write-only field", got)
	}
	if got := wire.GetSourceLabel(); got != "rtmp://live.example" {
		t.Fatalf("source_label = %q, want sanitized label", got)
	}
}

// assertProtoRoundTrip checks sessionToProto preserves the mapped fields.
func assertProtoRoundTrip(t *testing.T, sess *session.Session) {
	t.Helper()

	out := sessionToProto(sess)
	if out.GetSlug() != "demo" || out.GetDelaySeconds() != 8 {
		t.Fatalf("round-trip lost data: %+v", out)
	}

	if len(out.GetLangs()) != 2 || out.GetLangs()[1] != "de-CH" {
		t.Fatalf("round-trip langs = %v", out.GetLangs())
	}
}

func TestSessionFromProtoRejectsBadInput(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   *v1.Session
		name string
		code codes.Code
	}{
		{name: "nil session", in: nil, code: codes.InvalidArgument},
		{
			name: "bad profile",
			in:   &v1.Session{Slug: "x", Profile: "live", Langs: []string{"it"}},
			code: codes.InvalidArgument,
		},
		{
			name: "bad language",
			in:   &v1.Session{Slug: "x", Profile: "broadcast", Langs: []string{"ch"}},
			code: codes.InvalidArgument,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := sessionFromProto(tc.in)
			if status.Code(err) != tc.code {
				t.Fatalf("code = %v, want %v (err %v)", status.Code(err), tc.code, err)
			}
		})
	}
}

func TestSessionFromProtoRejectsRetiredVoiceMap(t *testing.T) {
	t.Parallel()

	in := &v1.Session{}
	if err := protojson.Unmarshal([]byte(`{
		"slug":"demo",
		"profile":"broadcast",
		"langs":["en"],
		"voiceMap":{"speaker-1":"alloy"}
	}`), in); err != nil {
		t.Fatalf("decode legacy session: %v", err)
	}
	_, err := sessionFromProto(in)
	if status.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), "session.voice_map") {
		t.Fatalf("sessionFromProto error = %v, want InvalidArgument naming session.voice_map", err)
	}
}

func TestStatusFromStore(t *testing.T) {
	t.Parallel()

	cases := []struct {
		err  error
		want codes.Code
	}{
		{err: session.ErrNotFound, want: codes.NotFound},
		{err: session.ErrExists, want: codes.AlreadyExists},
		{err: session.ErrCapacity, want: codes.ResourceExhausted},
		{err: session.ErrNoLanguages, want: codes.InvalidArgument},
		{err: errors.New("other"), want: codes.InvalidArgument},
	}

	for _, tc := range cases {
		if got := status.Code(statusFromStore(tc.err)); got != tc.want {
			t.Errorf("statusFromStore(%v) code = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestFailedRuntimeProjectionDoesNotLeakJoinedPathsToAPIOrSSE(t *testing.T) {
	t.Parallel()

	first := "/Users/alice/First Folder/input, one.wav"
	second := `/Users/bob/Second "Quoted" Folder/input:two.wav`
	failure := errors.Join(
		&os.PathError{Op: "open", Path: first, Err: errors.New("permission denied")},
		fmt.Errorf("second child: %w", &os.PathError{
			Op: "open", Path: second, Err: errors.New("permission denied"),
		}),
	)
	stored := projectFailedRuntime(t, failure)

	apiPayload, err := protojson.Marshal(sessionToProto(&stored))
	if err != nil {
		t.Fatalf("marshal API projection: %v", err)
	}
	var ssePayload bytes.Buffer
	if err := writeEvent(&ssePayload, &session.Event{
		Type: session.EventStatus, Session: stored,
	}, nil); err != nil {
		t.Fatalf("write SSE projection: %v", err)
	}

	assertRuntimeProjectionRedacted(t, "API", string(apiPayload))
	assertRuntimeProjectionRedacted(t, "SSE", ssePayload.String())
}

func projectFailedRuntime(t *testing.T, failure error) session.Session {
	t.Helper()

	store := session.NewStore()
	runtime := session.NewRuntime(
		store,
		func(context.Context, *session.Session, func()) error { return failure },
		nil,
		slog.New(slog.DiscardHandler),
	)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	t.Cleanup(func() { stopProjectionRuntime(t, cancel, done) })

	candidate := demoSession()
	candidate.Slug = "joined-paths"
	if err := store.Create(candidate); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	var stored session.Session
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		current, err := store.Get(candidate.Slug)
		if err == nil && current.Runtime().State == session.StateFailed {
			stored = current

			break
		}
		time.Sleep(time.Millisecond)
	}
	if stored.Runtime().State != session.StateFailed {
		t.Fatal("runtime failure was not projected")
	}

	return stored
}

func stopProjectionRuntime(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("runtime shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Error("runtime did not stop")
	}
}

func assertRuntimeProjectionRedacted(t *testing.T, surface, payload string) {
	t.Helper()

	for _, secret := range []string{
		"/Users", "alice", "bob", "First Folder", `Second \"Quoted\" Folder`, "input:two.wav",
	} {
		if strings.Contains(payload, secret) {
			t.Errorf("%s projection exposes %q: %s", surface, secret, payload)
		}
	}
	if !strings.Contains(payload, "[local-path]") {
		t.Errorf("%s projection omitted the redaction marker: %s", surface, payload)
	}
}

func TestParseLangs(t *testing.T) {
	t.Parallel()

	got, err := parseLangs([]string{"it", "en"})
	if err != nil {
		t.Fatalf("parseLangs returned error: %v", err)
	}

	if len(got) != 2 || got[0] != core.Lang("it") {
		t.Fatalf("parseLangs = %v, want [it en]", got)
	}

	if _, err := parseLangs([]string{"nope"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("parseLangs bad tag code = %v, want InvalidArgument", status.Code(err))
	}
	if _, err := parseLangs([]string{"auto"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("parseLangs auto target code = %v, want InvalidArgument", status.Code(err))
	}
}
