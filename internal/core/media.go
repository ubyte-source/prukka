package core

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// MaxSessionDelay bounds live buffering and teardown latency.
const MaxSessionDelay = 60 * time.Second

// BedLevel parses an original-audio bed level: "off" mutes the bed
// entirely (calls), a decibel value ducks it under the voice (broadcast).
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
