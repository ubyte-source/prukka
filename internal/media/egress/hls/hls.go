// Package hls owns the per-session HLS tree: master playlist, subtitle
// renditions and directory layout.
package hls

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/ubyte-source/prukka/internal/core"
)

// Directory and playlist names under one session's tree.
const (
	videoDir = "video"
	audioDir = "audio"
	subsDir  = "subs"
	playlist = "index.m3u8"
)

// nominalBandwidth seeds player ABR; the passthrough video's real rate is
// the source's.
const nominalBandwidth = 2_500_000

// Store owns the session trees under one root directory. It is safe for
// concurrent use.
type Store struct {
	log   *slog.Logger
	trees map[string]*Tree
	root  string
	mu    sync.Mutex
}

// NewStore wires the store; root is created lazily on the first session.
func NewStore(root string, log *slog.Logger) *Store {
	return &Store{root: root, log: log, trees: map[string]*Tree{}}
}

// Create builds (or rebuilds) one session's directory tree; cheap and
// idempotent.
func (s *Store) Create(slug string, langs []core.Lang) (*Tree, error) {
	return s.CreateWithSubtitles(slug, langs, langs)
}

// CreateWithSubtitles rebuilds a session tree while restricting subtitle
// outputs to the supplied language subset; every target keeps its audio dir.
func (s *Store) CreateWithSubtitles(
	slug string, langs, subtitleLangs []core.Lang,
) (*Tree, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Join(s.root, slug)
	if previous, ok := s.trees[slug]; ok {
		closeTree(previous)
		delete(s.trees, slug)
	}
	if err := os.RemoveAll(dir); err != nil {
		return nil, fmt.Errorf("reset hls tree for %s: %w", slug, err)
	}

	dirs := make([]string, 0, 1+len(langs)+len(subtitleLangs))
	dirs = append(dirs, filepath.Join(dir, videoDir))
	for _, lang := range langs {
		dirs = append(dirs, filepath.Join(dir, audioDir, string(lang)))
	}
	for _, lang := range subtitleLangs {
		dirs = append(dirs, filepath.Join(dir, subsDir, string(lang)))
	}

	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, errors.Join(fmt.Errorf("hls tree for %s: %w", slug, err), os.RemoveAll(dir))
		}
	}

	ownedLangs := slices.Clone(langs)
	ownedSubtitles := slices.Clone(subtitleLangs)
	tree := &Tree{
		dir:           dir,
		langs:         ownedLangs,
		subtitleLangs: ownedSubtitles,
		segmenters:    map[core.Lang]*Segmenter{},
	}
	for _, lang := range ownedSubtitles {
		tree.segmenters[lang] = newSegmenter(filepath.Join(dir, subsDir, string(lang)), s.log)
	}

	s.trees[slug] = tree

	return tree, nil
}

// MasterPlaylist renders the entry playlist per request; the video variant
// appears only once the splitter produced it.
func (s *Store) MasterPlaylist(slug string) ([]byte, bool) {
	s.mu.Lock()
	tree, ok := s.trees[slug]
	s.mu.Unlock()

	if !ok {
		return nil, false
	}

	return tree.master(), true
}

// Drop removes one session's tree and forgets it.
func (s *Store) Drop(slug string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tree, ok := s.trees[slug]
	if !ok {
		return
	}

	delete(s.trees, slug)
	closeTree(tree)

	if err := os.RemoveAll(tree.dir); err != nil {
		s.log.Warn("hls tree removal", "session", slug, "err", err)
	}
}

func closeTree(tree *Tree) {
	for _, segmenter := range tree.segmenters {
		segmenter.cue.Close()
	}
}

// VideoPlaylist locates one session's live video rendition for an AV push;
// false until the splitter has produced it (audio-only sources never do).
func (s *Store) VideoPlaylist(slug string) (string, bool) {
	s.mu.Lock()
	tree, ok := s.trees[slug]
	s.mu.Unlock()

	if !ok || !tree.hasVideo() {
		return "", false
	}

	return filepath.Join(tree.VideoDir(), playlist), true
}

// CueFile locates one language's live overlay text for a burn-in push.
func (s *Store) CueFile(slug, lang string) (string, bool) {
	s.mu.Lock()
	tree, ok := s.trees[slug]
	s.mu.Unlock()

	if !ok {
		return "", false
	}

	segmenter, known := tree.segmenters[core.Lang(lang)]
	if !known {
		return "", false
	}

	return segmenter.cue.Path(), true
}

// Tree is one live session's on-disk output: playlists and segment dirs.
type Tree struct {
	segmenters    map[core.Lang]*Segmenter
	dir           string
	langs         []core.Lang
	subtitleLangs []core.Lang
}

// VideoDir is where the ingest splitter writes the passthrough rendition.
func (t *Tree) VideoDir() string {
	return filepath.Join(t.dir, videoDir)
}

// AudioDir is where the encoder writes one language's dubbed rendition.
func (t *Tree) AudioDir(lang core.Lang) string {
	return filepath.Join(t.dir, audioDir, string(lang))
}

// Subtitles returns the live subtitle segmenter for one language; it
// consumes translated segments (pipeline.Sink).
func (t *Tree) Subtitles(lang core.Lang) *Segmenter {
	return t.segmenters[lang]
}

// master renders the playlist from the current on-disk state.
func (t *Tree) master() []byte {
	var b strings.Builder

	b.WriteString("#EXTM3U\n#EXT-X-VERSION:4\n")

	audio := t.audioLanguages()
	for i, lang := range audio {
		tag := string(lang)
		defaultFlag := "NO"

		if i == 0 {
			defaultFlag = "YES"
		}

		fmt.Fprintf(&b,
			"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"dub\",NAME=%q,LANGUAGE=%q,AUTOSELECT=YES,DEFAULT=%s,URI=\"%s/%s/%s\"\n",
			tag, tag, defaultFlag, audioDir, tag, playlist)
	}

	for _, lang := range t.subtitleLangs {
		tag := string(lang)
		fmt.Fprintf(&b,
			"#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=\"subs\",NAME=%q,LANGUAGE=%q,AUTOSELECT=YES,DEFAULT=NO,URI=\"%s/%s/%s\"\n",
			tag, tag, subsDir, tag, playlist)
	}

	if t.hasVideo() {
		groups := t.variantGroups(audio)
		fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d%s\n%s/%s\n",
			nominalBandwidth, groups, videoDir, playlist)

		return []byte(b.String())
	}

	// Audio-only source: the first available dub is the variant.
	if len(audio) > 0 {
		fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,CODECS=\"mp4a.40.2\",AUDIO=\"dub\"%s\n%s/%s/%s\n",
			nominalBandwidth/16, t.subtitleGroup(), audioDir, audio[0], playlist)
	}

	return []byte(b.String())
}

func (t *Tree) variantGroups(audio []core.Lang) string {
	groups := ""
	if len(audio) > 0 {
		groups += ",AUDIO=\"dub\""
	}

	return groups + t.subtitleGroup()
}

func (t *Tree) subtitleGroup() string {
	if len(t.subtitleLangs) == 0 {
		return ""
	}

	return ",SUBTITLES=\"subs\""
}

func (t *Tree) audioLanguages() []core.Lang {
	out := make([]core.Lang, 0, len(t.langs))
	for _, lang := range t.langs {
		if _, err := os.Stat(filepath.Join(t.AudioDir(lang), playlist)); err == nil {
			out = append(out, lang)
		}
	}

	return out
}

// hasVideo reports whether the splitter has produced the video rendition.
func (t *Tree) hasVideo() bool {
	_, err := os.Stat(filepath.Join(t.VideoDir(), playlist))

	return err == nil
}
