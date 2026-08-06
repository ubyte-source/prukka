package hls_test

import (
	"errors"
	"io/fs"
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

	tree, err := store.Create("demo", []core.Lang{"it", "en"})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	for _, dir := range []string{
		tree.VideoDir(),
		tree.AudioDir("it"),
		tree.AudioDir("en"),
	} {
		if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
			t.Fatalf("missing tree directory %s: %v", dir, statErr)
		}
	}

	if tree.Subtitles("en") == nil {
		t.Fatal("no subtitle segmenter for a created language")
	}
}

func TestCreateIsIdempotentForLanguageUpdates(t *testing.T) {
	t.Parallel()

	store := newStore(t)

	first, err := store.Create("demo", []core.Lang{"it"})
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	stale := filepath.Join(first.VideoDir(), "index.m3u8")
	if writeErr := os.WriteFile(stale, []byte("stale"), 0o600); writeErr != nil {
		t.Fatalf("seed stale playlist: %v", writeErr)
	}

	tree, err := store.Create("demo", []core.Lang{"it", "fr"})
	if err != nil {
		t.Fatalf("recreate with new language: %v", err)
	}

	if tree.Subtitles("fr") == nil {
		t.Fatal("recreate did not pick up the added language")
	}
	if _, err := os.Stat(stale); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("recreate retained a stale playlist: %v", err)
	}
}

func TestCreateOwnsItsLanguageList(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	langs := []core.Lang{"it"}
	tree, err := store.Create("demo", langs)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	langs[0] = "de"

	if tree.Subtitles("it") == nil || tree.Subtitles("de") != nil {
		t.Fatal("caller mutation changed the session language registry")
	}
}

func TestDropRemovesTheTree(t *testing.T) {
	t.Parallel()

	store := newStore(t)

	if _, ok := store.MasterPlaylist("ghost"); ok {
		t.Fatal("unknown session reported a master playlist")
	}

	tree, err := store.Create("demo", []core.Lang{"it"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	store.Drop("demo")

	if _, ok := store.MasterPlaylist("demo"); ok {
		t.Fatal("dropped session still reports a master playlist")
	}

	if _, statErr := os.Stat(tree.VideoDir()); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("dropped tree still on disk: %v", statErr)
	}
}

func TestMasterPlaylistAudioOnly(t *testing.T) {
	t.Parallel()

	store := newStore(t)

	tree, err := store.Create("demo", []core.Lang{"it", "en"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, lang := range []core.Lang{"it", "en"} {
		path := filepath.Join(tree.AudioDir(lang), "index.m3u8")
		if writeErr := os.WriteFile(path, []byte("#EXTM3U\n"), 0o600); writeErr != nil {
			t.Fatalf("simulate audio rendition: %v", writeErr)
		}
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

func TestMasterPlaylistOmitsUnavailableAudio(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	tree, err := store.Create("captions", []core.Lang{"it", "de"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	video := filepath.Join(tree.VideoDir(), "index.m3u8")
	if writeErr := os.WriteFile(video, []byte("#EXTM3U\n"), 0o600); writeErr != nil {
		t.Fatalf("simulate video rendition: %v", writeErr)
	}

	master, _ := store.MasterPlaylist("captions")
	text := string(master)
	if strings.Contains(text, "TYPE=AUDIO") || strings.Contains(text, `AUDIO="dub"`) {
		t.Fatalf("master advertises unavailable dubbed audio:\n%s", text)
	}
	if !strings.Contains(text, "subs/de/index.m3u8") || !strings.Contains(text, `SUBTITLES="subs"`) {
		t.Fatalf("caption-only master lost its subtitles:\n%s", text)
	}
}

func TestCreateWithoutSubtitlesPublishesNoSubtitleRendition(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	tree, err := store.CreateWithSubtitles("dub-only", []core.Lang{"it"}, nil)
	if err != nil {
		t.Fatalf("CreateWithSubtitles: %v", err)
	}
	if tree.Subtitles("it") != nil {
		t.Fatal("subtitle-disabled session created a segmenter")
	}
	if err := os.WriteFile(filepath.Join(tree.AudioDir("it"), "index.m3u8"), []byte("#EXTM3U\n"), 0o600); err != nil {
		t.Fatalf("simulate audio rendition: %v", err)
	}

	master, ok := store.MasterPlaylist("dub-only")
	if !ok {
		t.Fatal("MasterPlaylist reported unknown session")
	}
	if text := string(master); strings.Contains(text, "SUBTITLES") || strings.Contains(text, "TYPE=SUBTITLES") {
		t.Fatalf("subtitle-disabled master advertises subtitles:\n%s", text)
	}
	if _, ok := store.Open("dub-only", "subs/it/index.m3u8"); ok {
		t.Fatal("subtitle-disabled session authorized a subtitle path")
	}
}

func TestMasterPlaylistSupportsMixedDubAndCaptionLanguages(t *testing.T) {
	t.Parallel()

	store := newStore(t)
	tree, err := store.Create("mixed", []core.Lang{"it", "de"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	video := filepath.Join(tree.VideoDir(), "index.m3u8")
	if err = os.WriteFile(video, []byte("#EXTM3U\n"), 0o600); err != nil {
		t.Fatalf("simulate video rendition: %v", err)
	}
	audio := filepath.Join(tree.AudioDir("it"), "index.m3u8")
	if err = os.WriteFile(audio, []byte("#EXTM3U\n"), 0o600); err != nil {
		t.Fatalf("simulate Italian dub: %v", err)
	}

	master, _ := store.MasterPlaylist("mixed")
	text := string(master)
	if !strings.Contains(text, `TYPE=AUDIO,GROUP-ID="dub",NAME="it"`) {
		t.Fatalf("master lost available Italian audio:\n%s", text)
	}
	if strings.Contains(text, `TYPE=AUDIO,GROUP-ID="dub",NAME="de"`) {
		t.Fatalf("master advertises caption-only German as audio:\n%s", text)
	}
	if !strings.Contains(text, "subs/de/index.m3u8") {
		t.Fatalf("master lost German captions:\n%s", text)
	}
}

func TestMasterPlaylistWithVideo(t *testing.T) {
	t.Parallel()

	store := newStore(t)

	tree, err := store.Create("demo", []core.Lang{"it"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The splitter producing the rendition is what turns the video variant on.
	playlist := filepath.Join(tree.VideoDir(), "index.m3u8")
	if writeErr := os.WriteFile(playlist, []byte("#EXTM3U\n"), 0o600); writeErr != nil {
		t.Fatalf("simulate splitter output: %v", writeErr)
	}
	audio := filepath.Join(tree.AudioDir("it"), "index.m3u8")
	if writeErr := os.WriteFile(audio, []byte("#EXTM3U\n"), 0o600); writeErr != nil {
		t.Fatalf("simulate audio rendition: %v", writeErr)
	}

	master, _ := store.MasterPlaylist("demo")
	text := string(master)

	if !strings.Contains(text, "video/index.m3u8") {
		t.Fatalf("master must reference the video rendition once it exists:\n%s", text)
	}

	if !strings.Contains(text, `AUDIO="dub",SUBTITLES="subs"`) {
		t.Fatalf("video variant must bind its available media groups:\n%s", text)
	}
}
