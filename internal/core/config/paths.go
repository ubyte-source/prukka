package config

import (
	"os"
	"path/filepath"
	"runtime"
)

// windowsOS is the runtime.GOOS value for Windows, used across path helpers.
const windowsOS = "windows"

// DefaultPath returns the platform's config file location: system-wide
// when running as root, per-user otherwise.
func DefaultPath() string {
	switch runtime.GOOS {
	case windowsOS:
		return filepath.Join(appData(), "Prukka", "config.yaml")
	case "darwin":
		if os.Geteuid() == 0 {
			return "/Library/Application Support/Prukka/config.yaml"
		}

		return filepath.Join(home(), "Library", "Application Support", "Prukka", "config.yaml")
	default:
		if os.Geteuid() == 0 {
			return "/etc/prukka/config.yaml"
		}

		if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
			return filepath.Join(v, "prukka", "config.yaml")
		}

		return filepath.Join(home(), ".config", "prukka", "config.yaml")
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
		// Per-user, never %ProgramData%: the daemon holds the user's
		// control token and TLS material, and ProgramData files are
		// readable by every local account.
		return filepath.Join(localAppData(), "Prukka")
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
		// The pipe namespace is machine-global: without a per-user suffix
		// two logged-in users' daemons would collide on one endpoint.
		return `\\.\pipe\prukkad-` + windowsUser()
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

// appData resolves %AppData% (roaming, per-user), the Windows home for
// user configuration.
func appData() string {
	if v := os.Getenv("APPDATA"); v != "" {
		return v
	}

	return filepath.Join(home(), "AppData", "Roaming")
}

// localAppData resolves %LocalAppData% (per-user, non-roaming), the
// Windows home for machine-local state.
func localAppData() string {
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		return v
	}

	return filepath.Join(home(), "AppData", "Local")
}

// windowsUser names the current Windows account for per-user endpoints;
// the domain qualifier keeps two same-named accounts from different
// domains apart under fast user switching.
func windowsUser() string {
	name := os.Getenv("USERNAME")
	if name == "" {
		return "default"
	}

	if domain := os.Getenv("USERDOMAIN"); domain != "" {
		return domain + "-" + name
	}

	return name
}

// home resolves the user home directory, falling back to the working
// directory so path helpers never fail outright.
func home() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}

	return "."
}
