package ffmpeg

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/media/deviceurl"
	"github.com/ubyte-source/prukka/internal/redact"
)

// TestMain lets the test binary impersonate a device listing on every OS:
// re-exec'd with PRUKKA_FAKE_LIST set it prints and exits non-zero.
func TestMain(m *testing.M) {
	if os.Getenv("PRUKKA_FAKE_LIST") == "1" {
		if _, err := os.Stdout.WriteString("listed"); err != nil {
			os.Exit(2)
		}

		os.Exit(1)
	}

	// Re-exec'd through the fakeMicCapture symlink: emit the fixed PCM and
	// exit before flag parsing sees StartPCM's arguments.
	if filepath.Base(os.Args[0]) == fakeMicCaptureName {
		if _, err := os.Stdout.Write(micCaptureHelperPCM); err != nil {
			os.Exit(2)
		}

		os.Exit(0)
	}

	os.Exit(m.Run())
}

func TestDeviceInputArgsAudio(t *testing.T) {
	t.Parallel()

	args, err := deviceInputArgsConfigured("device://audio/0", pcmConfig{})
	if err != nil {
		t.Fatalf("audio input: %v", err)
	}

	joined := strings.Join(args, " ")

	switch runtime.GOOS {
	case "darwin":
		if !strings.Contains(joined, "avfoundation") || !slices.Contains(args, ":0") {
			t.Fatalf("darwin capture args wrong: %v", args)
		}
	case "windows":
		if !strings.Contains(joined, "dshow") {
			t.Fatalf("windows capture args wrong: %v", args)
		}
	default:
		if !strings.Contains(joined, "pulse") {
			t.Fatalf("linux capture args wrong: %v", args)
		}
	}
}

func TestDeviceCaptureBufferIsCallScoped(t *testing.T) {
	t.Parallel()

	config := pcmConfig{deviceBuffer: 20 * time.Millisecond}
	windows, err := deviceInputArgsFor("windows", "device://audio/Microphone", config)
	if err != nil || !strings.Contains(strings.Join(windows, " "), "-audio_buffer_size 20") {
		t.Fatalf("Windows call capture args = %v (%v)", windows, err)
	}
	linux, err := deviceInputArgsFor("linux", "device://audio/prukka.monitor", config)
	if err != nil || !strings.Contains(strings.Join(linux, " "), "-fragment_size 3840") {
		t.Fatalf("Pulse call capture args = %v (%v)", linux, err)
	}
	darwin, err := deviceInputArgsFor("darwin", "device://audio/0", config)
	if err != nil || strings.Contains(strings.Join(darwin, " "), "buffer") {
		t.Fatalf("AVFoundation call capture args = %v (%v), want supported defaults", darwin, err)
	}

	broadcast, err := deviceInputArgsFor("windows", "device://audio/Microphone", pcmConfig{})
	if err != nil || strings.Contains(strings.Join(broadcast, " "), "audio_buffer_size") {
		t.Fatalf("Windows broadcast capture args = %v (%v), want device defaults", broadcast, err)
	}
}

func TestAVDeviceCaptureBufferReachesAudioInput(t *testing.T) {
	t.Parallel()

	config := pcmConfig{deviceBuffer: 20 * time.Millisecond}
	windows, err := avInputArgs(
		"windows", deviceurl.Ref{Kind: deviceurl.AV, ID: "Camera|Microphone"}, config,
	)
	if err != nil || !strings.Contains(strings.Join(windows.input, " "), "-audio_buffer_size 20") {
		t.Fatalf("Windows AV call capture = %v (%v)", windows.input, err)
	}
	linux, err := avInputArgs("linux", deviceurl.Ref{Kind: deviceurl.AV, ID: "video0|mic"}, config)
	if err != nil || !strings.Contains(strings.Join(linux.input, " "), "-fragment_size 3840") {
		t.Fatalf("Linux AV call capture = %v (%v)", linux.input, err)
	}
}

func TestAVDeviceInputRebindsDarwinMicrophoneByLabel(t *testing.T) {
	t.Parallel()

	av, err := deviceAVConfiguredFor(
		"darwin", "device://av/0|2?label=Continuity+Microphone", pcmConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-f", "avfoundation", "-framerate", "30", "-i", "0:Continuity Microphone"}
	if !slices.Equal(av.input, want) {
		t.Fatalf("Darwin AV input = %v, want camera index plus microphone label %v", av.input, want)
	}

	colon, err := deviceAVConfiguredFor(
		"darwin", "device://av/0|2?label=Ambiguous%3AName", pcmConfig{},
	)
	if err != nil || !slices.Contains(colon.input, "0:2") {
		t.Fatalf("colon microphone label input = %v (%v), want indexed fallback", colon.input, err)
	}
}

func TestDeviceInputArgsRebindByLabel(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "darwin" {
		t.Skip("labels rebind avfoundation captures; other platforms already address by name")
	}

	args, err := deviceInputArgsConfigured("device://audio/2?label=Built-in+Microphone", pcmConfig{})
	if err != nil || !slices.Contains(args, ":Built-in Microphone") {
		t.Fatalf("labeled capture args = %v (%v), want the name after ':'", args, err)
	}

	args, err = deviceInputArgsConfigured("device://audio/2?label=Weird%3AName", pcmConfig{})
	if err != nil || !slices.Contains(args, ":2") {
		t.Fatalf("colon label args = %v (%v), want the index fallback", args, err)
	}
}

func TestDeviceInputArgsRejectsVideoAndMalformed(t *testing.T) {
	t.Parallel()

	if _, err := deviceInputArgsConfigured("device://video/0", pcmConfig{}); err == nil {
		t.Fatal("video capture accepted as a session source")
	}

	if _, err := deviceInputArgsConfigured("device://audio", pcmConfig{}); err == nil {
		t.Fatal("malformed device URL accepted")
	}
}

func TestDeviceOutputArgsAudio(t *testing.T) {
	t.Parallel()

	audio, err := DeviceOutputArgs("device://audio/1", nil)

	switch runtime.GOOS {
	case "darwin":
		if err != nil || !strings.Contains(strings.Join(audio, " "), "audiotoolbox") {
			t.Fatalf("darwin audio out: %v (%v)", audio, err)
		}
	case "linux":
		if err != nil || !strings.Contains(strings.Join(audio, " "), "pulse") {
			t.Fatalf("linux audio out: %v (%v)", audio, err)
		}
	default:
		if err == nil {
			t.Fatal("unsupported platform must error, not no-op")
		}
	}
}

func TestPulseOutputTargetsTheNamedSink(t *testing.T) {
	t.Parallel()

	args, err := deviceOutputArgs("linux", "device://audio/prukka-mic", nil)
	want := []string{
		"-c:a", "pcm_s16le", "-f", "pulse", "-device", "prukka-mic", "prukka-dub",
	}
	if err != nil || !slices.Equal(args, want) {
		t.Fatalf("Pulse output args = %v (%v), want %v", args, err, want)
	}
}

func TestDeviceOutputArgsRebindByLabel(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "darwin" {
		t.Skip("index rebinding is an audiotoolbox concern")
	}

	resolve := func(label string) (int, bool) {
		if label == "Prukka Microphone" {
			return 7, true
		}

		return 0, false
	}

	fresh, err := DeviceOutputArgs("device://audio/2?label=Prukka+Microphone", resolve)
	if err != nil || !slices.Contains(fresh, "7") {
		t.Fatalf("labeled output args = %v (%v), want the resolver's current index", fresh, err)
	}

	stale, err := DeviceOutputArgs("device://audio/2?label=Unplugged", resolve)
	if err != nil || !slices.Contains(stale, "2") {
		t.Fatalf("unresolved label args = %v (%v), want the embedded index", stale, err)
	}
}

func TestDeviceOutputArgsLinuxVideoWritesRawFramesToTheNode(t *testing.T) {
	t.Parallel()

	out, err := deviceOutputArgs("linux", "device://video//dev/video10", nil)
	if err != nil {
		t.Fatalf("linux video out: %v", err)
	}
	joined := strings.Join(out, " ")
	if !strings.Contains(joined, "-f rawvideo") || !strings.Contains(joined, "-pix_fmt yuyv422") {
		t.Fatalf("linux video out = %q, want raw yuyv422 frames via the file protocol", joined)
	}
	if strings.Contains(joined, "v4l2") {
		t.Fatalf("linux video out = %q, must not use the v4l2 muxer", joined)
	}

	if filter := deviceVideoFilter("linux", "device://video//dev/video10"); filter != linuxWebcamFilter {
		t.Fatalf("device filter = %q, want the node's fixed-mode normalization", filter)
	}
	if filter := deviceVideoFilter("darwin", "device://video//dev/video10"); filter != "" {
		t.Fatalf("darwin device filter = %q, want none", filter)
	}
	if filter := deviceVideoFilter("linux", "rtmp://live/x"); filter != "" {
		t.Fatalf("network target filter = %q, want none", filter)
	}
}

func TestDeviceOutputArgsVideo(t *testing.T) {
	t.Parallel()

	video, err := DeviceOutputArgs("device://video//dev/video10", nil)

	if runtime.GOOS == "linux" {
		if err != nil || !strings.Contains(strings.Join(video, " "), "rawvideo") {
			t.Fatalf("linux video out: %v (%v)", video, err)
		}

		return
	}

	if err == nil {
		t.Fatal("video out must report unsupported honestly off linux")
	}
}

func TestUnsupportedDeviceOutputRedactsTheTarget(t *testing.T) {
	t.Parallel()

	const target = "device://av/cam0|mic0?label=Boardroom+Camera"

	_, err := deviceOutputArgs("darwin", target, nil)
	if err == nil {
		t.Fatal("a paired capture is no output target on any platform")
	}
	if strings.Contains(err.Error(), target) || strings.Contains(err.Error(), "Boardroom") {
		t.Fatalf("device target rendered verbatim: %q", err.Error())
	}
	if !strings.Contains(err.Error(), redact.URL(target)) {
		t.Fatalf("device target not rendered through redact.URL: %q", err.Error())
	}
	for _, want := range []string{string(deviceurl.AV), "darwin"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q dropped %q, which is what the reader needs", err.Error(), want)
		}
	}
}

func TestListRawReturnsEverythingPrinted(t *testing.T) {
	exe, exeErr := os.Executable()
	if exeErr != nil {
		t.Fatalf("locate test binary: %v", exeErr)
	}

	t.Setenv("PRUKKA_FAKE_LIST", "1")

	out, err := ListRaw(t.Context(), exe)
	if err != nil || !strings.Contains(out, "listed") {
		t.Fatalf("ListRaw = %q, %v — want the output despite the exit status", out, err)
	}

	if _, err := ListRaw(t.Context(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("ListRaw succeeded with a binary that cannot run")
	}
}

func TestDeviceAVBuildsThePairedCapture(t *testing.T) {
	t.Parallel()

	av, err := deviceAVConfigured("device://av/0|1", pcmConfig{})
	if err != nil {
		t.Fatalf("deviceAV returned error: %v", err)
	}

	joined := strings.Join(av.input, " ")

	shapes := map[string]struct {
		audioMap string
		tokens   []string
	}{
		"darwin":  {audioMap: "0:a:0", tokens: []string{"avfoundation", "0:1"}},
		"windows": {audioMap: "0:a:0", tokens: []string{"dshow", "video=0:audio=1"}},
		"linux":   {audioMap: "1:a:0", tokens: []string{"v4l2", "pulse"}},
	}

	want, ok := shapes[runtime.GOOS]
	if !ok {
		want = shapes["linux"]
	}

	for _, token := range want.tokens {
		if !strings.Contains(joined, token) {
			t.Fatalf("input = %q, missing %q", joined, token)
		}
	}

	if av.audioMap != want.audioMap || av.videoMap != "0:v:0" {
		t.Fatalf("maps = %s/%s, want %s/0:v:0", av.audioMap, av.videoMap, want.audioMap)
	}
}

func TestDeviceAVRejectsHalfPairs(t *testing.T) {
	t.Parallel()

	if _, err := deviceAVConfigured("device://av/0", pcmConfig{}); err == nil {
		t.Fatal("deviceAV accepted a pairing without a microphone")
	}
}
