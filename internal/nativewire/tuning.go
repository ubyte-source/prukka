package nativewire

import (
	"errors"
	"fmt"
	"time"
)

// The bounds every tuning duration must respect.
const (
	sttTuningMin = 20 * time.Millisecond
	sttTuningMax = 30 * time.Second
)

// STTTuning is the endpointing half of the STT contract; the zero value leaves
// the helper's broadcast-safe defaults in force.
type STTTuning struct {
	SilenceHang time.Duration
	MaxWindow   time.Duration
	MinSpeech   time.Duration
}

// Validate rejects a tuning the endpointer could not honor.
func (t STTTuning) Validate() error {
	values := [...]struct {
		name  string
		value time.Duration
	}{
		{name: FlagSilenceHang, value: t.SilenceHang},
		{name: FlagMaxWindow, value: t.MaxWindow},
		{name: FlagMinSpeech, value: t.MinSpeech},
	}
	for _, item := range values {
		if item.value < sttTuningMin || item.value > sttTuningMax {
			return fmt.Errorf(
				"%s%s must be between %s and %s, got %s",
				FlagPrefix, item.name, sttTuningMin, sttTuningMax, item.value,
			)
		}
	}
	if t.MinSpeech > t.MaxWindow {
		return errors.New(
			FlagPrefix + FlagMinSpeech + " must not exceed " + FlagPrefix + FlagMaxWindow,
		)
	}

	return nil
}
