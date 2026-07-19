package nativewire

import (
	"errors"
	"fmt"
	"time"
)

// The bounds every tuning duration must respect; Validate names them in its
// diagnostics so a rejected flag reports the accepted range.
const (
	sttTuningMin = 20 * time.Millisecond
	sttTuningMax = 30 * time.Second
)

// STTTuning is the endpointing half of the STT contract: the daemon serializes
// it onto the helper's flags and the helper parses and validates the same
// shape, so the two sides of the process boundary cannot drift. Zero values on
// the daemon side leave the helper's broadcast-safe defaults in force.
type STTTuning struct {
	SilenceHang time.Duration
	MaxWindow   time.Duration
	MinSpeech   time.Duration
}

// Validate rejects a tuning the endpointer could not honor: every duration
// must sit inside the accepted range, and a silence endpoint must remain
// reachable before the window ceiling, so MinSpeech must not exceed MaxWindow.
func (t STTTuning) Validate() error {
	values := [...]struct {
		name  string
		value time.Duration
	}{
		{name: "silence-hang", value: t.SilenceHang},
		{name: "max-window", value: t.MaxWindow},
		{name: "min-speech", value: t.MinSpeech},
	}
	for _, item := range values {
		if item.value < sttTuningMin || item.value > sttTuningMax {
			return fmt.Errorf(
				"--%s must be between %s and %s, got %s",
				item.name, sttTuningMin, sttTuningMax, item.value,
			)
		}
	}
	if t.MinSpeech > t.MaxWindow {
		return errors.New("--min-speech must not exceed --max-window")
	}

	return nil
}
