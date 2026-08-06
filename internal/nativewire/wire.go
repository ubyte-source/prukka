// Package nativewire holds the contract spoken across the daemon/helper
// process boundary: the newline-delimited JSON exchanged between the daemon
// (internal/providers/native) and the prukka engine subcommands
// (internal/speechengine), plus the subcommand verbs, flag names and
// bundle-root environment variable the spawn sets. Both ends import it from
// here, so neither half can drift.
package nativewire

// ProtocolVersion is the STT stdio handshake version; the helper refuses a
// mismatch.
const ProtocolVersion = 2

// EngineRootEnv points a self-executed helper at the downloaded runtime
// bundle, which os.Executable cannot locate because the helper process is the
// daemon binary re-execed.
const EngineRootEnv = "PRUKKA_ENGINE_ROOT"

// The helper subcommand verbs.
const (
	SubSTT = "stt"
	SubMT  = "mt"
	SubTTS = "tts"
)

// FlagPrefix is how a flag name is spelled on argv; the helper binds the bare
// name.
const FlagPrefix = "--"

// The helper flag names, declared once for both sides of the argv contract.
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

// Ready is the STT helper's first line: the model has loaded and transcripts
// may follow.
type Ready struct {
	Ready bool `json:"ready"`
}

// Transcript is one stdout line of the STT helper; Partial and Text are
// pointers because presence, not emptiness, carries the meaning — an empty
// final still closes the agreement epoch and must stay on the wire.
type Transcript struct {
	Partial     *string  `json:"partial,omitempty"`
	Text        *string  `json:"text,omitempty"`
	InferenceMS *float64 `json:"inference_ms,omitempty"`
	Language    string   `json:"language,omitempty"`
	EndSamples  int64    `json:"end_samples"`
	Final       bool     `json:"final,omitempty"`
}

// TextLine is the single-field text frame shared by the MT request, the MT
// reply and the TTS synthesis request.
type TextLine struct {
	Text string `json:"text"`
}

// AudioReply is one line of TTS helper output: either a base64 PCM chunk or
// the turn boundary, never both.
type AudioReply struct {
	Audio string `json:"audio,omitempty"`
	Done  bool   `json:"done,omitempty"`
}
