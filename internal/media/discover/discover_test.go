package discover

import (
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

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

// TestColdInventoryDegradesInsteadOfEnumerating: enumeration costs seconds on
// a real host, so a list requested before the first pass completes gives up on
// time and answers again once that pass publishes.
func TestColdInventoryDegradesInsteadOfEnumerating(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	want := []Device{{URL: "device://audio/0", Label: "Stub Microphone", Kind: AudioIn}}
	inventory := NewInventory(t.Context(), func() []Device {
		<-release

		return want
	})

	start := time.Now()
	if got := inventory.List(t.Context()); got != nil {
		t.Fatalf("cold list = %+v, want manual entry", got)
	}
	if waited := time.Since(start); waited > 4*coldStartBudget {
		t.Fatalf("cold list waited %v for the enumeration, want at most %v", waited, coldStartBudget)
	}

	close(release)
	deadline := time.Now().Add(time.Second)
	for !slices.Equal(inventory.List(t.Context()), want) {
		if time.Now().After(deadline) {
			t.Fatal("completed enumeration was never published")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestWarmInventoryServesLastCompleteEnumeration: a blocked refresh never
// reaches a caller, and repeated listings coalesce onto the one worker.
func TestWarmInventoryServesLastCompleteEnumeration(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	want := []Device{{URL: "device://audio/0", Label: "Stub Microphone", Kind: AudioIn}}
	inventory := NewInventory(t.Context(), func() []Device {
		if calls.Add(1) > 1 {
			entered <- struct{}{}
			<-release
		}

		return want
	})

	if got := inventory.List(t.Context()); !slices.Equal(got, want) {
		t.Fatalf("first list = %+v, want %+v", got, want)
	}
	// That listing coalesced into the pass the constructor had already
	// scheduled; this one queues the pass that blocks.
	inventory.List(t.Context())

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("listing did not schedule the next enumeration")
	}

	for range 64 {
		start := time.Now()
		if got := inventory.List(t.Context()); !slices.Equal(got, want) {
			t.Fatalf("warm list = %+v, want %+v", got, want)
		}
		if waited := time.Since(start); waited >= coldStartBudget {
			t.Fatalf("warm list waited %v for the blocked enumeration", waited)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("blocked enumerations = %d, want one coalesced worker", got)
	}

	close(release)
}
