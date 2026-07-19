package speech

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/ubyte-source/prukka/internal/strictjson"
)

// stateName is the managed-install inventory beside the bundle directory: it
// records which runtime and packs produced the bundle so operations stay
// idempotent and removals delete exactly what their pack installed.
const (
	stateName     = "state.json"
	stateSchema   = "prukka.engine.state"
	stateVersion  = 1
	stateMaxBytes = 1 << 20
)

// errIncompatibleState marks an inventory written by a daemon of a different
// schema/version/protocol: unusable as-is, but safe to discard and reinstall
// from scratch (EnsureRuntime treats it like a fresh install; pack operations
// and Resolve keep hard-failing, which correctly asks the user to run setup).
var errIncompatibleState = errors.New("engine inventory is from an incompatible daemon version")

// State is the on-disk inventory of one managed engine install.
type State struct {
	Schema   string          `json:"schema"`
	Runtime  InstalledRun    `json:"runtime"`
	Packs    []InstalledPack `json:"packs"`
	Version  int             `json:"version"`
	Protocol int             `json:"protocol"`
}

// InstalledRun records the published runtime artifact.
type InstalledRun struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	SHA256 string `json:"sha256"`
}

// InstalledPack records one published pack and every file it owns, as
// bundle-relative slash paths.
type InstalledPack struct {
	ID     string   `json:"id"`
	Kind   string   `json:"kind"`
	From   string   `json:"from,omitempty"`
	To     string   `json:"to,omitempty"`
	Lang   string   `json:"lang,omitempty"`
	Voice  string   `json:"voice,omitempty"`
	SHA256 string   `json:"sha256"`
	Files  []string `json:"files"`
}

// Pack returns the installed pack with the given id.
func (s *State) Pack(id string) (InstalledPack, bool) {
	for i := range s.Packs {
		if s.Packs[i].ID == id {
			return s.Packs[i], true
		}
	}

	return InstalledPack{}, false
}

// upsertPack replaces or appends one pack record, keeping the list sorted so
// the rendered state is deterministic.
func (s *State) upsertPack(p *InstalledPack) {
	for i := range s.Packs {
		if s.Packs[i].ID == p.ID {
			s.Packs[i] = *p
			return
		}
	}
	s.Packs = append(s.Packs, *p)
	sort.Slice(s.Packs, func(i, j int) bool { return s.Packs[i].ID < s.Packs[j].ID })
}

// dropPack removes one pack record.
func (s *State) dropPack(id string) {
	kept := s.Packs[:0]
	for i := range s.Packs {
		if s.Packs[i].ID != id {
			kept = append(kept, s.Packs[i])
		}
	}
	s.Packs = kept
}

// readState loads and strictly validates the inventory; a missing file
// returns fs.ErrNotExist for the caller to interpret.
func readState(path string) (*State, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > stateMaxBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", stateName, stateMaxBytes)
	}

	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}

	s := new(State)
	if err := strictjson.Decode(raw, s); err != nil {
		return nil, fmt.Errorf("decode %s: %w", stateName, err)
	}
	if s.Schema != stateSchema || s.Version != stateVersion {
		return nil, fmt.Errorf("%w: %s declares %q v%d, want %q v%d",
			errIncompatibleState, stateName, s.Schema, s.Version, stateSchema, stateVersion)
	}
	if s.Protocol != SupportedProtocol {
		return nil, fmt.Errorf("%w: %s records protocol %d, this daemon needs %d",
			errIncompatibleState, stateName, s.Protocol, SupportedProtocol)
	}

	return s, nil
}

// writeState atomically persists the inventory beside the bundle.
func writeState(path string, s *State) error {
	rendered, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("render %s: %w", stateName, err)
	}
	rendered = append(rendered, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return fmt.Errorf("stage %s: %w", stateName, err)
	}
	name := tmp.Name()
	if err := writeAndClose(tmp, rendered); err != nil {
		removeQuietly(name)

		return fmt.Errorf("stage %s: %w", stateName, err)
	}
	if err := os.Rename(name, path); err != nil {
		removeQuietly(name)

		return fmt.Errorf("publish %s: %w", stateName, err)
	}

	return nil
}

func writeAndClose(f *os.File, data []byte) error {
	if _, err := f.Write(data); err != nil {
		closeQuietly(f)

		return err
	}
	if err := f.Sync(); err != nil {
		closeQuietly(f)

		return err
	}

	return f.Close()
}

// removeQuietly drops the error of a best-effort cleanup.
func removeQuietly(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return
	}
}
