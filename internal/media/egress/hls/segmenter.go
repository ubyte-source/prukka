package hls

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/media/egress/vtt"
)

// Segment geometry: six-second WebVTT parts with a five-part live window,
// matching the video rendition's rolling behavior.
const (
	segmentSeconds = 6
	segmentLength  = segmentSeconds * time.Second
	windowSegments = 5
)

// timestampMap anchors cue clocks to the video's MPEG-TS clock (RFC 8216
// §3.5): ffmpeg starts transport streams at 1.4 s = 126000 ticks.
const timestampMap = "X-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:126000\n"

// Segmenter renders one language's live subtitle rendition and keeps the
// burn-in overlay current; a pipeline.Sink.
type Segmenter struct {
	log  *slog.Logger
	cues map[int][]vtt.Cue
	cue  *LiveCue
	dir  string
	top  int
	mu   sync.Mutex
}

// newSegmenter writes into dir, which must exist.
func newSegmenter(dir string, log *slog.Logger) *Segmenter {
	return &Segmenter{
		dir:  dir,
		log:  log,
		cues: map[int][]vtt.Cue{},
		cue:  newLiveCue(dir, log),
		top:  -1,
	}
}

// Append implements pipeline.Sink: cues land in every part they overlap
// (RFC 8216).
func (s *Segmenter) Append(seg *core.TranslatedSegment) {
	text := strings.TrimSpace(seg.Text)
	if text == "" {
		return
	}

	dur := seg.Duration
	if dur <= 0 {
		dur = time.Second
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	touched := map[int]bool{}

	for _, cue := range vtt.Layout(text, seg.ScheduleAt, dur) {
		s.cue.Schedule(&cue)
		first := int(cue.Start / segmentLength)
		last := int((cue.End - time.Millisecond) / segmentLength)

		for idx := first; idx <= last; idx++ {
			s.cues[idx] = append(s.cues[idx], cue)
			touched[idx] = true

			if idx > s.top {
				s.top = idx
			}
		}
	}

	for idx := range touched {
		s.writePart(idx)
	}

	s.writePlaylist()
	s.evict()
}

// writePart renders one WebVTT part file.
func (s *Segmenter) writePart(idx int) {
	var b strings.Builder

	b.WriteString("WEBVTT\n")
	b.WriteString(timestampMap)

	for i := range s.cues[idx] {
		cue := &s.cues[idx][i]

		b.WriteString("\n")
		b.WriteString(vtt.Timestamp(cue.Start))
		b.WriteString(" --> ")
		b.WriteString(vtt.Timestamp(cue.End))
		b.WriteString("\n")
		b.WriteString(strings.Join(cue.Lines, "\n"))
		b.WriteString("\n")
	}

	if err := os.WriteFile(s.partPath(idx), []byte(b.String()), 0o600); err != nil {
		s.log.Warn("subtitle part write", "part", idx, "err", err)
	}
}

// writePlaylist renders the live window; cueless parts still get an empty
// file so players never 404 mid-window.
func (s *Segmenter) writePlaylist() {
	first := s.top - windowSegments + 1
	if first < 0 {
		first = 0
	}

	var b strings.Builder

	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:4\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:%d\n",
		segmentSeconds, first)

	for idx := first; idx <= s.top; idx++ {
		if _, ok := s.cues[idx]; !ok {
			s.writePart(idx)
		}

		fmt.Fprintf(&b, "#EXTINF:%d.0,\n%s\n", segmentSeconds, partName(idx))
	}

	if err := os.WriteFile(filepath.Join(s.dir, playlist), []byte(b.String()), 0o600); err != nil {
		s.log.Warn("subtitle playlist write", "err", err)
	}
}

// evict drops parts that left the live window, mirroring delete_segments.
func (s *Segmenter) evict() {
	first := s.top - windowSegments + 1

	for idx := range s.cues {
		if idx < first {
			delete(s.cues, idx)

			if err := os.Remove(s.partPath(idx)); err != nil && !os.IsNotExist(err) {
				s.log.Warn("subtitle part removal", "part", idx, "err", err)
			}
		}
	}
}

// partPath is the on-disk location of one part.
func (s *Segmenter) partPath(idx int) string {
	return filepath.Join(s.dir, partName(idx))
}

// partName mirrors the video rendition's numbering scheme.
func partName(idx int) string {
	return fmt.Sprintf("seg%05d.vtt", idx)
}
