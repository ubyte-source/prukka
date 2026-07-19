//go:build darwin

package discover

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestCoreAudioOutputsEnumerates: the CoreAudio walk returns well-formed
// playback devices on whatever hardware the host has (possibly none).
func TestCoreAudioOutputsEnumerates(t *testing.T) {
	t.Parallel()

	for _, d := range coreAudioOutputs(t.Context()) {
		if !strings.HasPrefix(d.URL, "device://audio/") || d.Label == "" || d.Kind != AudioOut {
			t.Fatalf("malformed output device: %+v", d)
		}
	}
}

// TestDevicesWithoutFFmpegStillListsOutputs: no ffmpeg means no capture
// list, never an error — playback enumeration is native.
func TestDevicesWithoutFFmpegStillListsOutputs(t *testing.T) {
	t.Parallel()

	devices, err := Devices(t.Context(), "")
	if err != nil {
		t.Fatalf("Devices returned error: %v", err)
	}

	for _, d := range devices {
		if d.Kind != AudioOut {
			t.Fatalf("capture device %+v listed without ffmpeg", d)
		}
	}
}

// TestMain lets the test binary impersonate the ffmpeg listing when
// re-exec'd through the symlink TestDevicesParsesCaptureListing plants.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "ffmpeg" {
		fmt.Fprint(os.Stderr, avfStubListing)
		os.Exit(1) // a listing run always exits non-zero (no real input)
	}

	os.Exit(m.Run())
}

// avfStubListing replays the real-world avfoundation listing shape.
const avfStubListing = `[AVFoundation indev @ 0x1] AVFoundation video devices:
[AVFoundation indev @ 0x1] [0] Stub Camera
[AVFoundation indev @ 0x1] AVFoundation audio devices:
[AVFoundation indev @ 0x1] [0] Stub Microphone
[AVFoundation indev @ 0x1] [1] Prukka Microphone
[AVFoundation indev @ 0x1] [2] Stub Microphone
`

// TestDevicesParsesCaptureListing: the ffmpeg branch turns a listing into
// capture devices; the impersonated binary replays the real-world output.
func TestDevicesParsesCaptureListing(t *testing.T) {
	t.Parallel()

	exe, exeErr := os.Executable()
	if exeErr != nil {
		t.Fatalf("locate test binary: %v", exeErr)
	}

	stub := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.Symlink(exe, stub); err != nil {
		t.Fatalf("plant stub ffmpeg: %v", err)
	}

	devices, err := Devices(t.Context(), stub)
	if err != nil {
		t.Fatalf("Devices returned error: %v", err)
	}

	assertContains(t, devices, Device{
		URL: "device://audio/0", Label: "Stub Microphone", Kind: AudioIn,
	})
	assertContains(t, devices, Device{URL: "device://audio/2", Label: "Stub Microphone", Kind: AudioIn})
	assertContains(t, devices, Device{URL: "device://video/0", Label: "Stub Camera", Kind: VideoIn})
	assertContains(t, devices, Device{
		URL: "device://audio/1?label=Prukka+Microphone", Label: "Prukka Microphone", Kind: AudioIn, Virtual: true,
	})
}

// TestOutputIndexTracksTheCurrentArray: the label lookup must agree with
// the enumeration it rebinds for — on whatever devices this machine has.
func TestOutputIndexTracksTheCurrentArray(t *testing.T) {
	t.Parallel()

	labels := map[int]string{}
	counts := map[string]int{}
	for _, d := range coreAudioOutputs(t.Context()) {
		raw := strings.TrimPrefix(d.URL, "device://audio/")
		id, _, _ := strings.Cut(raw, "?")
		index, err := strconv.Atoi(id)
		if err != nil {
			t.Fatalf("output URL %q has a non-numeric index", d.URL)
		}
		labels[index] = d.Label
		counts[d.Label]++
	}
	if len(labels) == 0 {
		t.Skip("no output devices on this machine")
	}

	for _, label := range labels {
		index, ok := OutputIndex(label)
		if counts[label] > 1 {
			if ok {
				t.Fatalf("OutputIndex(%q) = %d, want ambiguous duplicate rejected", label, index)
			}

			continue
		}
		if !ok {
			t.Fatalf("OutputIndex(%q) not found in the live array", label)
		}
		if labels[index] != label {
			t.Fatalf("OutputIndex(%q) = %d, which is %q", label, index, labels[index])
		}
	}
}

// TestOutputCatalogNeverWaitsForNativeRefresh proves the routing-side safety
// boundary: even a permanently blocked CoreAudio call cannot hold a push
// caller, and repeated lookups create only one native worker.
func TestOutputCatalogNeverWaitsForNativeRefresh(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	updated := &outputSnapshot{outputs: []coreAudioOutput{{
		index: 7, rate: 48_000, label: "Prukka Microphone", uid: "PrukkaMicUID",
	}}}
	catalog := newOutputCatalog(func() (*outputSnapshot, bool) {
		calls.Add(1)
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release

		return updated, true
	})
	initial := &outputSnapshot{outputs: []coreAudioOutput{{
		index: 3, rate: 16_000, label: "Prukka Microphone", uid: "PrukkaMicUID",
	}}}
	catalog.publish(initial)

	assertCatalogReturns(t, catalog, initial)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("snapshot lookup did not schedule a native refresh")
	}

	for range 64 {
		if got := catalog.current(); got != initial {
			t.Fatalf("lookup observed partial refresh %p, want %p", got, initial)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("blocked native refresh calls = %d, want one bounded worker", got)
	}

	close(release)
	awaitCatalogSnapshot(t, catalog, updated)
	close(catalog.requests)
}

func assertCatalogReturns(t *testing.T, catalog *outputCatalog, want *outputSnapshot) {
	t.Helper()

	returned := make(chan *outputSnapshot, 1)
	go func() { returned <- catalog.current() }()
	select {
	case got := <-returned:
		if got != want {
			t.Fatalf("current snapshot = %p, want published generation %p", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("snapshot lookup waited for the native refresh")
	}
}

func awaitCatalogSnapshot(t *testing.T, catalog *outputCatalog, want *outputSnapshot) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for catalog.snapshot.Load() != want {
		if time.Now().After(deadline) {
			t.Fatal("completed native refresh was not published")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestOutputFingerprintIncludesArrayPosition(t *testing.T) {
	t.Parallel()

	before := coreAudioOutput{index: 3, rate: 48_000, label: "Prukka Microphone", uid: "PrukkaMicUID"}
	after := before
	after.index = 7
	if outputFingerprint(before) == outputFingerprint(after) {
		t.Fatal("device-array reorder did not change the output fingerprint")
	}
}

func TestOutputCatalogKeepsLastCompleteGeneration(t *testing.T) {
	t.Parallel()

	attempted := make(chan struct{}, 1)
	want := &outputSnapshot{outputs: []coreAudioOutput{{
		index: 3, rate: 48_000, label: "Prukka Microphone", uid: "PrukkaMicUID",
	}}}
	catalog := newOutputCatalog(func() (*outputSnapshot, bool) {
		attempted <- struct{}{}

		return nil, false
	})
	catalog.publish(want)
	if got := catalog.current(); got != want {
		t.Fatalf("cached generation = %p, want %p", got, want)
	}
	select {
	case <-attempted:
	case <-time.After(time.Second):
		t.Fatal("refresh was not attempted")
	}
	if got := catalog.snapshot.Load(); got != want {
		t.Fatalf("failed refresh replaced the complete snapshot: got %p, want %p", got, want)
	}
	close(catalog.requests)
}

func TestColdOutputCatalogWaitHonorsContext(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	catalog := newOutputCatalog(func() (*outputSnapshot, bool) {
		close(started)
		<-release
		close(finished)

		return nil, false
	})
	ctx, cancel := context.WithCancel(t.Context())
	returned := make(chan *outputSnapshot, 1)
	go func() { returned <- catalog.currentWithin(ctx) }()
	<-started
	cancel()
	select {
	case snapshot := <-returned:
		if snapshot != nil {
			t.Fatalf("canceled cold lookup returned snapshot %p", snapshot)
		}
	case <-time.After(time.Second):
		t.Fatal("cold lookup ignored its context deadline")
	}
	close(release)
	<-finished
	close(catalog.requests)
}

func TestColdOutputCatalogWaitReturnsFirstSnapshot(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	want := &outputSnapshot{outputs: []coreAudioOutput{{
		index: 3, rate: 48_000, label: "Prukka Microphone", uid: "PrukkaMicUID",
	}}}
	catalog := newOutputCatalog(func() (*outputSnapshot, bool) {
		close(started)
		<-release

		return want, true
	})
	returned := make(chan *outputSnapshot, 1)
	go func() { returned <- catalog.currentWithin(t.Context()) }()
	<-started
	close(release)
	select {
	case got := <-returned:
		if got != want {
			t.Fatalf("first published snapshot = %p, want %p", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("cold lookup did not observe the first publication")
	}
	close(catalog.requests)
}

// assertContains fails unless want is among the enumerated devices.
func assertContains(t *testing.T, devices []Device, want Device) {
	t.Helper()

	if slices.Contains(devices, want) {
		return
	}

	t.Fatalf("devices = %+v, missing %+v", devices, want)
}

// TestOutputStampFingerprintsUniqueLabels: a resolvable label yields a
// stable uid@rate fingerprint; unknown labels report unwatchable.
func TestOutputStampFingerprintsUniqueLabels(t *testing.T) {
	t.Parallel()

	if stamp, ok := OutputStamp("no-such-device-label"); ok || stamp != "" {
		t.Fatalf("unknown label stamp = (%q, %v)", stamp, ok)
	}

	// Fingerprint every currently unique output label: each must be
	// non-empty, carry a rate suffix and be immediately reproducible.
	for _, device := range coreAudioOutputs(t.Context()) {
		first, ok := OutputStamp(device.Label)
		if !ok {
			continue // duplicate labels are deliberately unwatchable
		}
		if !strings.Contains(first, "@") {
			t.Fatalf("stamp %q for %q lacks a rate suffix", first, device.Label)
		}
		second, ok := OutputStamp(device.Label)
		if !ok || second != first {
			t.Fatalf("stamp for %q is unstable: %q then (%q, %v)", device.Label, first, second, ok)
		}
	}
}
