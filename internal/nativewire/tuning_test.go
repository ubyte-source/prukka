package nativewire_test

import (
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/nativewire"
)

// The tuning is the policy half of the STT contract: the daemon builds it, the
// helper validates it. One test here covers both ends of the process boundary.
func TestSTTTuningValidateAcceptsProfileTunings(t *testing.T) {
	t.Parallel()

	for name, tuning := range map[string]nativewire.STTTuning{
		"broadcast defaults": {
			SilenceHang: 300 * time.Millisecond, MaxWindow: 5 * time.Second,
			MinSpeech: 250 * time.Millisecond,
		},
		"range extremes": {
			SilenceHang: 20 * time.Millisecond, MaxWindow: 30 * time.Second,
			MinSpeech: 20 * time.Millisecond,
		},
	} {
		if err := tuning.Validate(); err != nil {
			t.Errorf("%s rejected: %v", name, err)
		}
	}
}

// ExampleSTTTuning_Validate shows the boundary rule beyond the per-field
// range: a minimum-speech longer than the window ceiling leaves no reachable
// silence endpoint, and the diagnostic names the helper flags that clash.
func ExampleSTTTuning_Validate() {
	tuning := nativewire.STTTuning{
		SilenceHang: 300 * time.Millisecond,
		MaxWindow:   5 * time.Second,
		MinSpeech:   250 * time.Millisecond,
	}
	emit(tuning.Validate())

	tuning.MinSpeech = 10 * time.Second
	emit(tuning.Validate())
	// Output:
	// <nil>
	// --min-speech must not exceed --max-window
}

func TestSTTTuningValidateRejectsDegenerateTunings(t *testing.T) {
	t.Parallel()

	valid := nativewire.STTTuning{
		SilenceHang: 300 * time.Millisecond, MaxWindow: 5 * time.Second,
		MinSpeech: 250 * time.Millisecond,
	}

	for name, mutate := range map[string]func(*nativewire.STTTuning){
		"zero silence-hang":    func(t *nativewire.STTTuning) { t.SilenceHang = 0 },
		"sub-range hang":       func(t *nativewire.STTTuning) { t.SilenceHang = time.Millisecond },
		"oversized window":     func(t *nativewire.STTTuning) { t.MaxWindow = 31 * time.Second },
		"zero min-speech":      func(t *nativewire.STTTuning) { t.MinSpeech = 0 },
		"unreachable endpoint": func(t *nativewire.STTTuning) { t.MinSpeech = 6 * time.Second },
	} {
		tuning := valid
		mutate(&tuning)
		if err := tuning.Validate(); err == nil {
			t.Errorf("%s accepted: %+v", name, tuning)
		}
	}
}
