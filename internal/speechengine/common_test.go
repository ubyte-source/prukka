package speechengine

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBundlePath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	model := filepath.Join("models", "model.bin")
	if got := bundlePath(dir, model); got != filepath.Join(dir, model) {
		t.Fatalf("relative bundle path = %q", got)
	}
	abs := filepath.Join(string(filepath.Separator), "tmp", "model.bin")
	if runtime.GOOS == goosWindows {
		// Windows absolute paths need a drive letter; "\tmp\model.bin" is not
		// absolute there, so exercise a genuinely-absolute path per OS.
		abs = `C:\tmp\model.bin`
	}
	if !filepath.IsAbs(abs) {
		t.Fatalf("test bug: %q is not absolute on %s", abs, runtime.GOOS)
	}
	if got := bundlePath(dir, abs); got != filepath.Clean(abs) {
		t.Fatalf("absolute bundle path = %q", got)
	}
}

func TestLibraryEnvUsesPlatformVariable(t *testing.T) {
	t.Setenv("LD_LIBRARY_PATH", "")
	t.Setenv("DYLD_LIBRARY_PATH", "")
	t.Setenv("PATH", "")

	wantKey := "LD_LIBRARY_PATH="
	switch runtime.GOOS {
	case "darwin":
		wantKey = "DYLD_LIBRARY_PATH="
	case goosWindows:
		wantKey = "PATH="
	}
	env := libraryEnv(nil, "/bundle/lib")
	if len(env) != 1 || !strings.HasPrefix(env[0], wantKey) {
		t.Fatalf("library env = %v, want %s", env, wantKey)
	}
}
