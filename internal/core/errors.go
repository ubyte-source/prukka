package core

import "errors"

// ErrNotReady marks media output still being assembled after session
// creation; clients may retry it without inspecting message text.
var ErrNotReady = errors.New("media output not ready")
