package hls

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Segment filename shapes; the numbering mirrors ffmpeg's seg%05d scheme.
const (
	segTS  = "seg%05d.ts"
	segVTT = "seg%05d.vtt"
)

// langUnknown marks a language the session does not carry.
const langUnknown = "\x00"

// Open serves a request path inside one session's tree; the on-disk path
// is rebuilt from store-owned parts, so request data never names a file.
func (s *Store) Open(slug, rest string) (io.ReadSeekCloser, bool) {
	s.mu.Lock()
	session, ok := s.sessions[slug]
	s.mu.Unlock()

	if !ok {
		return nil, false
	}

	parts := strings.Split(rest, "/")

	var (
		path  string
		valid bool
	)

	switch {
	case len(parts) == 2 && parts[0] == videoDir:
		path, valid = rebuild(session.dir, videoDir, "", parts[1], segTS)
	case len(parts) == 3 && parts[0] == audioDir:
		path, valid = rebuild(session.dir, audioDir, session.owned(parts[1]), parts[2], segTS)
	case len(parts) == 3 && parts[0] == subsDir:
		path, valid = rebuild(session.dir, subsDir, session.ownedSubtitle(parts[1]), parts[2], segVTT)
	}

	if !valid {
		return nil, false
	}

	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, false
	}

	return f, true
}

func (s *Session) ownedSubtitle(tag string) string {
	for _, lang := range s.subtitleLangs {
		if string(lang) == tag {
			return string(lang)
		}
	}

	return langUnknown
}

// rebuild reconstructs one rendition path from owned parts; the unknown
// marker is rejected.
func rebuild(dir, kind, lang, file, segPattern string) (path string, ok bool) {
	if lang == langUnknown {
		return "", false
	}

	base := filepath.Join(dir, kind)
	if lang != "" {
		base = filepath.Join(base, lang)
	}

	if file == playlist {
		return filepath.Join(base, playlist), true
	}

	// A segment name round-trips through its number: parse, then re-render
	// and require an exact match, so only canonical names resolve.
	var n int
	if _, err := fmt.Sscanf(file, segPattern, &n); err != nil || n < 0 || fmt.Sprintf(segPattern, n) != file {
		return "", false
	}

	return filepath.Join(base, fmt.Sprintf(segPattern, n)), true
}

// owned returns the session's own copy of a requested language tag, or the
// unknown marker.
func (s *Session) owned(tag string) string {
	for _, lang := range s.langs {
		if string(lang) == tag {
			return string(lang)
		}
	}

	return langUnknown
}
