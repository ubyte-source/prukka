package devices

import (
	"path/filepath"
	"testing"
)

// TestMarkerRoundTrip: write, read, drop — and dropping the absent
// marker stays quiet.
func TestMarkerRoundTrip(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	if recordedSum(Microphone) != "" {
		t.Fatal("phantom marker before any install")
	}

	if Recorded() {
		t.Fatal("Recorded without any marker")
	}

	sum := payloadSum([]byte("payload"))
	if err := writeMarker(Microphone, sum); err != nil {
		t.Fatalf("writeMarker: %v", err)
	}

	if got := recordedSum(Microphone); got != sum {
		t.Fatalf("recordedSum = %q, want %q", got, sum)
	}

	if !Recorded() {
		t.Fatal("Recorded misses the written marker")
	}

	if err := dropMarker(Microphone); err != nil {
		t.Fatalf("dropMarker: %v", err)
	}

	if recordedSum(Microphone) != "" {
		t.Fatal("marker survived dropMarker")
	}

	if err := dropMarker(Microphone); err != nil {
		t.Fatalf("dropping an absent marker: %v", err)
	}
}

// TestMarkerStateClassifies: missing, installed, outdated — and an
// unbundled build trusts the marker.
func TestMarkerStateClassifies(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	data := []byte("payload")

	if got := markerState(Speaker, expectedMarker(data)); got != StateMissing {
		t.Fatalf("state before install = %q, want missing", got)
	}

	if err := writeMarker(Speaker, payloadSum(data)); err != nil {
		t.Fatalf("writeMarker: %v", err)
	}

	if got := markerState(Speaker, expectedMarker(data)); got != StateInstalled {
		t.Fatalf("state after install = %q, want installed", got)
	}

	if got := markerState(Speaker, expectedMarker([]byte("newer payload"))); got != StateOutdated {
		t.Fatalf("state against newer payload = %q, want outdated", got)
	}

	if got := markerState(Speaker, expectedMarker(nil)); got != StateInstalled {
		t.Fatalf("state without payload = %q, want installed", got)
	}
}

// TestDevicesDirHonorsTheStateOverride: PRUKKA_STATE redirects the
// records, mirroring paths.StateDir.
func TestDevicesDirHonorsTheStateOverride(t *testing.T) {
	state := t.TempDir()
	t.Setenv("PRUKKA_STATE", state)

	if got := devicesDir(); got != filepath.Join(state, "devices") {
		t.Fatalf("devicesDir = %q", got)
	}

	if got := markerPath(Speaker); got != filepath.Join(state, "devices", "speaker.installed") {
		t.Fatalf("markerPath = %q", got)
	}
}
