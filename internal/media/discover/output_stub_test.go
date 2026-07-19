//go:build !darwin

package discover

import "testing"

// TestOutputStampIsUnwatchableHere: without a platform fingerprint, device
// outputs must feed unwatched instead of guessing.
func TestOutputStampIsUnwatchableHere(t *testing.T) {
	t.Parallel()

	if stamp, ok := OutputStamp("any"); ok || stamp != "" {
		t.Fatalf("stamp = (%q, %v), want unwatchable", stamp, ok)
	}
	if index, ok := OutputIndex("any"); ok || index != 0 {
		t.Fatalf("index = (%d, %v), want unresolvable", index, ok)
	}
}
