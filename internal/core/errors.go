package core

import "errors"

var (
	// ErrTransient marks provider failures safe to retry; decorators and
	// fallback logic branch on it with errors.Is.
	ErrTransient = errors.New("transient provider failure")
	// ErrNotReady marks media output still being assembled after session
	// creation; clients may retry it without inspecting message text.
	ErrNotReady = errors.New("media output not ready")
	// ErrVoicePending marks a take that cannot be voiced yet because the
	// speaker's cloned voice is still being created in the background. It
	// is neither a failure nor retryable: the lane ships the caption and
	// voices the next take once the clone lands.
	ErrVoicePending = errors.New("voice clone pending")
)
