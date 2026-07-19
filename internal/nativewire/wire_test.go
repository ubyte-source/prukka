package nativewire_test

import (
	"encoding/json"
	"testing"

	"github.com/ubyte-source/prukka/internal/nativewire"
)

// The wire shape is the contract between the daemon and the shipped helper
// binaries: these round trips pin the exact JSON so a field rename cannot pass
// review unnoticed.

func TestReadyMarshalsTheHandshakeLine(t *testing.T) {
	t.Parallel()

	got, err := json.Marshal(nativewire.Ready{Ready: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(got) != `{"ready":true}` {
		t.Fatalf("ready line = %s, want {\"ready\":true}", got)
	}
}

func TestTextLineIsTheSingleTextField(t *testing.T) {
	t.Parallel()

	got, err := json.Marshal(nativewire.TextLine{Text: "ciao"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(got) != `{"text":"ciao"}` {
		t.Fatalf("text line = %s, want {\"text\":\"ciao\"}", got)
	}

	var back nativewire.TextLine
	if err := json.Unmarshal(got, &back); err != nil || back.Text != "ciao" {
		t.Fatalf("round trip = %+v, %v", back, err)
	}
}

// An audio chunk and the turn boundary are mutually exclusive lines: each
// omits the other field so a decoder reads whichever is present.
func TestAudioReplyChunkAndBoundaryAreDisjoint(t *testing.T) {
	t.Parallel()

	chunk, err := json.Marshal(nativewire.AudioReply{Audio: "AAAA"})
	if err != nil {
		t.Fatalf("marshal chunk: %v", err)
	}
	if string(chunk) != `{"audio":"AAAA"}` {
		t.Fatalf("audio chunk = %s, want {\"audio\":\"AAAA\"}", chunk)
	}

	done, err := json.Marshal(nativewire.AudioReply{Done: true})
	if err != nil {
		t.Fatalf("marshal done: %v", err)
	}
	if string(done) != `{"done":true}` {
		t.Fatalf("boundary = %s, want {\"done\":true}", done)
	}
}

// ProtocolVersion is the one number both ends compile against; guarding its
// value makes an accidental bump a visible, reviewed change.
func TestProtocolVersionIsPinned(t *testing.T) {
	t.Parallel()

	if nativewire.ProtocolVersion != 2 {
		t.Fatalf("ProtocolVersion = %d, want 2", nativewire.ProtocolVersion)
	}
}
