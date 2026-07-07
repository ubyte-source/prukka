package discover

import "testing"

// avfSample is a verbatim `-f avfoundation -list_devices true` capture.
const avfSample = `[AVFoundation indev @ 0x7fa062807c40] AVFoundation video devices:
[AVFoundation indev @ 0x7fa062807c40] [0] FaceTime HD Camera
[AVFoundation indev @ 0x7fa062807c40] [1] Fotocamera di iPhone di Paolo
[AVFoundation indev @ 0x7fa062807c40] [2] Capture screen 0
[AVFoundation indev @ 0x7fa062807c40] AVFoundation audio devices:
[AVFoundation indev @ 0x7fa062807c40] [0] Microfono di iPhone di Paolo
[AVFoundation indev @ 0x7fa062807c40] [1] Built-in Microphone
[AVFoundation indev @ 0x7fa062807c40] [2] Prukka Speaker
[AVFoundation indev @ 0x7fa062807c40] [3] Prukka Microphone
[in#0 @ 0x7fa062807540] Error opening input: Input/output error
Error opening input file .
`

// TestParseAVFoundationSplitsSections: indexes and names land in the
// right section, and the trailing error noise is ignored.
func TestParseAVFoundationSplitsSections(t *testing.T) {
	t.Parallel()

	audio, video := parseAVFoundation(avfSample)

	if len(audio) != 4 || len(video) != 3 {
		t.Fatalf("parsed %d audio and %d video devices, want 4 and 3", len(audio), len(video))
	}

	if audio[3].id != "3" || audio[3].label != "Prukka Microphone" {
		t.Fatalf("audio[3] = %+v, want [3] Prukka Microphone", audio[3])
	}

	if video[0].id != "0" || video[0].label != "FaceTime HD Camera" {
		t.Fatalf("video[0] = %+v, want [0] FaceTime HD Camera", video[0])
	}
}

// dshowSample mirrors ffmpeg's documented dshow listing shape.
const dshowSample = `[dshow @ 0000024] DirectShow video devices (some may be both video and audio devices)
[dshow @ 0000024]  "Integrated Camera"
[dshow @ 0000024]     Alternative name "@device_pnp_\\?\usb#vid_04f2"
[dshow @ 0000024] DirectShow audio devices
[dshow @ 0000024]  "Microphone (Realtek High Definition Audio)"
[dshow @ 0000024]     Alternative name "@device_cm_{33D9A762}"
dummy: Immediate exit requested
`

// TestParseDShowUsesFriendlyNames: the quoted friendly name is both id
// and label; moniker lines never leak in.
func TestParseDShowUsesFriendlyNames(t *testing.T) {
	t.Parallel()

	audio, video := parseDShow(dshowSample)

	if len(audio) != 1 || len(video) != 1 {
		t.Fatalf("parsed %d audio and %d video devices, want 1 and 1", len(audio), len(video))
	}

	if audio[0].id != "Microphone (Realtek High Definition Audio)" || audio[0].id != audio[0].label {
		t.Fatalf("audio[0] = %+v, want the friendly name as id and label", audio[0])
	}

	if video[0].label != "Integrated Camera" {
		t.Fatalf("video[0] = %+v, want Integrated Camera", video[0])
	}
}

// pulseSample mirrors `ffmpeg -sources pulse` output: the star marks the
// default device and newer builds append the media types.
const pulseSample = `Auto-detected sources for pulse:
* alsa_input.pci-0000_00_1f.3.analog-stereo [Built-in Audio Analog Stereo] (audio)
  prukka_mic.monitor [Monitor of Prukka Microphone]
`

// TestParsePulseReadsNamesAndDescriptions: names become ids, bracketed
// descriptions become labels, the default marker is tolerated.
func TestParsePulseReadsNamesAndDescriptions(t *testing.T) {
	t.Parallel()

	got := parsePulse(pulseSample)

	if len(got) != 2 {
		t.Fatalf("parsed %d devices, want 2", len(got))
	}

	if got[0].id != "alsa_input.pci-0000_00_1f.3.analog-stereo" || got[0].label != "Built-in Audio Analog Stereo" {
		t.Fatalf("got[0] = %+v, want the alsa input with its description", got[0])
	}

	if got[1].id != "prukka_mic.monitor" {
		t.Fatalf("got[1] = %+v, want the monitor source", got[1])
	}
}
