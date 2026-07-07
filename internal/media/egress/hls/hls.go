// Package hls owns the per-session HLS tree: master playlist, subtitle
// renditions and directory layout.
package hls

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ubyte-source/prukka/internal/core"
)

// Tree layout under one session directory.
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
	log      *slog.Logger
	sessions map[string]*Session
	root     string
	mu       sync.Mutex
}

// NewStore wires the store; root is created lazily on the first session.
func NewStore(root string, log *slog.Logger) *Store {
	return &Store{root: root, log: log, sessions: map[string]*Session{}}
}

// Create builds (or rebuilds) one session's directory tree; cheap and
// idempotent.
func (s *Store) Create(slug string, langs []core.Lang) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Join(s.root, slug)

	dirs := make([]string, 0, 1+2*len(langs))
	dirs = append(dirs, filepath.Join(dir, videoDir))
	for _, lang := range langs {
		dirs = append(dirs,
			filepath.Join(dir, audioDir, string(lang)),
			filepath.Join(dir, subsDir, string(lang)))
	}

	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("hls tree for %s: %w", slug, err)
		}
	}

	session := &Session{dir: dir, langs: langs, segmenters: map[core.Lang]*Segmenter{}}
	for _, lang := range langs {
		session.segmenters[lang] = newSegmenter(filepath.Join(dir, subsDir, string(lang)), s.log)
	}

	s.sessions[slug] = session

	return session, nil
}

// MasterPlaylist renders the entry playlist per request; the video variant
// appears only once the splitter produced it.
func (s *Store) MasterPlaylist(slug string) ([]byte, bool) {
	s.mu.Lock()
	session, ok := s.sessions[slug]
	s.mu.Unlock()

	if !ok {
		return nil, false
	}

	return session.master(), true
}

// Drop removes one session's tree and forgets it.
func (s *Store) Drop(slug string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[slug]
	if !ok {
		return
	}

	delete(s.sessions, slug)

	for _, segmenter := range session.segmenters {
		segmenter.cue.Close()
	}

	if err := os.RemoveAll(session.dir); err != nil {
		s.log.Warn("hls tree removal", "session", slug, "err", err)
	}
}

// VideoPlaylist locates one session's live video rendition for an AV push;
// false until the splitter has produced it (audio-only sources never do).
func (s *Store) VideoPlaylist(slug string) (string, bool) {
	s.mu.Lock()
	session, ok := s.sessions[slug]
	s.mu.Unlock()

	if !ok || !session.hasVideo() {
		return "", false
	}

	return filepath.Join(session.VideoDir(), playlist), true
}

// CueFile locates one language's live overlay text for a burn-in push.
func (s *Store) CueFile(slug, lang string) (string, bool) {
	s.mu.Lock()
	session, ok := s.sessions[slug]
	s.mu.Unlock()

	if !ok {
		return "", false
	}

	segmenter, known := session.segmenters[core.Lang(lang)]
	if !known {
		return "", false
	}

	return segmenter.cue.Path(), true
}

// Session is one live session's HLS tree.
type Session struct {
	segmenters map[core.Lang]*Segmenter
	dir        string
	langs      []core.Lang
}

// VideoDir is where the ingest splitter writes the passthrough rendition.
func (s *Session) VideoDir() string {
	return filepath.Join(s.dir, videoDir)
}

// AudioDir is where the encoder writes one language's dubbed rendition.
func (s *Session) AudioDir(lang core.Lang) string {
	return filepath.Join(s.dir, audioDir, string(lang))
}

// Subtitles returns the live subtitle segmenter for one language; it
// consumes translated segments (pipeline.Sink).
func (s *Session) Subtitles(lang core.Lang) *Segmenter {
	return s.segmenters[lang]
}

// master renders the playlist from the current on-disk state.
func (s *Session) master() []byte {
	var b strings.Builder

	b.WriteString("#EXTM3U\n#EXT-X-VERSION:4\n")

	for i, lang := range s.langs {
		tag := string(lang)
		defaultFlag := "NO"

		if i == 0 {
			defaultFlag = "YES"
		}

		fmt.Fprintf(&b,
			"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"dub\",NAME=%q,LANGUAGE=%q,AUTOSELECT=YES,DEFAULT=%s,URI=\"%s/%s/%s\"\n",
			tag, tag, defaultFlag, audioDir, tag, playlist)
		fmt.Fprintf(&b,
			"#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=\"subs\",NAME=%q,LANGUAGE=%q,AUTOSELECT=YES,DEFAULT=NO,URI=\"%s/%s/%s\"\n",
			tag, tag, subsDir, tag, playlist)
	}

	if s.hasVideo() {
		fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,AUDIO=\"dub\",SUBTITLES=\"subs\"\n%s/%s\n",
			nominalBandwidth, videoDir, playlist)

		return []byte(b.String())
	}

	// Audio-only source: the first language's rendition is the variant.
	if len(s.langs) > 0 {
		fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,CODECS=\"mp4a.40.2\",AUDIO=\"dub\",SUBTITLES=\"subs\"\n%s/%s/%s\n",
			nominalBandwidth/16, audioDir, s.langs[0], playlist)
	}

	return []byte(b.String())
}

// hasVideo reports whether the splitter has produced the video rendition.
func (s *Session) hasVideo() bool {
	_, err := os.Stat(filepath.Join(s.VideoDir(), playlist))

	return err == nil
}
