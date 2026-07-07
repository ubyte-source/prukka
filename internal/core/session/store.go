package session

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/ubyte-source/prukka/internal/core"
)

// EventType classifies a session change notification.
type EventType string

// Event types emitted by the store.
const (
	EventCreated EventType = "created"
	EventUpdated EventType = "updated"
	EventDeleted EventType = "deleted"
)

// Event notifies subscribers of one session change. Subscribers must treat
// the embedded Session as read-only: one copy fans out to every subscriber.
type Event struct {
	Type    EventType
	Session Session
}

// subscriberBuffer bounds each subscriber channel; see Subscribe for the
// overflow semantics.
const subscriberBuffer = 16

// Store is the in-memory session registry behind the control plane. It is
// safe for concurrent use, and every Session leaving it is a deep copy.
type Store struct {
	sessions map[string]Session
	subs     map[int]chan Event
	mu       sync.RWMutex
	nextSub  int
}

// NewStore returns an empty registry.
func NewStore() *Store {
	return &Store{
		sessions: map[string]Session{},
		subs:     map[int]chan Event{},
	}
}

// Create validates and stores a new session, notifying subscribers. The
// store keeps a deep copy; the caller retains ownership of s.
func (st *Store) Create(s *Session) error {
	if err := s.validate(); err != nil {
		return err
	}

	st.mu.Lock()
	defer st.mu.Unlock()

	if _, ok := st.sessions[s.Slug]; ok {
		return fmt.Errorf("%w: %q", ErrExists, s.Slug)
	}

	stored := clone(s)
	st.sessions[s.Slug] = stored
	st.notify(&Event{Type: EventCreated, Session: stored})

	return nil
}

// Get returns a copy of the session with the given slug.
func (st *Store) Get(slug string) (Session, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()

	s, ok := st.sessions[slug]
	if !ok {
		return Session{}, fmt.Errorf("%w: %q", ErrNotFound, slug)
	}

	return clone(&s), nil
}

// List returns copies of all sessions, ordered by slug.
func (st *Store) List() []Session {
	st.mu.RLock()
	defer st.mu.RUnlock()

	slugs := slices.Sorted(maps.Keys(st.sessions))

	out := make([]Session, 0, len(slugs))
	for _, slug := range slugs {
		s := st.sessions[slug]
		out = append(out, clone(&s))
	}

	return out
}

// Count returns the number of stored sessions.
func (st *Store) Count() int {
	st.mu.RLock()
	defer st.mu.RUnlock()

	return len(st.sessions)
}

// UpdateLangs hot-adds and hot-removes target languages; removing every
// language fails with ErrNoLanguages.
func (st *Store) UpdateLangs(slug string, add, remove []core.Lang) (Session, error) {
	st.mu.Lock()
	defer st.mu.Unlock()

	s, ok := st.sessions[slug]
	if !ok {
		return Session{}, fmt.Errorf("%w: %q", ErrNotFound, slug)
	}

	merged := mergeLangs(s.Langs, add, remove)
	if len(merged) == 0 {
		return Session{}, ErrNoLanguages
	}

	s.Langs = merged
	st.sessions[slug] = s
	st.notify(&Event{Type: EventUpdated, Session: s})

	return clone(&s), nil
}

// Delete removes a session and notifies subscribers.
func (st *Store) Delete(slug string) error {
	st.mu.Lock()
	defer st.mu.Unlock()

	s, ok := st.sessions[slug]
	if !ok {
		return fmt.Errorf("%w: %q", ErrNotFound, slug)
	}

	delete(st.sessions, slug)
	st.notify(&Event{Type: EventDeleted, Session: s})

	return nil
}

// Subscribe registers for change events until ctx ends; events beyond a
// slow subscriber's buffer are dropped — re-List to resynchronize.
func (st *Store) Subscribe(ctx context.Context) <-chan Event {
	ch := make(chan Event, subscriberBuffer)

	st.mu.Lock()
	id := st.nextSub
	st.nextSub++
	st.subs[id] = ch
	st.mu.Unlock()

	// Unregistering under the same lock used by notify guarantees no send
	// can race the close.
	go func() {
		<-ctx.Done()

		st.mu.Lock()
		delete(st.subs, id)
		st.mu.Unlock()

		close(ch)
	}()

	return ch
}

// notify fans an event out to all subscribers. It must run with st.mu held.
func (st *Store) notify(e *Event) {
	for _, ch := range st.subs {
		select {
		case ch <- *e:
		default: // subscriber buffer full: drop, per Subscribe's contract
		}
	}
}

// mergeLangs appends additions not already present and filters removals,
// preserving the order languages were first enabled in.
func mergeLangs(current, add, remove []core.Lang) []core.Lang {
	drop := make(map[core.Lang]bool, len(remove))
	for _, l := range remove {
		drop[l] = true
	}

	out := make([]core.Lang, 0, len(current)+len(add))
	seen := make(map[core.Lang]bool, len(current)+len(add))

	for _, l := range slices.Concat(current, add) {
		if drop[l] || seen[l] {
			continue
		}

		seen[l] = true

		out = append(out, l)
	}

	return out
}
