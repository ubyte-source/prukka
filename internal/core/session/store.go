package session

import (
	"context"
	"errors"
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
	EventStatus  EventType = "status"
)

// Event notifies subscribers of one session change; each subscriber owns its
// embedded Session.
type Event struct {
	Type    EventType
	Session Session
}

// subscriberBuffer bounds each subscriber channel.
const subscriberBuffer = 16

// Store is the in-memory session registry behind the control plane; it is safe
// for concurrent use and every Session leaving it is a deep copy.
type Store struct {
	sessions map[string]Session
	subs     map[int]chan Event
	mu       sync.RWMutex
	nextRev  revision
	nextSub  int
	max      int
}

// StoreOption configures construction-time registry limits.
type StoreOption func(*Store)

// WithMaxSessions caps registered definitions, whatever their runtime state.
func WithMaxSessions(maxSessions int) StoreOption {
	if maxSessions < 1 {
		panic("session store max sessions must be positive")
	}

	return func(store *Store) { store.max = maxSessions }
}

// NewStore returns an empty registry.
func NewStore(options ...StoreOption) *Store {
	store := &Store{
		sessions: map[string]Session{},
		subs:     map[int]chan Event{},
	}
	for _, option := range options {
		option(store)
	}

	return store
}

// Create validates and stores a deep copy of a new session, notifying
// subscribers.
func (st *Store) Create(s *Session) error {
	if err := s.validate(); err != nil {
		return err
	}

	st.mu.Lock()
	defer st.mu.Unlock()

	if _, ok := st.sessions[s.Slug]; ok {
		return fmt.Errorf("%w: %q", ErrExists, s.Slug)
	}
	if st.max > 0 && len(st.sessions) >= st.max {
		return fmt.Errorf("%w: maximum %d", ErrCapacity, st.max)
	}

	st.nextRev++
	stored := clone(s)
	stored.revision = st.nextRev
	stored.runtime = RuntimeStatus{State: StateStarting}
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
	return st.updateLangs(slug, add, remove, nil)
}

// UpdateLangsChecked applies a caller-owned policy check to the candidate
// under the store lock; a rejected candidate changes nothing.
func (st *Store) UpdateLangsChecked(
	slug string, add, remove []core.Lang, check func(Session) error,
) (Session, error) {
	if check == nil {
		return Session{}, errors.New("session update check is nil")
	}

	return st.updateLangs(slug, add, remove, check)
}

func (st *Store) updateLangs(
	slug string, add, remove []core.Lang, check func(Session) error,
) (Session, error) {
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
	s.DubLangs = s.DubLangs.retain(merged)
	if err := s.validate(); err != nil {
		return Session{}, err
	}
	if check != nil {
		if err := check(clone(&s)); err != nil {
			return Session{}, err
		}
	}
	st.nextRev++
	s.revision = st.nextRev
	s.runtime = RuntimeStatus{State: StateStarting}
	st.sessions[slug] = s
	st.notify(&Event{Type: EventUpdated, Session: s})

	return clone(&s), nil
}

// bindRuntime makes one incarnation the sole writer of runtime state for its
// definition, emitting no event when visible state is already starting.
func (st *Store) bindRuntime(id laneID) bool {
	st.mu.Lock()
	defer st.mu.Unlock()

	s, ok := st.sessions[id.slug]
	if !ok || s.revision != id.revision || id.gen <= s.runtime.gen {
		return false
	}

	notify := s.runtime.State != StateStarting || s.runtime.Error != ""
	s.runtime = RuntimeStatus{State: StateStarting, gen: id.gen}
	st.sessions[id.slug] = s
	if notify {
		st.notify(&Event{Type: EventStatus, Session: s})
	}

	return true
}

// setRuntime updates observed state only for the bound incarnation, so a stale
// lane cannot overwrite its replacement.
func (st *Store) setRuntime(id laneID, state RuntimeState, laneErr error) bool {
	st.mu.Lock()
	defer st.mu.Unlock()

	s, ok := st.sessions[id.slug]
	if !ok || s.revision != id.revision || s.runtime.gen != id.gen ||
		!validRuntimeTransition(s.runtime.State, state) {
		return false
	}

	s.runtime.State = state
	s.runtime.Error = sanitizeRuntimeError(laneErr)
	st.sessions[id.slug] = s
	st.notify(&Event{Type: EventStatus, Session: s})

	return true
}

// Delete removes a session and, when the two name each other in Pair, the
// reciprocal one too, so one Delete may free two capacity slots.
func (st *Store) Delete(slug string) error {
	st.mu.Lock()
	defer st.mu.Unlock()

	s, ok := st.sessions[slug]
	if !ok {
		return fmt.Errorf("%w: %q", ErrNotFound, slug)
	}

	delete(st.sessions, slug)
	st.notify(&Event{Type: EventDeleted, Session: s})

	if paired, pairedOK := st.sessions[s.Pair]; pairedOK && paired.Pair == slug {
		delete(st.sessions, s.Pair)
		st.notify(&Event{Type: EventDeleted, Session: paired})
	}

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

	// Unregistering under notify's own lock is what keeps a send off the close.
	go func() {
		<-ctx.Done()

		st.mu.Lock()
		delete(st.subs, id)
		st.mu.Unlock()

		close(ch)
	}()

	return ch
}

// notify fans an event out to all subscribers; it must run with st.mu held.
func (st *Store) notify(e *Event) {
	for _, ch := range st.subs {
		event := Event{Type: e.Type, Session: clone(&e.Session)}
		select {
		case ch <- event:
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
