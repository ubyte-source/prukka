//go:build windows

package wasapi

import "testing"

// TestEndpointsEnumeratesRenderDevices: enumeration succeeds on any host
// (a bare CI runner has zero endpoints) and every entry is well-formed.
func TestEndpointsEnumeratesRenderDevices(t *testing.T) {
	t.Parallel()

	endpoints, err := Endpoints()
	if err != nil {
		t.Fatalf("Endpoints returned error: %v", err)
	}

	for _, e := range endpoints {
		if e.ID == "" {
			t.Fatalf("endpoint without an ID: %+v", e)
		}
	}
}
