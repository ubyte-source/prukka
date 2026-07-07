package hls_test

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/media/egress/hls"
)

func newStore(t *testing.T) *hls.Store {
	t.Helper()

	return hls.NewStore(filepath.Join(t.TempDir(), "media"), slog.New(slog.DiscardHandler))
}

func TestCreateBuildsTheSessionTree(t *testing.T) {
	t.Parallel()

	store := newStore(t)

	session, err := store.Create("demo", []core.Lang{"it", "en"})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	for _, dir := range []string{
		session.VideoDir(),
		session.AudioDir("it"),
		session.AudioDir("en"),
	} {
		if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
			t.Fatalf("missing tree directory %s: %v", dir, statErr)
		}
	}

	if session.Subtitles("en") == nil {
		t.Fatal("no subtitle segmenter for a created language")
	}
}

func TestCreateIsIdempotentForLanguageUpdates(t *testing.T) {
	t.Parallel()

	store := newStore(t)

	if _, err := store.Create("demo", []core.Lang{"it"}); err != nil {
		t.Fatalf("first Create: %v", err)
	}

	session, err := store.Create("demo", []core.Lang{"it", "fr"})
	if err != nil {
		t.Fatalf("recreate with new language: %v", err)
	}

	if session.Subtitles("fr") == nil {
		t.Fatal("recreate did not pick up the added language")
	}
}

func TestDropRemovesTheTree(t *testing.T) {
	t.Parallel()

	store := newStore(t)

	if _, ok := store.MasterPlaylist("ghost"); ok {
		t.Fatal("unknown session reported a master playlist")
	}

	session, err := store.Create("demo", []core.Lang{"it"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	store.Drop("demo")

	if _, ok := store.MasterPlaylist("demo"); ok {
		t.Fatal("dropped session still reports a master playlist")
	}

	if _, statErr := os.Stat(session.VideoDir()); !os.IsNotExist(statErr) {
		t.Fatalf("dropped tree still on disk: %v", statErr)
	}
}

func TestMasterPlaylistAudioOnly(t *testing.T) {
	t.Parallel()

	store := newStore(t)

	if _, err := store.Create("demo", []core.Lang{"it", "en"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	master, ok := store.MasterPlaylist("demo")
	if !ok {
		t.Fatal("MasterPlaylist reported unknown session")
	}

	text := string(master)

	for _, want := range []string{
		"#EXTM3U",
		`TYPE=AUDIO,GROUP-ID="dub",NAME="it"`,
		`TYPE=SUBTITLES,GROUP-ID="subs",NAME="en"`,
		"audio/it/index.m3u8",
		"subs/en/index.m3u8",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("master missing %q:\n%s", want, text)
		}
	}

	// Without a video rendition the variant must not reference video/.
	if strings.Contains(text, "video/index.m3u8") {
		t.Fatalf("audio-only master references a missing video rendition:\n%s", text)
	}

	// Exactly one language is the default track.
	if strings.Count(text, "DEFAULT=YES") != 1 {
		t.Fatalf("want exactly one default audio rendition:\n%s", text)
	}
}

func TestMasterPlaylistWithVideo(t *testing.T) {
	t.Parallel()

	store := newStore(t)

	session, err := store.Create("demo", []core.Lang{"it"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The splitter producing the rendition is what turns the video variant on.
	playlist := filepath.Join(session.VideoDir(), "index.m3u8")
	if writeErr := os.WriteFile(playlist, []byte("#EXTM3U\n"), 0o600); writeErr != nil {
		t.Fatalf("simulate splitter output: %v", writeErr)
	}

	master, _ := store.MasterPlaylist("demo")
	text := string(master)

	if !strings.Contains(text, "video/index.m3u8") {
		t.Fatalf("master must reference the video rendition once it exists:\n%s", text)
	}

	if !strings.Contains(text, `AUDIO="dub",SUBTITLES="subs"`) {
		t.Fatalf("video variant must bind the audio and subtitle groups:\n%s", text)
	}
}
