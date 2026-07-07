package local_test

import (
	"testing"

	"github.com/ubyte-source/prukka/internal/providers/local"
)

// TestVoiceBank: distinct ids and both registers, the same contract the
// hosted bank satisfies.
func TestVoiceBank(t *testing.T) {
	t.Parallel()

	bank := local.VoiceBank()
	if len(bank) < 2 {
		t.Fatalf("bank has %d voices, want several", len(bank))
	}

	seen := make(map[string]bool, len(bank))

	var male, female int

	for _, v := range bank {
		if v.ID == "" {
			t.Fatal("a bank voice has no id")
		}

		if seen[v.ID] {
			t.Fatalf("duplicate voice id %q", v.ID)
		}

		seen[v.ID] = true

		switch v.Gender {
		case "m":
			male++
		case "f":
			female++
		}
	}

	if male == 0 || female == 0 {
		t.Fatalf("bank is single-register: %d male, %d female", male, female)
	}
}
