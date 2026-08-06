package nativewire_test

import (
	"encoding/json"
	"fmt"
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

// ExampleTranscript round-trips the STT line whose meaning lives in field
// presence: Text and Partial are pointers because an empty final must still
// travel — it closes the endpointed segment — while EndSamples is always on
// the wire.
func ExampleTranscript() {
	empty := ""
	line, err := json.Marshal(nativewire.Transcript{Text: &empty, EndSamples: 32000, Final: true})
	fmt.Println(string(line), err)

	var decoded nativewire.Transcript
	err = json.Unmarshal(line, &decoded)
	// Presence, not emptiness: the final text arrived, no partial did.
	fmt.Println(decoded.Text != nil, decoded.Partial != nil, err)
	// Output:
	// {"text":"","end_samples":32000,"final":true} <nil>
	// true false <nil>
}

// ProtocolVersion is the one number both ends compile against; guarding its
// value makes an accidental bump a visible, reviewed change.
func TestProtocolVersionIsPinned(t *testing.T) {
	t.Parallel()

	if nativewire.ProtocolVersion != 2 {
		t.Fatalf("ProtocolVersion = %d, want 2", nativewire.ProtocolVersion)
	}
}

// The launch contract — the env var a spawn sets and the verbs it invokes —
// is matched by shipped helper binaries, so a rename here must be a visible,
// reviewed change, not a refactor side effect.
func TestLaunchContractIsPinned(t *testing.T) {
	t.Parallel()

	if nativewire.EngineRootEnv != "PRUKKA_ENGINE_ROOT" {
		t.Fatalf("EngineRootEnv = %q, want PRUKKA_ENGINE_ROOT", nativewire.EngineRootEnv)
	}
	if nativewire.SubSTT != "stt" || nativewire.SubMT != "mt" || nativewire.SubTTS != "tts" {
		t.Fatalf("verbs = %q/%q/%q, want stt/mt/tts",
			nativewire.SubSTT, nativewire.SubMT, nativewire.SubTTS)
	}
}
