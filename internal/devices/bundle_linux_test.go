//go:build linux && bundleddrivers

package devices

import "testing"

// TestPayloadsExposeTheModuleSources: the embedded source archive is
// non-empty; CI asserts the assets before building with the tag.
func TestPayloadsExposeTheModuleSources(t *testing.T) {
	t.Parallel()

	pay, err := payloads()
	if err != nil {
		t.Fatalf("payloads: %v", err)
	}

	if len(pay[payloadSrc]) == 0 {
		t.Fatal("module source payload is empty")
	}
}
