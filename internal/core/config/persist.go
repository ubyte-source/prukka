// Persistence for the dashboard-managed configuration: what the dashboard
// writes is exactly what the daemon can load.

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"gopkg.in/yaml.v3"

	"github.com/ubyte-source/prukka/internal/paths"
)

// ErrPersist marks a failure to write the config file — the environment's
// fault, never the caller's edit.
var ErrPersist = errors.New("persist config")

// Save writes cfg atomically (temp + rename) so a crash never tears the
// file; empty path selects the platform default.
func Save(path string, cfg *Config) error {
	if path == "" {
		path = paths.DefaultPath()
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}

	dir := filepath.Dir(path)
	if mkErr := os.MkdirAll(dir, 0o750); mkErr != nil {
		return fmt.Errorf("%w: create config dir: %w", ErrPersist, mkErr)
	}

	tmp, err := os.CreateTemp(dir, ".config-*.yaml")
	if err != nil {
		return fmt.Errorf("%w: stage config: %w (is %s writable by the daemon?)", ErrPersist, err, dir)
	}

	if fillErr := fill(tmp, data); fillErr != nil {
		return errors.Join(fmt.Errorf("%w: %w", ErrPersist, fillErr), os.Remove(tmp.Name()))
	}

	if renameErr := replaceConfig(tmp.Name(), path); renameErr != nil {
		return errors.Join(fmt.Errorf("%w: publish config: %w", ErrPersist, renameErr), os.Remove(tmp.Name()))
	}
	if syncErr := syncConfigDir(dir); syncErr != nil {
		return fmt.Errorf("%w: persist config directory: %w", ErrPersist, syncErr)
	}

	return nil
}

// fill writes the staged bytes: content, private permissions, fsync — the
// temp file is closed on every path.
func fill(tmp *os.File, data []byte) (err error) {
	defer func() { err = errors.Join(err, tmp.Close()) }()

	if _, writeErr := tmp.Write(data); writeErr != nil {
		return fmt.Errorf("write config: %w", writeErr)
	}

	if chmodErr := tmp.Chmod(0o600); chmodErr != nil {
		return fmt.Errorf("chmod config: %w", chmodErr)
	}

	if syncErr := tmp.Sync(); syncErr != nil {
		return fmt.Errorf("sync config: %w", syncErr)
	}

	return nil
}

// clone deep-copies a snapshot.
func (c *Config) clone() *Config {
	fresh := *c
	fresh.Defaults.Langs = slices.Clone(c.Defaults.Langs)
	fresh.Providers.Local.MT.Pairs = slices.Clone(c.Providers.Local.MT.Pairs)
	fresh.deprecated = slices.Clone(c.deprecated)

	return &fresh
}
