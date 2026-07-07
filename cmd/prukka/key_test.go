package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// fakeKeychain records stores and deletes; tests must never touch the real
// OS keychain, where the maintainer's actual keys live.
type fakeKeychain struct {
	stored  map[string]string
	fail    error
	deleted []string
}

func (f *fakeKeychain) Store(ref, value string) error {
	if f.fail != nil {
		return f.fail
	}

	if f.stored == nil {
		f.stored = map[string]string{}
	}

	f.stored[ref] = value

	return nil
}

func (f *fakeKeychain) Delete(ref string) error {
	if f.fail != nil {
		return f.fail
	}

	f.deleted = append(f.deleted, ref)

	return nil
}

// runKey executes `prukka key` with the given args and piped stdin.
func runKey(t *testing.T, store keyStore, stdin string, args ...string) (string, error) {
	t.Helper()

	cmd := newKeyCmd(store)
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)

	err := cmd.Execute()

	return out.String(), err
}

func TestKeySetStoresThePipedKey(t *testing.T) {
	t.Parallel()

	fake := &fakeKeychain{}

	out, err := runKey(t, fake, "sk-or-test-123\n", "set", "openrouter")
	if err != nil {
		t.Fatalf("key set returned error: %v", err)
	}

	const ref = "keychain://prukka/openrouter"
	if fake.stored[ref] != "sk-or-test-123" {
		t.Fatalf("stored %q under %q, want the piped key", fake.stored[ref], ref)
	}

	if !strings.Contains(out, ref) {
		t.Fatalf("output %q does not name the reference the config resolves", out)
	}
}

func TestKeySetTrimsSurroundingWhitespace(t *testing.T) {
	t.Parallel()

	fake := &fakeKeychain{}

	if _, err := runKey(t, fake, "  sk-x  \n", "set", "openrouter"); err != nil {
		t.Fatalf("key set returned error: %v", err)
	}

	if got := fake.stored["keychain://prukka/openrouter"]; got != "sk-x" {
		t.Fatalf("stored %q, want the trimmed key", got)
	}
}

func TestKeySetRejectsEmptyKey(t *testing.T) {
	t.Parallel()

	fake := &fakeKeychain{}

	if _, err := runKey(t, fake, "\n", "set", "openrouter"); err == nil || !strings.Contains(err.Error(), "empty key") {
		t.Fatalf("key set on blank input = %v, want the empty-key error", err)
	}

	if len(fake.stored) != 0 {
		t.Fatal("a blank key reached the keychain")
	}
}

func TestKeySetRejectsUnknownProvider(t *testing.T) {
	t.Parallel()

	_, err := runKey(t, &fakeKeychain{}, "sk-x\n", "set", "acme")
	if err == nil || !strings.Contains(err.Error(), "openrouter") {
		t.Fatalf("key set acme = %v, want an error listing the available providers", err)
	}
}

func TestKeySetSurfacesKeychainFailure(t *testing.T) {
	t.Parallel()

	fake := &fakeKeychain{fail: errors.New("keychain locked")}

	_, err := runKey(t, fake, "sk-x\n", "set", "openrouter")
	if err == nil || !strings.Contains(err.Error(), "keychain locked") {
		t.Fatalf("key set = %v, want the keychain failure surfaced", err)
	}
}

func TestKeyRmDeletesTheReference(t *testing.T) {
	t.Parallel()

	fake := &fakeKeychain{}

	out, err := runKey(t, fake, "", "rm", "openrouter")
	if err != nil {
		t.Fatalf("key rm returned error: %v", err)
	}

	const ref = "keychain://prukka/openrouter"
	if len(fake.deleted) != 1 || fake.deleted[0] != ref {
		t.Fatalf("deleted %v, want exactly %q", fake.deleted, ref)
	}

	if !strings.Contains(out, ref) {
		t.Fatalf("output %q does not confirm what was removed", out)
	}
}
