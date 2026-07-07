package local

import "github.com/ubyte-source/prukka/internal/core"

// VoiceBank returns the OpenAI-standard preset voices ordered deep →
// bright; every OpenAI-compatible speech server ships these names.
func VoiceBank() []core.Voice {
	return []core.Voice{
		{ID: "onyx", Gender: "m"},
		{ID: "echo", Gender: "m"},
		{ID: "fable", Gender: "m"},
		{ID: "alloy", Gender: "f"},
		{ID: "nova", Gender: "f"},
		{ID: "shimmer", Gender: "f"},
	}
}
