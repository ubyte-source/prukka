package deviceurl_test

import (
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/media/deviceurl"
)

func TestVocabularyIsTheDocumentedSpelling(t *testing.T) {
	t.Parallel()

	spellings := map[string]string{
		"scheme":       deviceurl.Scheme,
		"audio kind":   string(deviceurl.Audio),
		"video kind":   string(deviceurl.Video),
		"av kind":      string(deviceurl.AV),
		"native video": deviceurl.NativeVideo,
	}
	want := map[string]string{
		"scheme":       "device://",
		"audio kind":   "audio",
		"video kind":   "video",
		"av kind":      "av",
		"native video": "device://video/prukka",
	}
	for name, got := range spellings {
		if got != want[name] {
			t.Errorf("%s = %q, want %q", name, got, want[name])
		}
	}
}

func TestParseSplitsEveryPublishedShape(t *testing.T) {
	t.Parallel()

	cases := map[string]deviceurl.Ref{
		"device://audio/3":                  {Kind: deviceurl.Audio, ID: "3"},
		"device://audio/default":            {Kind: deviceurl.Audio, ID: "default"},
		"device://audio/alsa_input.usb-mic": {Kind: deviceurl.Audio, ID: "alsa_input.usb-mic"},
		"device://video//dev/video0":        {Kind: deviceurl.Video, ID: "/dev/video0"},
		"device://video/prukka":             {Kind: deviceurl.Video, ID: "prukka"},
		"device://av/0|1":                   {Kind: deviceurl.AV, ID: "0|1"},
		"device://audio/3?label=Prukka+Microphone": {
			Kind:  deviceurl.Audio,
			ID:    "3",
			Label: "Prukka Microphone",
		},
		"device://av/0|2?label=Ambiguous%3AName": {
			Kind:  deviceurl.AV,
			ID:    "0|2",
			Label: "Ambiguous:Name",
		},
	}
	for raw, want := range cases {
		got, err := deviceurl.Parse(raw)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = (%#v, %v), want %#v", raw, got, err, want)
		}
	}
}

func TestParseStripsTheLabelFromTheID(t *testing.T) {
	t.Parallel()

	ref, err := deviceurl.Parse("device://audio/3?label=Speakers")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ref.ID != "3" {
		t.Fatalf("ID = %q, want the bare endpoint id %q", ref.ID, "3")
	}
	if ref.Label != "Speakers" {
		t.Fatalf("Label = %q, want %q", ref.Label, "Speakers")
	}
}

func TestParseRejectsWhatIsNotADeviceURL(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"",
		"bogus",
		"rtmp://host/app",
		"::bad::",
		"device://",
		"device://audio",
		"device://audio/",
		"device:///3",
		"device://audio/?label=Speakers",
		"device://camera/0",
	} {
		if ref, err := deviceurl.Parse(raw); err == nil {
			t.Errorf("Parse(%q) = %#v, want a grammar error", raw, ref)
		}
	}
}

func TestParseErrorNamesTheGrammar(t *testing.T) {
	t.Parallel()

	_, err := deviceurl.Parse("bogus")
	if err == nil {
		t.Fatal("malformed URL accepted")
	}
	if !strings.Contains(err.Error(), "device://audio/<id>") {
		t.Fatalf("error %q does not name the expected shape", err)
	}
}

func TestStringRebuildsWhatParseTook(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"device://audio/3",
		"device://audio/default",
		"device://video//dev/video0",
		"device://video/prukka",
		"device://av/0|1",
		"device://audio/1?label=Prukka+Microphone",
		"device://audio/2?label=Studio+Display+Speakers",
	} {
		ref, err := deviceurl.Parse(raw)
		if err != nil {
			t.Errorf("Parse(%q): %v", raw, err)

			continue
		}
		if got := ref.String(); got != raw {
			t.Errorf("Parse(%q).String() = %q, want the URL back", raw, got)
		}
	}
}

func TestStringEscapesOnlyTheLabel(t *testing.T) {
	t.Parallel()

	labeled := deviceurl.Ref{Kind: deviceurl.Audio, ID: "/dev/snd 1", Label: "Studio Display Speakers"}
	want := "device://audio//dev/snd 1?label=Studio+Display+Speakers"
	if got := labeled.String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestPairSplitsOnlyPairedCaptures(t *testing.T) {
	t.Parallel()

	paired, err := deviceurl.Parse("device://av/0|Built-in Microphone")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	camera, microphone, err := paired.Pair()
	if err != nil || camera != "0" || microphone != "Built-in Microphone" {
		t.Fatalf("Pair() = (%q, %q, %v), want the camera and the microphone", camera, microphone, err)
	}

	for _, ref := range []deviceurl.Ref{
		{Kind: deviceurl.AV, ID: "0"},
		{Kind: deviceurl.AV, ID: "0|"},
		{Kind: deviceurl.AV, ID: "|1"},
		{Kind: deviceurl.Audio, ID: "0|1"},
	} {
		if camera, microphone, err := ref.Pair(); err == nil {
			t.Errorf("Pair() on %#v = (%q, %q), want an error", ref, camera, microphone)
		}
	}
}

func TestIsClaimsTheSchemeWithoutParsing(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"device://audio/0", "device://", "device://bogus"} {
		if !deviceurl.Is(raw) {
			t.Errorf("Is(%q) = false, want the device route", raw)
		}
	}
	for _, raw := range []string{"rtmp://host/app", "file:///tmp/x.mp4", ""} {
		if deviceurl.Is(raw) {
			t.Errorf("Is(%q) = true, want the network or file route", raw)
		}
	}
}

func TestIsKindClassifiesOnlyWellFormedURLs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		raw  string
		kind deviceurl.Kind
		want bool
	}{
		{raw: "device://audio/1", kind: deviceurl.Audio, want: true},
		{raw: "device://audio/1?label=Mic", kind: deviceurl.Audio, want: true},
		{raw: "device://video/0", kind: deviceurl.Audio, want: false},
		{raw: "device://av/0|1", kind: deviceurl.AV, want: true},
		{raw: "device://audio/1", kind: deviceurl.AV, want: false},
		{raw: "device://av/", kind: deviceurl.AV, want: false},
		{raw: "device://audio/", kind: deviceurl.Audio, want: false},
		{raw: "rtmp://host/app", kind: deviceurl.Audio, want: false},
	}
	for _, c := range cases {
		if got := deviceurl.IsKind(c.raw, c.kind); got != c.want {
			t.Errorf("IsKind(%q, %q) = %v, want %v", c.raw, c.kind, got, c.want)
		}
	}
}

func TestIsNativeVideoIsExact(t *testing.T) {
	t.Parallel()

	if !deviceurl.IsNativeVideo(deviceurl.NativeVideo) {
		t.Fatal("the native target was not recognized")
	}
	if deviceurl.IsNativeVideo(deviceurl.NativeVideo + "/extra") {
		t.Fatal("a non-canonical native target was recognized")
	}
	if !deviceurl.IsKind(deviceurl.NativeVideo, deviceurl.Video) {
		t.Fatal("the native target is not a video device URL")
	}
}

func TestAudioLabelAnswersOnlyForLabeledAudio(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"device://audio/3?label=Prukka+Microphone": "Prukka Microphone",
		"device://audio/3":                         "",
		"device://video/0?label=Cam":               "",
		"rtmp://host/app":                          "",
		"::bad::":                                  "",
	}
	for target, want := range cases {
		if got := deviceurl.AudioLabel(target); got != want {
			t.Errorf("AudioLabel(%q) = %q, want %q", target, got, want)
		}
	}
}

// TestParseErrorsNameNoDeviceIdentity pins that a rejected device URL is named
// by its scheme alone: the id and the label are capture hardware.
func TestParseErrorsNameNoDeviceIdentity(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"device://camera/Blue Yeti USB",
		"device://audio/?label=Blue Yeti USB",
		"/Users/alice/private/mic.wav",
	} {
		_, err := deviceurl.Parse(raw)
		if err == nil {
			t.Fatalf("Parse(%q) accepted a malformed device URL", raw)
		}
		for _, secret := range []string{"Blue Yeti", "alice", "private"} {
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("Parse(%q) error exposes %q: %v", raw, secret, err)
			}
		}
	}
}

// TestPairErrorNamesNoDeviceIdentity pins the same rule for the paired form.
func TestPairErrorNamesNoDeviceIdentity(t *testing.T) {
	t.Parallel()

	_, _, err := deviceurl.Ref{Kind: deviceurl.AV, ID: "Blue Yeti USB"}.Pair()
	if err == nil {
		t.Fatal("Pair accepted an unpaired ref")
	}
	if strings.Contains(err.Error(), "Blue Yeti") {
		t.Fatalf("Pair error exposes the device id: %v", err)
	}
}
