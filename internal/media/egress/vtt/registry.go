package vtt

import (
	"sync"

	"github.com/ubyte-source/prukka/internal/core"
)

// Registry tracks live writers by session and language; safe for
// concurrent use.
type Registry struct {
	writers map[string]*Writer
	mu      sync.RWMutex
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{writers: map[string]*Writer{}}
}

// key builds the lookup key; the slug is DNS-label shaped and the tag is
// registry-validated, so "/" cannot collide.
func key(session, lang string) string {
	return session + "/" + lang
}

// Create registers and returns the writer for one session and language,
// replacing any previous one (a lane restart starts a fresh document).
func (r *Registry) Create(session string, lang core.Lang) *Writer {
	w := NewWriter()

	r.mu.Lock()
	defer r.mu.Unlock()

	r.writers[key(session, string(lang))] = w

	return w
}

// Document renders the current caption document, reporting whether the
// session and language pair exists.
func (r *Registry) Document(session, lang string) ([]byte, bool) {
	r.mu.RLock()
	w, ok := r.writers[key(session, lang)]
	r.mu.RUnlock()

	if !ok {
		return nil, false
	}

	return w.Document(), true
}

// Drop removes every writer of one session.
func (r *Registry) Drop(session string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	prefix := session + "/"
	for k := range r.writers {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			delete(r.writers, k)
		}
	}
}
