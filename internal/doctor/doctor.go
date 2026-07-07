// Package doctor runs the environment checks behind `prukka doctor` and
// the Doctor RPC.
package doctor

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
	"github.com/ubyte-source/prukka/internal/secret"
)

// Status grades one probe result.
type Status string

// The three probe outcomes.
const (
	StatusOK   Status = "ok"
	StatusWarn Status = "warn"
	StatusFail Status = "fail"
)

// Check is one probe result.
type Check struct {
	Name   string
	Status Status
	Detail string
}

// checkStateDir names the state-directory probe.
const checkStateDir = "state-dir"

// checkProviderKey names the OpenRouter key probe.
const checkProviderKey = "openrouter-key"

// Run executes every local probe against the loaded configuration.
func Run(cfg *config.Config) []Check {
	return []Check{
		ffmpegCheck(),
		keychainCheck(),
		providerKeyCheck(cfg),
		stateDirCheck(),
	}
}

// ffmpegCheck looks for the ffmpeg binary the media plane runs on: PATH
// first, then the managed state-dir install.
func ffmpegCheck() Check {
	path, err := ffmpeg.Resolve(config.StateDir())
	if err != nil {
		return Check{
			Name:   "ffmpeg",
			Status: StatusWarn,
			Detail: "not found — run `prukka setup` to install it automatically",
		}
	}

	return Check{Name: "ffmpeg", Status: StatusOK, Detail: path}
}

// keychainCheck round-trips a throwaway secret through the OS keychain.
func keychainCheck() Check {
	if err := secret.Probe(); err != nil {
		return Check{
			Name:   "keychain",
			Status: StatusWarn,
			Detail: fmt.Sprintf("OS keychain unavailable: %v — keys cannot be stored securely", err),
		}
	}

	return Check{Name: "keychain", Status: StatusOK, Detail: "OS keychain read/write verified"}
}

// providerKeyCheck warns about missing, plaintext or unresolvable OpenRouter
// keys (plaintext keys in YAML are rejected with a warning here).
func providerKeyCheck(cfg *config.Config) Check {
	key := cfg.Providers.OpenRouter.Key

	switch {
	case key == "":
		return Check{
			Name:   checkProviderKey,
			Status: StatusWarn,
			Detail: "no key configured — AI stages stay offline until one is set",
		}
	case !secret.IsRef(key):
		return Check{
			Name:   checkProviderKey,
			Status: StatusWarn,
			Detail: "plaintext key in config — move it to the OS keychain and reference it as " +
				secret.Scheme + "prukka/openrouter",
		}
	default:
		return resolvedKeyCheck(key)
	}
}

// resolvedKeyCheck verifies the keychain reference yields a secret.
func resolvedKeyCheck(key string) Check {
	if _, err := secret.Resolve(key); err != nil {
		return Check{
			Name:   checkProviderKey,
			Status: StatusWarn,
			Detail: fmt.Sprintf("%s does not resolve (%v) — store the key with: "+
				"security add-generic-password -s prukka -a openrouter -w <KEY> (macOS) "+
				"or secret-tool store --label=prukka service prukka username openrouter (Linux)", key, err),
		}
	}

	return Check{Name: checkProviderKey, Status: StatusOK, Detail: key + " (resolves)"}
}

// stateDirCheck verifies the state directory exists and is writable.
func stateDirCheck() Check {
	dir := config.StateDir()

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Check{Name: checkStateDir, Status: StatusFail, Detail: fmt.Sprintf("cannot create %s: %v", dir, err)}
	}

	probe := filepath.Join(dir, ".doctor-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		return Check{Name: checkStateDir, Status: StatusFail, Detail: fmt.Sprintf("cannot write in %s: %v", dir, err)}
	}

	if err := os.Remove(probe); err != nil {
		return Check{Name: checkStateDir, Status: StatusWarn, Detail: fmt.Sprintf("probe cleanup failed: %v", err)}
	}

	return Check{Name: checkStateDir, Status: StatusOK, Detail: dir}
}
