// Package nativewire is the one home of the contract spoken across the
// daemon/helper process boundary: the newline-delimited JSON exchanged between
// the daemon (internal/providers/native, the client that spawns the helpers)
// and the prukka engine subcommands (internal/speechengine, the helper that
// serves stt|mt|tts over stdio), plus the launch half of that contract — the
// subcommand verbs, the flag names and the bundle-root environment variable the
// spawn sets. Both ends import these from here, so neither the wire shape nor
// the launch convention can drift between the two sides — a contract defined
// twice would have to be kept equal by hand.
package nativewire

// ProtocolVersion is the STT stdio handshake version. The client passes it to
// the helper, which refuses a mismatch; bumping it here moves both ends at
// once. MT and TTS carry no version: their one-field request and audio-chunk
// reply leave nothing to negotiate.
const ProtocolVersion = 2

// EngineRootEnv points a self-executed helper at the downloaded runtime
// bundle. The single prukka binary re-execs itself for the stt|mt|tts helpers,
// so os.Executable resolves to the daemon rather than the bundle; the daemon
// sets this to the bundle root instead, and the helper honors it over its own
// executable directory.
const EngineRootEnv = "PRUKKA_ENGINE_ROOT"

// The helper subcommand verbs: the daemon spawns them, the binary's hidden
// commands serve them.
const (
	SubSTT = "stt"
	SubMT  = "mt"
	SubTTS = "tts"
)

// FlagPrefix is how a flag name is spelled on argv. The helper binds the bare
// name into its flag.FlagSet; the daemon composes the prefixed form from this
// constant and the names below, so the two sides cannot spell one differently.
const FlagPrefix = "--"

// The helper flag names, declared once for both sides of the argv half of the
// contract. ProtocolVersion cannot police this half, because it travels
// through it — and the tuning flags are omitted altogether when a lane sends
// the zero STTTuning, so a one-sided rename would surface only as a call
// lane's failed readiness handshake while every broadcast lane stayed green.
// Naming them here makes such a rename one edit the compiler propagates to the
// daemon, the helper and the protocol fixture.
const (
	FlagProtocol    = "protocol-version"
	FlagModel       = "model"
	FlagRate        = "rate"
	FlagThreads     = "threads"
	FlagLanguage    = "language"
	FlagSilenceHang = "silence-hang"
	FlagMaxWindow   = "max-window"
	FlagMinSpeech   = "min-speech"
	FlagFastDecode  = "fast-decode"
	FlagFrom        = "from"
	FlagTo          = "to"
)

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
