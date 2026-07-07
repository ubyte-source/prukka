package pipeline

import (
	"math"
	"sync"

	"github.com/ubyte-source/prukka/internal/core"
)

// speakerTolerance (octaves) separates speakers without splitting one
// speaker's natural range.
const speakerTolerance = 0.17

// centerSmoothing is the EMA weight of a new observation on its cluster.
const centerSmoothing = 0.2

// maxSpeakers bounds the clusters; a busier stage reuses voices.
const maxSpeakers = 6

// Pitch range mapped onto the voice bank (Hz); speakers outside clamp to
// the ends.
const (
	bankLowHz  = 85
	bankHighHz = 255
)

// Speakers clusters a stream's people by pitch and register-matches each
// to a bank voice; ids are dense indices. Concurrency-safe.
type Speakers struct {
	slots    []bool // bank slots taken, sized to the bank on first use
	centers  []float64
	voices   [maxSpeakers]core.Voice
	mu       sync.Mutex
	assigned [maxSpeakers]bool
}

// NewSpeakers starts with no known speakers.
func NewSpeakers() *Speakers {
	return &Speakers{}
}

// Classify returns the stable speaker index and its register-matched
// voice; the index works even with an empty bank.
func (s *Speakers) Classify(f0 float64, bank []core.Voice) (int, core.Voice) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := s.identify(f0)

	if len(bank) == 0 {
		return id, core.Voice{}
	}

	if s.assigned[id] {
		return id, s.voices[id]
	}

	if len(s.slots) < len(bank) {
		s.slots = append(s.slots, make([]bool, len(bank)-len(s.slots))...)
	}

	slot := s.pickSlot(f0, len(bank))
	s.slots[slot] = true
	s.assigned[id] = true
	s.voices[id] = bank[slot]

	return id, bank[slot]
}

// CenterF0 returns a speaker's EMA-smoothed fundamental in Hz, or 0 when
// unknown or unvoiced.
func (s *Speakers) CenterF0(id int) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	if id < 0 || id >= len(s.centers) {
		return 0
	}

	return s.centers[id]
}

// identify matches f0 to the nearest cluster within tolerance, updates its
// center, or opens a new cluster (bounded by maxSpeakers).
func (s *Speakers) identify(f0 float64) int {
	if f0 <= 0 {
		// Unvoiced: stick with the newest speaker, or the first.
		if len(s.centers) == 0 {
			s.centers = append(s.centers, 0)
		}

		return len(s.centers) - 1
	}

	best, bestDist := -1, math.MaxFloat64

	for i, center := range s.centers {
		if center <= 0 {
			continue
		}

		if dist := math.Abs(math.Log2(f0 / center)); dist < bestDist {
			best, bestDist = i, dist
		}
	}

	if best >= 0 && bestDist <= speakerTolerance {
		s.centers[best] += (f0 - s.centers[best]) * centerSmoothing

		return best
	}

	if len(s.centers) >= maxSpeakers {
		if best < 0 {
			return 0
		}

		return best // nearest despite distance: the stage is full
	}

	s.centers = append(s.centers, f0)

	return len(s.centers) - 1
}

// pickSlot takes the nearest unowned bank slot for a pitch so concurrent
// speakers stay distinguishable; runs with s.mu held.
func (s *Speakers) pickSlot(f0 float64, size int) int {
	want := pitchSlot(f0, size)

	if !s.slots[want] {
		return want
	}

	for offset := 1; offset < size; offset++ {
		if want-offset >= 0 && !s.slots[want-offset] {
			return want - offset
		}

		if want+offset < size && !s.slots[want+offset] {
			return want + offset
		}
	}

	return want
}

// pitchSlot buckets a pitch into a bank index.
func pitchSlot(f0 float64, size int) int {
	if f0 <= 0 {
		return size / 2
	}

	pos := (f0 - bankLowHz) / (bankHighHz - bankLowHz)
	slot := int(pos * float64(size))

	if slot < 0 {
		return 0
	}

	if slot >= size {
		return size - 1
	}

	return slot
}
