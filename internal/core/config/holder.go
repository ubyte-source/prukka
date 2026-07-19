package config

import (
	"sync"
	"sync/atomic"
)

// Holder serves the live configuration and swaps it atomically on Reload
// or Update; snapshots are immutable by convention.
type Holder struct {
	current atomic.Pointer[Config]
	path    string
	// edit serializes the writers so concurrent swaps cannot publish the
	// older snapshot; reads stay lock-free.
	edit sync.Mutex
}

// NewHolder loads the initial configuration from path (empty selects the
// platform default location).
func NewHolder(path string) (*Holder, error) {
	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}

	h := &Holder{path: path}
	h.current.Store(cfg)

	return h, nil
}

// Current returns the live snapshot.
func (h *Holder) Current() *Config {
	return h.current.Load()
}

// Transition is one serialized snapshot swap: the ordered before/after pair
// plus the structural notes. Observers classify their OWN transition instead
// of re-reading Current, which under concurrent writers may already carry a
// LATER snapshot — comparing against that would silently swallow this edit.
type Transition struct {
	Previous *Config
	Current  *Config
	Notes    []string
}

// Reload re-reads the file and swaps the snapshot, serialized with Update;
// a failed load keeps the previous config. The returned transition names
// structural changes and carries the ordered snapshot pair.
func (h *Holder) Reload() (Transition, error) {
	h.edit.Lock()
	defer h.edit.Unlock()

	return h.reloadLocked()
}

// reloadLocked is Reload's body; the caller holds edit.
func (h *Holder) reloadLocked() (Transition, error) {
	fresh, err := Load(h.path)
	if err != nil {
		return Transition{}, err
	}

	previous := h.Current()
	h.current.Store(fresh)

	return Transition{
		Previous: previous,
		Current:  fresh,
		Notes:    structuralChanges(previous, fresh),
	}, nil
}

// Update edits the config in one serialized transaction: re-read the file
// layer, mutate, validate, write atomically, swap live.
func (h *Holder) Update(mutate func(*Config)) (Transition, error) {
	h.edit.Lock()
	defer h.edit.Unlock()

	// loadFile allocates a fresh Config on every call, so the transaction
	// owns base outright and mutates it in place; live snapshots are never
	// touched — readers only ever see the reload below.
	base, err := loadFile(h.path)
	if err != nil {
		return Transition{}, err
	}

	mutate(base)

	if err := base.validate(); err != nil {
		return Transition{}, err
	}

	if err := Save(h.path, base); err != nil {
		return Transition{}, err
	}

	// Reload rather than store fresh: the live snapshot must carry the
	// environment layer the file intentionally does not.
	return h.reloadLocked()
}

// structuralChanges lists changed fields that only apply at restart.
func structuralChanges(old, fresh *Config) []string {
	notes := make([]string, 0, 3)

	if old.Daemon.HTTP != fresh.Daemon.HTTP {
		notes = append(notes, "daemon.http (restart required)")
	}

	if old.Control != fresh.Control {
		notes = append(notes, "control (restart required)")
	}

	if old.Providers.Dispatch != fresh.Providers.Dispatch {
		notes = append(notes, "providers.dispatch (restart required)")
	}

	return notes
}
