package core

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// SampleRate is the canonical PCM sample rate in hertz: 16 kHz mono, the rate
// the speech engine expects and every media stage assumes.
const SampleRate = 16000

// PCM is a chunk of interleaved 16-bit samples; buffers are pooled, so
// consumers must not retain Data without copying it.
type PCM struct {
	Data []int16
	Rate int           // Hz; SampleRate is the internal reference
	Ch   int           // channel count; 1 is the internal reference
	PTS  time.Duration // source-clock timestamp of the first sample
}

// Voice selects a TTS voice; an empty Lang means the voice is multilingual.
type Voice struct {
	ID   string
	Lang Lang
}

// Supports reports whether the voice may synthesize the target language.
func (v Voice) Supports(target Lang) bool {
	if v.Lang == LangAuto {
		return true
	}

	return SameLang(v.Lang, target)
}

// TranslatedSegment is one caption and its source-clock schedule.
type TranslatedSegment struct {
	Slug       string
	Text       string
	ScheduleAt time.Duration // source PTS plus the per-session delay D
	Duration   time.Duration // source utterance duration
}

// MaxSessionDelay bounds live buffering and teardown latency.
const MaxSessionDelay = 60 * time.Second

// BedLevel parses an original-audio bed level: "off" mutes the bed entirely,
// a decibel value ducks it under the voice.
func BedLevel(raw string) (float64, error) {
	trimmed := strings.TrimSpace(raw)
	if strings.EqualFold(trimmed, "off") {
		return math.Inf(-1), nil
	}

	value := strings.TrimSuffix(trimmed, "dB")
	level, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(level) || math.IsInf(level, 0) || level < -60 || level > 0 {
		return 0, fmt.Errorf("bed level %q: expected off, or -60dB to 0dB", raw)
	}

	return level, nil
}

// ErrNotReady marks media output still being assembled after session creation.
var ErrNotReady = errors.New("media output not ready")
