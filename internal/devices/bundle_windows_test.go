//go:build windows && bundleddrivers

package devices

import "testing"

// TestPayloadsExposeTheWebcamArchive: the embedded camera archive is
// non-empty; CI asserts the assets before building with the tag.
func TestPayloadsExposeTheWebcamArchive(t *testing.T) {
	t.Parallel()

	pay, err := payloads()
	if err != nil {
		t.Fatalf("payloads: %v", err)
	}

	if len(pay[string(Webcam)]) == 0 {
		t.Fatal("webcam payload is empty")
	}
}
