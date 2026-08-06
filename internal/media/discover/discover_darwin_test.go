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

	"github.com/ubyte-source/prukka/internal/media/deviceurl"
)

func TestCoreAudioOutputsEnumerates(t *testing.T) {
	t.Parallel()

	for _, d := range coreAudioOutputs(t.Context()) {
		if !strings.HasPrefix(d.URL, "device://audio/") || d.Label == "" || d.Kind != AudioOut {
			t.Fatalf("malformed output device: %+v", d)
		}
	}
}

func TestDevicesWithoutFFmpegStillListsOutputs(t *testing.T) {
	t.Parallel()

	for _, d := range Devices(t.Context(), "") {
		if d.Kind != AudioOut {
			t.Fatalf("capture device %+v listed without ffmpeg", d)
		}
	}
}

// TestMain lets the test binary impersonate the ffmpeg listing when re-exec'd
// through a planted symlink.
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

	devices := Devices(t.Context(), stub)

	assertContains(t, devices, Device{
		URL:   "device://audio/0",
		Label: "Stub Microphone",
		Kind:  AudioIn,
	})
	assertContains(t, devices, Device{URL: "device://audio/2", Label: "Stub Microphone", Kind: AudioIn})
	assertContains(t, devices, Device{URL: "device://video/0", Label: "Stub Camera", Kind: VideoIn})
	assertContains(t, devices, Device{
		URL:     "device://audio/1?label=Prukka+Microphone",
		Label:   "Prukka Microphone",
		Kind:    AudioIn,
		Virtual: true,
	})
}

func TestOutputIndexTracksTheCurrentArray(t *testing.T) {
	t.Parallel()

	labels := map[int]string{}
	counts := map[string]int{}
	for _, d := range coreAudioOutputs(t.Context()) {
		ref, parseErr := deviceurl.Parse(d.URL)
		if parseErr != nil {
			t.Fatalf("output URL %q is not a device URL: %v", d.URL, parseErr)
		}
		index, err := strconv.Atoi(ref.ID)
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

func TestOutputCatalogNeverWaitsForNativeRefresh(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	updated := &outputSnapshot{outputs: []coreAudioOutput{{
		index: 7,
		rate:  48_000,
		label: "Prukka Microphone",
		uid:   "PrukkaMicUID",
	}}}
	catalog := newCatalog(t.Context(), func() (*outputSnapshot, bool) {
		calls.Add(1)
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release

		return updated, true
	})
	initial := &outputSnapshot{outputs: []coreAudioOutput{{
		index: 3,
		rate:  16_000,
		label: "Prukka Microphone",
		uid:   "PrukkaMicUID",
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
}

func assertCatalogReturns(t *testing.T, c *catalog[outputSnapshot], want *outputSnapshot) {
	t.Helper()

	returned := make(chan *outputSnapshot, 1)
	go func() { returned <- c.current() }()
	select {
	case got := <-returned:
		if got != want {
			t.Fatalf("current snapshot = %p, want published generation %p", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("snapshot lookup waited for the native refresh")
	}
}

func awaitCatalogSnapshot(t *testing.T, c *catalog[outputSnapshot], want *outputSnapshot) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for c.snapshot.Load() != want {
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
		index: 3,
		rate:  48_000,
		label: "Prukka Microphone",
		uid:   "PrukkaMicUID",
	}}}
	catalog := newCatalog(t.Context(), func() (*outputSnapshot, bool) {
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
}

func TestColdOutputCatalogWaitHonorsContext(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	catalog := newCatalog(t.Context(), func() (*outputSnapshot, bool) {
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
}

func TestColdOutputCatalogWaitReturnsFirstSnapshot(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	want := &outputSnapshot{outputs: []coreAudioOutput{{
		index: 3,
		rate:  48_000,
		label: "Prukka Microphone",
		uid:   "PrukkaMicUID",
	}}}
	catalog := newCatalog(t.Context(), func() (*outputSnapshot, bool) {
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
}

// assertContains fails unless want is among the enumerated devices.
func assertContains(t *testing.T, devices []Device, want Device) {
	t.Helper()

	if slices.Contains(devices, want) {
		return
	}

	t.Fatalf("devices = %+v, missing %+v", devices, want)
}

func TestOutputSnapshotUniqueResolvesOnlyUnambiguousLabels(t *testing.T) {
	t.Parallel()

	snapshot := &outputSnapshot{outputs: []coreAudioOutput{
		{index: 0, rate: 48_000, label: "Studio Display Speakers", uid: "StudioUID"},
		{index: 2, rate: 44_100, label: "USB Speakers", uid: "UsbUID-1"},
		{index: 5, rate: 48_000, label: "USB Speakers", uid: "UsbUID-2"},
	}}

	output, ok := snapshot.unique("Studio Display Speakers")
	if !ok || output.index != 0 || output.uid != "StudioUID" {
		t.Fatalf("unique = (%+v, %v), want the single match", output, ok)
	}
	if output, ok := snapshot.unique("USB Speakers"); ok || output != (coreAudioOutput{}) {
		t.Fatalf("unique duplicate = (%+v, %v), want rejected", output, ok)
	}
	if _, ok := snapshot.unique("absent"); ok {
		t.Fatal("unique resolved an absent label")
	}
}

func TestOutputSnapshotDevicesLabelsAndFlags(t *testing.T) {
	t.Parallel()

	snapshot := &outputSnapshot{outputs: []coreAudioOutput{
		{index: 1, rate: 48_000, label: "Studio Display Speakers", uid: "StudioUID"},
		{index: 2, rate: 44_100, label: "USB Speakers", uid: "UsbUID-1"},
		{index: 5, rate: 48_000, label: "USB Speakers", uid: "UsbUID-2"},
		{index: 7, rate: 16_000, label: "Loopback", uid: "PrukkaSpeakerUID"},
	}}

	want := []Device{
		{URL: "device://audio/1?label=Studio+Display+Speakers", Label: "Studio Display Speakers", Kind: AudioOut},
		{URL: "device://audio/2", Label: "USB Speakers", Kind: AudioOut},
		{URL: "device://audio/5", Label: "USB Speakers", Kind: AudioOut},
		{URL: "device://audio/7?label=Loopback", Label: "Loopback", Kind: AudioOut, Virtual: true},
	}
	if got := snapshot.devices(); !slices.Equal(got, want) {
		t.Fatalf("devices = %+v, want %+v", got, want)
	}

	var none *outputSnapshot
	if got := none.devices(); got != nil {
		t.Fatalf("nil snapshot devices = %+v, want nil", got)
	}
}

func TestOutputStampFingerprintsUniqueLabels(t *testing.T) {
	t.Parallel()

	if stamp, ok := OutputStamp("no-such-device-label"); ok || stamp != "" {
		t.Fatalf("unknown label stamp = (%q, %v)", stamp, ok)
	}

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
