package openrouter_test

import (
	"testing"

	"github.com/ubyte-source/prukka/internal/providers/openrouter"
)

// TestVoiceBankShape: distinct ids, declared genders, both registers
// represented.
func TestVoiceBankShape(t *testing.T) {
	t.Parallel()

	bank := openrouter.VoiceBank()
	if len(bank) == 0 {
		t.Fatal("empty voice bank")
	}

	seen := map[string]bool{}
	genders := map[string]int{}

	for _, v := range bank {
		if v.ID == "" {
			t.Fatalf("voice without id: %+v", v)
		}

		if seen[v.ID] {
			t.Fatalf("duplicate voice id %q", v.ID)
		}

		seen[v.ID] = true

		if v.Gender != "m" && v.Gender != "f" {
			t.Fatalf("voice %q has gender %q, want m or f", v.ID, v.Gender)
		}

		genders[v.Gender]++
	}

	if genders["m"] == 0 || genders["f"] == 0 {
		t.Fatalf("bank lacks a register: %v", genders)
	}
}
