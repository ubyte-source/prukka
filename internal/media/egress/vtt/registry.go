package vtt

import (
	"sync"

	"github.com/ubyte-source/prukka/internal/core"
)

// pairID identifies one session's document in one language.
type pairID struct {
	slug string
	lang core.Lang
}

// Registry tracks live writers by session and language; safe for
// concurrent use.
type Registry struct {
	writers map[pairID]*Writer
	mu      sync.RWMutex
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{writers: map[pairID]*Writer{}}
}

// Create registers and returns the writer for one session and language,
// replacing any previous one (a lane restart starts a fresh document).
func (r *Registry) Create(slug string, lang core.Lang) *Writer {
	w := NewWriter()

	r.mu.Lock()
	defer r.mu.Unlock()

	r.writers[pairID{slug: slug, lang: lang}] = w

	return w
}

// Document renders the current caption document, reporting whether the
// session and language pair exists.
func (r *Registry) Document(slug, lang string) ([]byte, bool) {
	r.mu.RLock()
	w, ok := r.writers[pairID{slug: slug, lang: core.Lang(lang)}]
	r.mu.RUnlock()

	if !ok {
		return nil, false
	}

	return w.Document(), true
}

// Drop removes every writer of one session.
func (r *Registry) Drop(slug string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for key := range r.writers {
		if key.slug == slug {
			delete(r.writers, key)
		}
	}
}
