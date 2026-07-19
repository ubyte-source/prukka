package control

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"encoding/json"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/session"
	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
)

// demoSession is a valid session for wire tests.
func demoSession() *session.Session {
	return &session.Session{
		Slug:    "demo",
		Profile: session.ProfileBroadcast,
		Source:  core.SourceSpec{URL: "rtmp://x/in/demo"},
		Langs:   []core.Lang{"it", "en"},
		Delay:   8 * time.Second,
	}
}

func TestToWireProjectsFields(t *testing.T) {
	t.Parallel()

	s := demoSession()
	s.Flags = map[string]string{"dub_langs": "it"}
	w := toWire(s, func(*session.Session) []core.Lang { return []core.Lang{"it"} })

	if w.Slug != "demo" || w.Profile != "broadcast" || w.SourceLabel != "rtmp://x" {
		t.Fatalf("wire identity = %+v", w)
	}

	if len(w.Langs) != 2 || w.Langs[1] != "en" || w.DelaySeconds != 8 {
		t.Fatalf("wire fields = %+v", w)
	}
	if w.Flags["dub_langs"] != "it" {
		t.Fatalf("wire flags = %v, want dubbing metadata", w.Flags)
	}
	if len(w.DubbedLangs) != 1 || w.DubbedLangs[0] != "it" {
		t.Fatalf("effective dubbed languages = %v, want [it]", w.DubbedLangs)
	}
}

func TestToWireDoesNotExposeSourceSecrets(t *testing.T) {
	t.Parallel()

	s := demoSession()
	s.Source.URL = "srt://user:pass@relay.example:9000/live?passphrase=secret#private"
	w := toWire(s, nil)
	if w.SourceLabel != "srt://relay.example:9000" {
		t.Fatalf("SSE source = %q, want sanitized label", w.SourceLabel)
	}

	store := session.NewStore()
	if err := store.Create(s); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	var payload bytes.Buffer
	if err := writeSnapshot(&payload, store, nil); err != nil {
		t.Fatalf("writeSnapshot returned error: %v", err)
	}
	if strings.Contains(payload.String(), "sourceUrl") ||
		strings.Contains(payload.String(), "passphrase") ||
		strings.Contains(payload.String(), "secret") ||
		!strings.Contains(payload.String(), `"sourceLabel":"srt://relay.example:9000"`) {
		t.Fatalf("SSE exposed source input or omitted its public label:\n%s", payload.String())
	}
}

func TestWriteSSEFramesEventAndData(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	if err := writeSSE(&buf, "session", map[string]string{"k": "v"}); err != nil {
		t.Fatalf("writeSSE returned error: %v", err)
	}

	out := buf.String()
	if !strings.HasPrefix(out, "event: session\ndata: ") || !strings.HasSuffix(out, "\n\n") {
		t.Fatalf("SSE framing wrong:\n%q", out)
	}

	if !strings.Contains(out, `{"k":"v"}`) {
		t.Fatalf("SSE data missing the JSON payload:\n%q", out)
	}
}

func TestWriteSnapshotAndEvent(t *testing.T) {
	t.Parallel()

	store := session.NewStore()
	if err := store.Create(demoSession()); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	var snap bytes.Buffer
	if err := writeSnapshot(&snap, store, nil); err != nil {
		t.Fatalf("writeSnapshot returned error: %v", err)
	}

	if !strings.HasPrefix(snap.String(), "event: snapshot\n") ||
		!strings.Contains(snap.String(), `"slug":"demo"`) ||
		!strings.Contains(snap.String(), `"status":"starting"`) {
		t.Fatalf("snapshot event wrong:\n%s", snap.String())
	}

	var ev bytes.Buffer

	evt := &session.Event{Type: session.EventCreated, Session: *demoSession()}
	if err := writeEvent(&ev, evt, nil); err != nil {
		t.Fatalf("writeEvent returned error: %v", err)
	}

	if !strings.Contains(ev.String(), `"type":"created"`) || !strings.Contains(ev.String(), `"slug":"demo"`) {
		t.Fatalf("session event wrong:\n%s", ev.String())
	}
}

func TestSnapshotRepairsAStaleClientView(t *testing.T) {
	t.Parallel()

	store := session.NewStore()
	var initial bytes.Buffer
	if err := writeSnapshot(&initial, store, nil); err != nil {
		t.Fatalf("initial snapshot: %v", err)
	}
	if err := store.Create(demoSession()); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var refresh bytes.Buffer
	if err := writeSnapshot(&refresh, store, nil); err != nil {
		t.Fatalf("refresh snapshot: %v", err)
	}
	if strings.Contains(initial.String(), `"slug":"demo"`) ||
		!strings.Contains(refresh.String(), `"slug":"demo"`) {
		t.Fatal("periodic snapshot did not repair the stale client view")
	}
}

// failWriter fails every write, exercising the error paths.
type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errWrite }

var errWrite = &writeError{}

type writeError struct{}

func (*writeError) Error() string { return "write failed" }

func TestWriteSSEPropagatesWriteErrors(t *testing.T) {
	t.Parallel()

	if err := writeSSE(failWriter{}, "session", "x"); err == nil {
		t.Fatal("writeSSE swallowed a write error")
	}
}

// TestWireSessionKeysMatchTheProtoContract pins the SSE projection to the
// gateway's protojson field names: the two projections are kept separate on
// purpose (presence semantics differ), so shared fields must never drift in
// name — the web client reads both.
func TestWireSessionKeysMatchTheProtoContract(t *testing.T) {
	t.Parallel()

	wire := wireSession{
		Flags: map[string]string{"source": "it"}, Slug: "s", Profile: "call",
		SourceLabel: "device://audio", Status: "running", Error: "x",
		Langs: []string{"en"}, DubbedLangs: []string{"en"}, DelaySeconds: 1,
	}
	delay := 1.0
	proto := &v1.Session{
		Slug: "s", Profile: "call", SourceLabel: "device://audio", Status: "running",
		Error: "x", Langs: []string{"en"}, EffectiveDubbedLangs: []string{"en"},
		DelaySeconds: &delay, Flags: map[string]string{"source": "it"},
	}
	rawWire, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	rawProto, err := protojson.Marshal(proto)
	if err != nil {
		t.Fatal(err)
	}

	protoKeys := jsonObjectKeys(t, rawProto)
	for key := range jsonObjectKeys(t, rawWire) {
		if _, shared := protoKeys[key]; !shared {
			t.Errorf("SSE key %q has no protojson counterpart on v1.Session — the projections drifted", key)
		}
	}
}

// jsonObjectKeys decodes a JSON object and returns its top-level key set.
func jsonObjectKeys(t *testing.T, raw []byte) map[string]any {
	t.Helper()

	keys := map[string]any{}
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("decode JSON object: %v", err)
	}

	return keys
}
