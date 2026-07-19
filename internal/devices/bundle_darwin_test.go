//go:build darwin && bundleddrivers

package devices

import "testing"

// TestPayloadsExposeTheDriverArchives: every embedded archive is
// non-empty; CI asserts the assets before building with the tag.
func TestPayloadsExposeTheDriverArchives(t *testing.T) {
	t.Parallel()

	pay, err := payloads()
	if err != nil {
		t.Fatalf("payloads: %v", err)
	}

	for _, kind := range kinds() {
		if len(pay[string(kind)]) == 0 {
			t.Fatalf("payload %s is empty", kind)
		}
	}
}
