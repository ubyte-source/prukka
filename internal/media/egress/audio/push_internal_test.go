package audio

// Tests for the push layer: which target a push resolves to, which arguments
// and start hook it needs, and what one Push call remembers.

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
	"github.com/ubyte-source/prukka/internal/hostos"

	"github.com/ubyte-source/prukka/internal/testkit"
)

func TestNetworkMuxMatchesTheTransport(t *testing.T) {
	t.Parallel()

	for target, want := range map[string]string{
		"rtmp://example.test/live/key":  "flv",
		"rtmps://example.test/live/key": "flv",
		"srt://example.test:9000":       "mpegts",
	} {
		got, err := networkMux(target)
		if err != nil || got != want {
			t.Fatalf("networkMux(%q) = (%q, %v), want %q", target, got, err, want)
		}
	}

	for _, target := range []string{"/tmp/output.flv", "file:///tmp/output.flv", "https://example.test/live"} {
		if _, err := networkMux(target); err == nil {
			t.Fatalf("networkMux accepted unsupported target %q", target)
		}
	}
}

// TestPushDoesNotRememberUnserveableTargets: a target the daemon can never
// serve must not become a relaunching intent.
func TestPushDoesNotRememberUnserveableTargets(t *testing.T) {
	t.Parallel()

	r := NewRegistry(t.Context(), nil, nil, discardLogger())
	defer r.Drop("call")
	r.Create("call", "en", idleMixer())

	if err := r.Push("call", "en", "ftp://nowhere/live", "off"); err == nil {
		t.Fatal("unserveable target must fail")
	}
	r.mu.RLock()
	kept := len(r.routes)
	r.mu.RUnlock()
	if kept != 0 {
		t.Fatalf("unserveable target remembered: %d routes", kept)
	}
}

func TestDeviceBufferDurationTracksFeedQuantum(t *testing.T) {
	t.Parallel()

	call := defaultFeedConfig()
	WithFeedQuantum(20 * time.Millisecond)(&call)
	if got := deviceBufferDuration(call); got != 40*time.Millisecond {
		t.Fatalf("call device buffer = %v, want 40ms", got)
	}
	if got := deviceBufferDuration(defaultFeedConfig()); got != 200*time.Millisecond {
		t.Fatalf("broadcast device buffer = %v, want 200ms", got)
	}
}

// TestDevicePushPrefersThePlaybackHelper: a labeled device target with the
// native helper available renders through it — no supervisor (ffmpeg) needed —
// and PCM flows into the helper's stdin. The helper binds the device by NAME,
// which is the whole point: array indexes reshuffle, names do not.
func TestDevicePushPrefersThePlaybackHelper(t *testing.T) {
	if runtime.GOOS == hostos.Windows {
		t.Skip("the playback helper is a darwin binary; windows uses WASAPI")
	}
	pacing := makeFeedConfig(5 * time.Millisecond)

	dir := t.TempDir()
	sinkFile := filepath.Join(dir, "captured.pcm")
	helper := filepath.Join(dir, "fake-helper")
	script := "#!/bin/sh\nexec cat > \"" + sinkFile + "\"\n"
	writeFakeHelper(t, helper, script)

	r := NewRegistry(t.Context(), nil, nil, discardLogger(),
		WithPlaybackHelper(func() string { return helper }))
	defer r.Drop("call")
	r.Create("call", "de", pipeline.NewVoiceQueue(0), WithFeedQuantum(5*time.Millisecond))

	if err := r.startDeviceAudioJob("call", "de", "device://audio/3?label=Prukka+Microphone"); err != nil {
		t.Fatalf("helper-backed device push: %v", err)
	}
	// A minute of bounded patience, not a weakened assertion: on a saturated
	// runner the shell spawn plus the 5ms pacing ticker can take double-digit
	// seconds to land the first quanta.
	testkit.Eventually(t, time.Minute, func() bool {
		info, err := os.Stat(sinkFile)

		return err == nil && info.Size() >= int64(2*pacing.samples)
	}, "no PCM reached the playback helper")
}

// TestDevicePushWithoutHelperNeedsTheSupervisor: with no helper resolver the
// labeled target falls back to the ffmpeg path, which requires a supervisor.
func TestDevicePushWithoutHelperNeedsTheSupervisor(t *testing.T) {
	r := NewRegistry(t.Context(), nil, nil, discardLogger())
	defer r.Drop("call")
	r.Create("call", "de", pipeline.NewVoiceQueue(0), WithFeedQuantum(5*time.Millisecond))

	err := r.startDeviceAudioJob("call", "de", "device://audio/3?label=Prukka+Microphone")
	if !errors.Is(err, core.ErrNotReady) {
		t.Fatalf("fallback without a supervisor = %v, want ErrNotReady", err)
	}
}

// writeFakeHelper stages an executable stand-in for the playback helper.
func writeFakeHelper(t *testing.T, path, script string) {
	t.Helper()
	mode := os.FileMode(0o700)
	if err := os.WriteFile(path, []byte(script), mode); err != nil {
		t.Fatal(err)
	}
}

// recordingStarter captures the arguments of every StartSink call so tests can
// prove the device args are re-derived on each open.
type recordingStarter struct {
	args [][]string
	mu   sync.Mutex
}

func (r *recordingStarter) StartSink(_ context.Context, args []string) (io.WriteCloser, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.args = append(r.args, append([]string(nil), args...))

	return devNullSink{}, nil
}

func (r *recordingStarter) calls() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([][]string(nil), r.args...)
}

// TestDeviceAudioStarterRebindsArgsPerOpen: every open re-derives the device
// arguments, so a reopen after the system device array shifted binds the
// label's CURRENT index instead of injecting into whatever device now sits at
// the stale position.
func TestDeviceAudioStarterRebindsArgsPerOpen(t *testing.T) {
	if runtime.GOOS == hostos.Windows {
		t.Skip("audio device pushes use WASAPI on Windows, not ffmpeg device args")
	}

	index := atomic.Int32{}
	index.Store(3)
	resolve := func(label string) (int, bool) {
		if label != "Prukka Microphone" {
			return 0, false
		}

		return int(index.Load()), true
	}

	starter := &recordingStarter{}
	start := deviceAudioSinkStarter(starter, "device://audio/9?label=Prukka+Microphone", resolve)

	if _, err := start(t.Context()); err != nil {
		t.Fatalf("first open: %v", err)
	}
	index.Store(7) // the device array shifted between opens
	if _, err := start(t.Context()); err != nil {
		t.Fatalf("second open: %v", err)
	}

	calls := starter.calls()
	if len(calls) != 2 {
		t.Fatalf("StartSink called %d times, want 2 fresh derivations", len(calls))
	}
	if runtime.GOOS == "darwin" {
		if !slices.Contains(calls[0], "3") || !slices.Contains(calls[1], "7") {
			t.Errorf("indexes not rebound per open: first=%v second=%v", calls[0], calls[1])
		}
	}
}
