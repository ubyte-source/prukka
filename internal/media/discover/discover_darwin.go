//go:build darwin

package discover

/*
#cgo LDFLAGS: -framework CoreAudio -framework CoreFoundation

#include <CoreAudio/CoreAudio.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>
#include <string.h>

// The enumeration walks kAudioHardwarePropertyDevices in system order —
// the same array ffmpeg's audiotoolbox muxer indexes, so a device's
// position here is exactly its -audio_device_index.

#define PRUKKA_MAX_DEVICES 128

static AudioDeviceID prukkaDevices[PRUKKA_MAX_DEVICES];
static char prukkaName[256];
static char prukkaUID[256];

// prukkaLoadDevices refreshes the device array and returns its length.
static int prukkaLoadDevices(void) {
	AudioObjectPropertyAddress addr = {
		kAudioHardwarePropertyDevices,
		kAudioObjectPropertyScopeGlobal,
		kAudioObjectPropertyElementMain,
	};

	UInt32 size = 0;
	if (AudioObjectGetPropertyDataSize(kAudioObjectSystemObject, &addr, 0, NULL, &size) != noErr) {
		return 0;
	}

	if (size > sizeof(prukkaDevices)) {
		size = sizeof(prukkaDevices);
	}

	if (AudioObjectGetPropertyData(kAudioObjectSystemObject, &addr, 0, NULL, &size, prukkaDevices) != noErr) {
		return 0;
	}

	return (int)(size / sizeof(AudioDeviceID));
}

// prukkaCopyString reads one CFString property into buf.
static int prukkaCopyString(AudioDeviceID dev, AudioObjectPropertySelector sel, char *buf, size_t len) {
	AudioObjectPropertyAddress addr = {sel, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};

	CFStringRef ref = NULL;
	UInt32 size = sizeof(ref);

	buf[0] = '\0';

	if (AudioObjectGetPropertyData(dev, &addr, 0, NULL, &size, &ref) != noErr || ref == NULL) {
		return -1;
	}

	Boolean ok = CFStringGetCString(ref, buf, (CFIndex)len, kCFStringEncodingUTF8);
	CFRelease(ref);

	return ok ? 0 : -1;
}

// prukkaOutputChannels sums the device's output stream channels.
static int prukkaOutputChannels(AudioDeviceID dev) {
	AudioObjectPropertyAddress addr = {
		kAudioDevicePropertyStreamConfiguration,
		kAudioObjectPropertyScopeOutput,
		kAudioObjectPropertyElementMain,
	};

	UInt32 size = 0;
	if (AudioObjectGetPropertyDataSize(dev, &addr, 0, NULL, &size) != noErr || size == 0) {
		return 0;
	}

	AudioBufferList *list = (AudioBufferList *)malloc(size);
	if (list == NULL) {
		return 0;
	}

	int channels = 0;
	if (AudioObjectGetPropertyData(dev, &addr, 0, NULL, &size, list) == noErr) {
		for (UInt32 i = 0; i < list->mNumberBuffers; i++) {
			channels += (int)list->mBuffers[i].mNumberChannels;
		}
	}

	free(list);

	return channels;
}

// prukkaDeviceInfo fills the static name/uid buffers for one array slot
// and returns its output channel count (-1 on lookup failure).
static int prukkaDeviceInfo(int index) {
	AudioDeviceID dev = prukkaDevices[index];

	if (prukkaCopyString(dev, kAudioObjectPropertyName, prukkaName, sizeof(prukkaName)) != 0) {
		return -1;
	}

	prukkaCopyString(dev, kAudioDevicePropertyDeviceUID, prukkaUID, sizeof(prukkaUID));

	return prukkaOutputChannels(dev);
}

static const char *prukkaNamePtr(void) { return prukkaName; }
static const char *prukkaUIDPtr(void) { return prukkaUID; }
*/
import "C"

import (
	"context"
	neturl "net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
)

// labeledAudioURL tags an audio device URL with its stable display name:
// positional indexes reshuffle whenever any device appears or vanishes,
// and consumers rebind by label at start time.
func labeledAudioURL(id, label string) string {
	return "device://audio/" + id + "?label=" + neturl.QueryEscape(label)
}

// coreAudioMu guards the C side's static device and string buffers.
var coreAudioMu sync.Mutex

// captureListBudget bounds the avfoundation listing on its own: a pending
// camera-consent decision can wedge it, and the native playback list must
// still come back promptly.
const captureListBudget = 2500 * time.Millisecond

// Devices enumerates capture sources through ffmpeg's avfoundation layer
// (its indexes are what device:// sources use) and playback targets
// through CoreAudio, whose array positions are audiotoolbox indexes.
func Devices(ctx context.Context, bin string) ([]Device, error) {
	var out []Device

	if bin != "" {
		listCtx, cancel := context.WithTimeout(ctx, captureListBudget)
		defer cancel()

		raw, err := ffmpeg.ListRaw(listCtx, bin, "-hide_banner", "-f", "avfoundation", "-list_devices", "true", "-i", "")
		if err == nil {
			audio, video := parseAVFoundation(raw)
			for _, e := range audio {
				out = append(out, Device{
					URL: labeledAudioURL(e.id, e.label), Label: e.label, Kind: AudioIn, Virtual: virtualLabel(e.label),
				})
			}

			for _, e := range video {
				out = append(out, Device{
					URL: "device://video/" + e.id, Label: e.label, Kind: VideoIn, Virtual: virtualLabel(e.label),
				})
			}
		}
	}

	out = append(out, coreAudioOutputs()...)

	return appendNativeVideoOutput(out, "Prukka Camera", ffmpeg.NativeVideoAvailable(ctx)), nil
}

// coreAudioOutputs lists every device with output channels; the URL index
// is the device's position in the full system array.
func coreAudioOutputs() []Device {
	coreAudioMu.Lock()
	defer coreAudioMu.Unlock()

	var out []Device

	count := int(C.prukkaLoadDevices())
	for i := range count {
		channels := int(C.prukkaDeviceInfo(C.int(i)))
		if channels <= 0 {
			continue
		}

		label := C.GoString(C.prukkaNamePtr())
		uid := C.GoString(C.prukkaUIDPtr())

		out = append(out, Device{
			URL:     labeledAudioURL(strconv.Itoa(i), label),
			Label:   label,
			Kind:    AudioOut,
			Virtual: virtualLabel(label) || strings.HasPrefix(uid, "Prukka"),
		})
	}

	return out
}

// OutputIndex returns the label's CURRENT position in the system device
// array — the value audiotoolbox indexes — so output targets rebind at
// start instead of trusting a stale enumeration.
func OutputIndex(label string) (int, bool) {
	coreAudioMu.Lock()
	defer coreAudioMu.Unlock()

	count := int(C.prukkaLoadDevices())
	for i := range count {
		if int(C.prukkaDeviceInfo(C.int(i))) <= 0 {
			continue
		}
		if C.GoString(C.prukkaNamePtr()) == label {
			return i, true
		}
	}

	return 0, false
}
