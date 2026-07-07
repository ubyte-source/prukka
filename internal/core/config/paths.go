package config

import (
	"os"
	"path/filepath"
	"runtime"
)

// windowsOS is the runtime.GOOS value for Windows, used across path helpers.
const windowsOS = "windows"

// DefaultPath returns the platform's config file location.
func DefaultPath() string {
	switch runtime.GOOS {
	case windowsOS:
		return filepath.Join(programData(), "Prukka", "config.yaml")
	case "darwin":
		return "/Library/Application Support/Prukka/config.yaml"
	default:
		return "/etc/prukka/config.yaml"
	}
}

// StateDir returns the runtime-state directory: PRUKKA_STATE, else
// system-wide as root, else per-user.
func StateDir() string {
	if v := os.Getenv("PRUKKA_STATE"); v != "" {
		return v
	}

	switch runtime.GOOS {
	case windowsOS:
		return filepath.Join(programData(), "Prukka")
	case "darwin":
		if os.Geteuid() == 0 {
			return "/Library/Application Support/Prukka"
		}

		return filepath.Join(home(), "Library", "Application Support", "Prukka")
	default:
		if os.Geteuid() == 0 {
			return "/var/lib/prukka"
		}

		if v := os.Getenv("XDG_STATE_HOME"); v != "" {
			return filepath.Join(v, "prukka")
		}

		return filepath.Join(home(), ".local", "state", "prukka")
	}
}

// TokenPath returns the location of the per-install control token
// ($STATE/control.token, mode 0600).
func TokenPath() string {
	return filepath.Join(StateDir(), "control.token")
}

// IPCPath returns the local control endpoint: a named pipe on Windows, a
// UNIX socket elsewhere.
func IPCPath() string {
	switch runtime.GOOS {
	case windowsOS:
		return `\\.\pipe\prukkad`
	case "darwin":
		return filepath.Join(StateDir(), "prukkad.sock")
	default:
		if os.Geteuid() == 0 {
			return "/run/prukka/prukkad.sock"
		}

		if v := os.Getenv("XDG_RUNTIME_DIR"); v != "" {
			return filepath.Join(v, "prukka", "prukkad.sock")
		}

		return filepath.Join(StateDir(), "prukkad.sock")
	}
}

// programData resolves %ProgramData%, defaulting to the stock Windows path.
func programData() string {
	if v := os.Getenv("ProgramData"); v != "" {
		return v
	}

	return `C:\ProgramData`
}

// home resolves the user home directory, falling back to the working
// directory so path helpers never fail outright.
func home() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}

	return "."
}
