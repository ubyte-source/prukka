//go:build windows

package discover

import (
	"strings"
	"testing"
)

func TestDevicesAlwaysOffersTheDefaultOutput(t *testing.T) {
	t.Parallel()

	found := false

	for _, d := range Devices(t.Context(), "") {
		if d.URL == "device://audio/default" && d.Kind == AudioOut {
			found = true
		}

		if !strings.HasPrefix(d.URL, "device://") {
			t.Fatalf("malformed device URL: %+v", d)
		}
	}

	if !found {
		t.Fatal("the default output device is missing")
	}
}
