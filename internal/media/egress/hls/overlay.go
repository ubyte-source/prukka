package hls

import (
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ubyte-source/prukka/internal/media/egress/vtt"
)

// cueFileName is the live overlay text consumed by the burn-in push:
// ffmpeg's drawtext filter re-reads it every frame (reload=1).
const cueFileName = "current.txt"

// LiveCue maintains one language's current-cue text file on the wall
// clock, matching the delayed output timeline.
type LiveCue struct {
	log    *slog.Logger
	timers map[int]*time.Timer
	anchor time.Time
	path   string
	gen    int
	closed bool
	mu     sync.Mutex
}

// newLiveCue writes an empty overlay eagerly so a push can attach the
// drawtext filter before the first caption exists.
func newLiveCue(dir string, log *slog.Logger) *LiveCue {
	c := &LiveCue{
		log:    log,
		timers: map[int]*time.Timer{},
		path:   filepath.Join(dir, cueFileName),
		anchor: time.Now(),
	}

	c.write("")

	return c
}

// Schedule shows the cue during its window; later cues own the file, an
// earlier clear never erases newer text.
func (c *LiveCue) Schedule(cue *vtt.Cue) {
	text := strings.Join(cue.Lines, "\n")

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return
	}

	show := time.Until(c.anchor.Add(cue.Start))
	hide := time.Until(c.anchor.Add(cue.End))

	if hide <= 0 {
		return
	}

	c.gen++
	gen := c.gen

	if show <= 0 {
		c.write(text)
	} else {
		c.after(gen*2, show, func() { c.owned(gen, text) })
	}

	c.after(gen*2+1, hide, func() { c.owned(gen, "") })
}

// owned writes on behalf of generation gen; a stale timer must not touch
// the file, so the check and write share the lock.
func (c *LiveCue) owned(gen int, text string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || gen != c.gen {
		return
	}

	c.write(text)
}

// after registers a stoppable timer that removes itself once fired.
func (c *LiveCue) after(id int, d time.Duration, fn func()) {
	c.timers[id] = time.AfterFunc(d, func() {
		fn()

		c.mu.Lock()
		delete(c.timers, id)
		c.mu.Unlock()
	})
}

// Path locates the overlay file for the push's drawtext filter.
func (c *LiveCue) Path() string { return c.path }

// Close stops every pending timer; the session tree removal follows.
func (c *LiveCue) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.closed = true

	for id, timer := range c.timers {
		timer.Stop()
		delete(c.timers, id)
	}
}

// write replaces the overlay atomically (unique temp + rename): drawtext
// must never observe a partial write.
func (c *LiveCue) write(text string) {
	if err := writeAtomic(c.path, []byte(text)); err != nil {
		c.log.Debug("overlay write", "path", c.path, "err", err)
	}
}
