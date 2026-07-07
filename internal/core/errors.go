package core

import "errors"

// ErrTransient marks provider failures safe to retry; decorators and
// fallback logic branch on it with errors.Is.
var ErrTransient = errors.New("transient provider failure")
