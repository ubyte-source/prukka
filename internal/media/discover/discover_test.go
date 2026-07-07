package discover

import "testing"

// TestVirtualLabelSpotsPrukkaDevices: only Prukka's own loopback devices
// are flagged virtual.
func TestVirtualLabelSpotsPrukkaDevices(t *testing.T) {
	t.Parallel()

	if !virtualLabel("Prukka Microphone") {
		t.Fatal("Prukka Microphone not flagged as virtual")
	}

	if virtualLabel("Built-in Microphone") {
		t.Fatal("a physical device was flagged as virtual")
	}
}

func TestAppendNativeVideoOutputRequiresAvailability(t *testing.T) {
	t.Parallel()

	if got := appendNativeVideoOutput(nil, "Prukka Camera", false); len(got) != 0 {
		t.Fatalf("unavailable output = %+v, want none", got)
	}

	got := appendNativeVideoOutput(nil, "Prukka Camera", true)
	want := Device{
		URL: "device://video/prukka", Label: "Prukka Camera", Kind: VideoOut, Virtual: true,
	}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("available output = %+v, want %+v", got, want)
	}
}
