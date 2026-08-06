//go:build !darwin

package discover

import "testing"

func TestOutputStampIsUnwatchableHere(t *testing.T) {
	t.Parallel()

	if stamp, ok := OutputStamp("any"); ok || stamp != "" {
		t.Fatalf("stamp = (%q, %v), want unwatchable", stamp, ok)
	}
	if index, ok := OutputIndex("any"); ok || index != 0 {
		t.Fatalf("index = (%d, %v), want unresolvable", index, ok)
	}
}
