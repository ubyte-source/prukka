package control

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/session"
)

// demoSession is a valid session for wire tests.
func demoSession() *session.Session {
	return &session.Session{
		Slug:             "demo",
		Profile:          session.ProfileBroadcast,
		Source:           core.SourceSpec{URL: "rtmp://x/in/demo"},
		Langs:            []core.Lang{"it", "en"},
		BudgetEURPerHour: 3,
		Delay:            8 * time.Second,
	}
}

func TestToWireProjectsFields(t *testing.T) {
	t.Parallel()

	w := toWire(demoSession())

	if w.Slug != "demo" || w.Profile != "broadcast" || w.SourceURL != "rtmp://x/in/demo" {
		t.Fatalf("wire identity = %+v", w)
	}

	if len(w.Langs) != 2 || w.Langs[1] != "en" || w.DelaySeconds != 8 || w.BudgetEURPerHour != 3 {
		t.Fatalf("wire fields = %+v", w)
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
	if err := writeSnapshot(&snap, store); err != nil {
		t.Fatalf("writeSnapshot returned error: %v", err)
	}

	if !strings.HasPrefix(snap.String(), "event: snapshot\n") || !strings.Contains(snap.String(), `"slug":"demo"`) {
		t.Fatalf("snapshot event wrong:\n%s", snap.String())
	}

	var ev bytes.Buffer

	evt := &session.Event{Type: session.EventCreated, Session: *demoSession()}
	if err := writeEvent(&ev, evt); err != nil {
		t.Fatalf("writeEvent returned error: %v", err)
	}

	if !strings.Contains(ev.String(), `"type":"created"`) || !strings.Contains(ev.String(), `"slug":"demo"`) {
		t.Fatalf("session event wrong:\n%s", ev.String())
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
