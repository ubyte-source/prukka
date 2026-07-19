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

// Reload re-reads the file and swaps the snapshot, serialized with Update;
// a failed load keeps the previous config, notes name structural changes.
func (h *Holder) Reload() ([]string, error) {
	h.edit.Lock()
	defer h.edit.Unlock()

	return h.reloadLocked()
}

// reloadLocked is Reload's body; the caller holds edit.
func (h *Holder) reloadLocked() ([]string, error) {
	fresh, err := Load(h.path)
	if err != nil {
		return nil, err
	}

	notes := structuralChanges(h.Current(), fresh)
	h.current.Store(fresh)

	return notes, nil
}

// Update edits the config in one serialized transaction: re-read the file
// layer, mutate, validate, write atomically, swap live.
func (h *Holder) Update(mutate func(*Config)) ([]string, error) {
	h.edit.Lock()
	defer h.edit.Unlock()

	base, err := loadFile(h.path)
	if err != nil {
		return nil, err
	}

	fresh := base.clone()
	mutate(fresh)

	if err := fresh.validate(); err != nil {
		return nil, err
	}

	if err := Save(h.path, fresh); err != nil {
		return nil, err
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
