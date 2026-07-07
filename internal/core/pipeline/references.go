package pipeline

import (
	"sync"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
)

// minReferenceDuration is the shortest clip adopted as a cloning
// reference; shorter clips clone unstably.
const minReferenceDuration = 2 * time.Second

// References holds one cloning reference per speaker — the first
// long-enough utterance, reused so the timbre stays stable.
type References struct {
	refs map[int][]int16
	mu   sync.Mutex
}

// NewReferences starts with no captured speakers.
func NewReferences() *References {
	return &References{refs: map[int][]int16{}}
}

// Capture returns a speaker's reference, adopting the first long-enough
// clip; nil until one arrives.
func (r *References) Capture(speaker int, audio core.PCM) []int16 {
	r.mu.Lock()
	defer r.mu.Unlock()

	if ref, held := r.refs[speaker]; held {
		return ref
	}

	if clipDuration(audio) < minReferenceDuration {
		return nil
	}

	// Copy: the pipeline pools and reuses utterance buffers, and the
	// reference must outlive this call.
	ref := append([]int16(nil), audio.Data...)
	r.refs[speaker] = ref

	return ref
}

// clipDuration is the wall-clock length of a PCM clip.
func clipDuration(p core.PCM) time.Duration {
	if p.Rate <= 0 || p.Ch <= 0 {
		return 0
	}

	frames := len(p.Data) / p.Ch

	return time.Duration(frames) * time.Second / time.Duration(p.Rate)
}
