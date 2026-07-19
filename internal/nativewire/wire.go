// Package nativewire is the one home of the newline-delimited JSON contract
// spoken between the daemon (internal/providers/native, the client that spawns
// the helpers) and the prukka engine subcommands (internal/speechengine, the helper
// that serves stt|mt|tts over stdio). Both ends import these types and the
// protocol version from here, so the wire shape and the handshake number can
// never drift between the two sides — previously the version lived as two
// separate constants that had to be kept equal by hand.
package nativewire

// ProtocolVersion is the STT stdio handshake version. The client passes it to
// the helper, which refuses a mismatch; bumping it here moves both ends at
// once. MT and TTS carry no version because their one-field request and
// audio-chunk reply have been stable since the first release.
const ProtocolVersion = 2

// Ready is the STT helper's first line: it signals that the model has loaded
// and transcript lines may follow. No other message sets Ready.
type Ready struct {
	Ready bool `json:"ready"`
}

// Transcript is one stdout line of the STT helper: a partial refines the live
// tail while the speaker is still talking, a final closes the endpointed
// segment. Partial and Text are pointers because presence, not emptiness,
// carries the meaning — an empty final still closes the agreement epoch and
// must stay on the wire — and EndSamples, the cumulative source-sample
// boundary, is required on every transcript.
type Transcript struct {
	Partial     *string  `json:"partial,omitempty"`
	Text        *string  `json:"text,omitempty"`
	InferenceMS *float64 `json:"inference_ms,omitempty"`
	Language    string   `json:"language,omitempty"`
	EndSamples  int64    `json:"end_samples"`
	Final       bool     `json:"final,omitempty"`
}

// TextLine is the single-field text frame shared by three legs: the MT
// request, the MT reply, and the TTS synthesis request. Encoders set Text;
// the strict server-side decoder rejects any line carrying other fields.
type TextLine struct {
	Text string `json:"text"`
}

// AudioReply is one line of TTS helper output: either a base64 PCM chunk
// (Audio set) or the turn boundary (Done set). The two never appear together,
// so both fields are omitempty and a decoded reply reads whichever is present.
type AudioReply struct {
	Audio string `json:"audio,omitempty"`
	Done  bool   `json:"done,omitempty"`
}
