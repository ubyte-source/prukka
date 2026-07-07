package secret_test

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/ubyte-source/prukka/internal/secret"
)

func TestIsRef(t *testing.T) {
	t.Parallel()

	cases := []struct {
		value string
		want  bool
	}{
		{value: "keychain://prukka/openrouter", want: true},
		{value: "keychain://", want: true},
		{value: "sk-plaintext-key", want: false},
		{value: "", want: false},
	}

	for _, tc := range cases {
		if got := secret.IsRef(tc.value); got != tc.want {
			t.Errorf("IsRef(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestResolvePassesPlaintextThrough(t *testing.T) {
	t.Parallel()

	got, err := secret.Resolve("sk-plaintext")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}

	if got != "sk-plaintext" {
		t.Fatalf("Resolve = %q, want the value unchanged", got)
	}
}

func TestMalformedReferences(t *testing.T) {
	t.Parallel()

	cases := []string{
		"keychain://",            // no service/account
		"keychain://onlyservice", // no account
		"keychain://svc/",        // empty account
		"keychain:///account",    // empty service
		"keychain://svc/a/b",     // nested account
	}

	for _, ref := range cases {
		if _, err := secret.Resolve(ref); !errors.Is(err, secret.ErrMalformed) {
			t.Errorf("Resolve(%q) error = %v, want ErrMalformed", ref, err)
		}
	}
}

func TestStoreResolveDeleteRoundTrip(t *testing.T) {
	// Not parallel: MockInit swaps the process-wide keyring provider.
	keyring.MockInit()

	const ref = "keychain://prukka/roundtrip"

	if err := secret.Store(ref, "s3cr3t"); err != nil {
		t.Fatalf("Store returned error: %v", err)
	}

	got, err := secret.Resolve(ref)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}

	if got != "s3cr3t" {
		t.Fatalf("Resolve = %q, want the stored secret", got)
	}

	if err := secret.Delete(ref); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}

	if _, err := secret.Resolve(ref); err == nil {
		t.Fatal("Resolve succeeded after Delete, want a not-found error")
	}
}

func TestResolveMissingKeyErrors(t *testing.T) {
	keyring.MockInit()

	if _, err := secret.Resolve("keychain://prukka/absent"); err == nil {
		t.Fatal("Resolve of an absent key succeeded, want error")
	}
}

func TestProbeRoundTrips(t *testing.T) {
	keyring.MockInit()

	if err := secret.Probe(); err != nil {
		t.Fatalf("Probe returned error: %v", err)
	}
}
