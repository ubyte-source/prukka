package session

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/lang"
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

// Event notifies subscribers of one session change. Each subscriber owns its
// embedded Session.
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
	nextRev  uint64
	nextSub  int
	max      int
}

// StoreOption configures construction-time registry limits.
type StoreOption func(*Store)

// WithMaxSessions caps registered definitions, including starting, failed and
// finished sessions. A session must be deleted before its slot is reusable.
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

// UpdateLangsChecked applies a final, caller-owned policy check to a deep
// copy of the candidate while holding the store lock. A rejected candidate
// does not change the definition, revision, runtime state or event stream.
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
	s.Flags = retainDubLanguages(s.Flags, merged)
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

// bindRuntime makes generation the sole writer of runtime state for this
// definition. It intentionally emits no event when visible state is already
// starting; the preceding create or update event carried that state.
func (st *Store) bindRuntime(slug string, revision, generation uint64) bool {
	st.mu.Lock()
	defer st.mu.Unlock()

	s, ok := st.sessions[slug]
	if !ok || s.revision != revision || generation <= s.runtime.gen {
		return false
	}

	notify := s.runtime.State != StateStarting || s.runtime.Error != ""
	s.runtime = RuntimeStatus{State: StateStarting, gen: generation}
	st.sessions[slug] = s
	if notify {
		st.notify(&Event{Type: EventStatus, Session: s})
	}

	return true
}

// setRuntime updates observed state only for the bound definition and
// generation. A stale lane therefore cannot overwrite its replacement.
func (st *Store) setRuntime(
	slug string, revision, generation uint64, state RuntimeState, laneErr error, sources ...string,
) bool {
	st.mu.Lock()
	defer st.mu.Unlock()

	s, ok := st.sessions[slug]
	if !ok || s.revision != revision || s.runtime.gen != generation ||
		!validRuntimeTransition(s.runtime.State, state) {
		return false
	}

	s.runtime.State = state
	s.runtime.Error = sanitizeRuntimeError(laneErr, sources...)
	st.sessions[slug] = s
	st.notify(&Event{Type: EventStatus, Session: s})

	return true
}

func retainDubLanguages(flags map[string]string, langs []core.Lang) map[string]string {
	raw, configured := flags["dub_langs"]
	if !configured {
		return flags
	}

	selected := make(map[core.Lang]bool)
	for value := range strings.SplitSeq(raw, ",") {
		// Parse mirrors validation: retention must canonicalize ("IT",
		// "en_us") exactly like the check that admitted the flag.
		if target, parseErr := lang.Parse(strings.TrimSpace(value)); parseErr == nil {
			selected[target] = true
		}
	}
	retained := make([]string, 0, len(selected))
	for _, target := range langs {
		if selected[target] {
			retained = append(retained, string(target))
		}
	}

	out := maps.Clone(flags)
	out["dub_langs"] = strings.Join(retained, ",")

	return out
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

	pairedSlug := s.Flags["pair"]
	if paired, pairedOK := st.sessions[pairedSlug]; pairedOK && paired.Flags["pair"] == slug {
		delete(st.sessions, pairedSlug)
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
