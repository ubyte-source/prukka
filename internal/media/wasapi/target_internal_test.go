package wasapi

import (
	"strings"
	"testing"
)

func TestEndpointIDStripsTheLabel(t *testing.T) {
	t.Parallel()

	id, err := endpointID("device://audio/3?label=Speakers")
	if err != nil {
		t.Fatalf("endpointID: %v", err)
	}
	if id != "3" {
		t.Fatalf("endpointID = %q, want the bare endpoint id %q", id, "3")
	}
}

func TestEndpointIDAcceptsWhatDiscoveryPublishes(t *testing.T) {
	t.Parallel()

	for target, want := range map[string]string{
		"device://audio/default":                     "default",
		"device://audio/{0.0.0.00000000}.{endpoint}": "{0.0.0.00000000}.{endpoint}",
	} {
		id, err := endpointID(target)
		if err != nil || id != want {
			t.Errorf("endpointID(%q) = (%q, %v), want %q", target, id, err, want)
		}
	}
}

func TestOpenRejectsMalformedTargets(t *testing.T) {
	t.Parallel()

	for _, target := range []string{"", "bogus", "device://video/0", "device://audio/"} {
		w, err := Open(target)
		if err == nil || w != nil {
			t.Fatalf("Open(%q) = (%v, %v), want a validation error", target, w, err)
		}

		if !strings.Contains(err.Error(), "device://audio/") {
			t.Fatalf("Open(%q) error %q does not name the expected shape", target, err)
		}
	}
}

func TestEndpointIDErrorsNameNoDeviceIdentity(t *testing.T) {
	t.Parallel()

	for _, target := range []string{"device://video/Blue Yeti USB", "/Users/alice/speakers"} {
		_, err := endpointID(target)
		if err == nil {
			t.Fatalf("endpointID(%q) accepted a non-audio target", target)
		}
		for _, secret := range []string{"Blue Yeti", "alice", "speakers"} {
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("endpointID(%q) error exposes %q: %v", target, secret, err)
			}
		}
	}
}
