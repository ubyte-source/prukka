package hostos_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ubyte-source/prukka/internal/hostos"
)

func TestNamesSpellTheGOOSValues(t *testing.T) {
	t.Parallel()

	for goos, name := range map[string]string{
		"darwin":  hostos.Darwin,
		"linux":   hostos.Linux,
		"windows": hostos.Windows,
	} {
		if name != goos {
			t.Fatalf("constant for %s = %q", goos, name)
		}
	}
}

func TestExecutableRejectsDirectories(t *testing.T) {
	t.Parallel()

	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat temp dir: %v", err)
	}
	if hostos.Executable(info) {
		t.Fatal("a directory is not an executable file")
	}
}

func TestExecutableFollowsTheHostExecuteBit(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(path, []byte("binary"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if got := hostos.Executable(info); got != (runtime.GOOS == hostos.Windows) {
		t.Fatalf("Executable(non-executable file) = %v on %s", got, runtime.GOOS)
	}

	if chmodErr := os.Chmod(path, info.Mode().Perm()|0o100); chmodErr != nil {
		t.Fatalf("chmod: %v", chmodErr)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if !hostos.Executable(info) {
		t.Fatal("Executable(owner-executable file) = false")
	}
}
