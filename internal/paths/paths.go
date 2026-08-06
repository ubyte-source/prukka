// Package paths resolves the platform's filesystem locations for the daemon:
// the config file, the runtime-state directory, the control token and the
// local IPC endpoint.
package paths

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/ubyte-source/prukka/internal/hostos"
)

// DefaultPath returns the platform's config file location: system-wide
// when running as root, per-user otherwise.
func DefaultPath() string {
	switch runtime.GOOS {
	case hostos.Windows:
		return filepath.Join(appData(), "Prukka", "config.yaml")
	case hostos.Darwin:
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
	case hostos.Windows:
		// Never %ProgramData%: those files are readable by every local
		// account, and the state holds the control token.
		return filepath.Join(localAppData(), "Prukka")
	case hostos.Darwin:
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
	case hostos.Windows:
		// The pipe namespace is machine-global, so two logged-in users'
		// daemons would collide without a per-user suffix.
		return `\\.\pipe\prukkad-` + windowsUser()
	case hostos.Darwin:
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

// appData resolves %AppData% (roaming, per-user).
func appData() string {
	if v := os.Getenv("APPDATA"); v != "" {
		return v
	}

	return filepath.Join(home(), "AppData", "Roaming")
}

// localAppData resolves %LocalAppData% (per-user, non-roaming).
func localAppData() string {
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		return v
	}

	return filepath.Join(home(), "AppData", "Local")
}

// windowsUser names the current Windows account, domain-qualified so two
// same-named accounts from different domains stay apart.
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

// home falls back to the working directory so path helpers never fail.
func home() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}

	return "."
}
