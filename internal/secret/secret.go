// Package secret resolves keychain:// references from the OS keychain so
// keys never live in plaintext config.
package secret

import (
	"errors"
	"fmt"
	"strings"

	"github.com/zalando/go-keyring"
)

// Scheme prefixes every OS-keychain reference.
const Scheme = "keychain://"

// ErrMalformed marks a keychain reference that is not keychain://service/account.
var ErrMalformed = errors.New("malformed secret reference")

// IsRef reports whether value is a keychain reference rather than a
// plaintext secret. Doctor warns when provider keys are not references.
func IsRef(value string) bool {
	return strings.HasPrefix(value, Scheme)
}

// Resolve returns the secret behind a keychain:// reference; non-reference
// values pass through unchanged.
func Resolve(value string) (string, error) {
	if !IsRef(value) {
		return value, nil
	}

	service, account, err := parse(value)
	if err != nil {
		return "", err
	}

	got, err := keyring.Get(service, account)
	if err != nil {
		return "", fmt.Errorf("keychain lookup %s/%s: %w", service, account, err)
	}

	return got, nil
}

// Store writes a secret behind a keychain:// reference. `prukka key set`
// and doctor's keychain probe go through here.
func Store(ref, value string) error {
	service, account, err := parse(ref)
	if err != nil {
		return err
	}

	if err := keyring.Set(service, account, value); err != nil {
		return fmt.Errorf("keychain store %s/%s: %w", service, account, err)
	}

	return nil
}

// Delete removes the secret behind a keychain:// reference.
func Delete(ref string) error {
	service, account, err := parse(ref)
	if err != nil {
		return err
	}

	if err := keyring.Delete(service, account); err != nil {
		return fmt.Errorf("keychain delete %s/%s: %w", service, account, err)
	}

	return nil
}

// Probe verifies the OS keychain is usable by round-tripping a throwaway
// entry. Doctor reports its result.
func Probe() error {
	const ref = Scheme + "prukka/doctor-probe"

	if err := Store(ref, "ok"); err != nil {
		return err
	}

	if _, err := Resolve(ref); err != nil {
		return err
	}

	return Delete(ref)
}

// parse splits keychain://service/account into its parts.
func parse(ref string) (service, account string, err error) {
	rest, ok := strings.CutPrefix(ref, Scheme)
	if !ok {
		return "", "", fmt.Errorf("%w %q: expected %sservice/account", ErrMalformed, ref, Scheme)
	}

	service, account, ok = strings.Cut(rest, "/")
	if !ok || service == "" || account == "" || strings.Contains(account, "/") {
		return "", "", fmt.Errorf("%w %q: expected %sservice/account", ErrMalformed, ref, Scheme)
	}

	return service, account, nil
}
