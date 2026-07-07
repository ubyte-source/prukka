package openrouter

import "github.com/ubyte-source/prukka/internal/core"

// VoiceBank returns gpt-audio's preset voices ordered deep → bright; the
// platform offers no cloning.
func VoiceBank() []core.Voice {
	return []core.Voice{
		{ID: "onyx", Gender: "m"},
		{ID: "ash", Gender: "m"},
		{ID: "echo", Gender: "m"},
		{ID: "alloy", Gender: "f"},
		{ID: "coral", Gender: "f"},
		{ID: "shimmer", Gender: "f"},
	}
}
