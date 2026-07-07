package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"golang.org/x/term"

	"github.com/spf13/cobra"

	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/secret"
)

// keyStore is the command's port onto the OS keychain. Write-only:
// keys go in and are removed, never read back out.
type keyStore interface {
	Store(ref, value string) error
	Delete(ref string) error
}

// osKeychain wires keyStore to the real OS keychain.
type osKeychain struct{}

func (osKeychain) Store(ref, value string) error { return secret.Store(ref, value) }
func (osKeychain) Delete(ref string) error       { return secret.Delete(ref) }

// newKeyCmd manages provider API keys in the OS keychain — the only place
// Prukka reads them from.
func newKeyCmd(store keyStore) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "key",
		Short: "Store or remove provider API keys in the OS keychain",
	}

	cmd.AddCommand(newKeySetCmd(store), newKeyRmCmd(store))

	return cmd
}

// newKeySetCmd stores one provider's key: a hidden prompt on a terminal, or
// one line of stdin when piped.
func newKeySetCmd(store keyStore) *cobra.Command {
	return &cobra.Command{
		Use:   "set <provider>",
		Short: "Prompt for a key (hidden; or piped stdin) and store it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := keyRef(args[0])
			if err != nil {
				return err
			}

			value, err := readKey(cmd)
			if err != nil {
				return err
			}

			if err := store.Store(ref, value); err != nil {
				return err
			}

			cmd.Printf("stored at %s — new sessions use it (verify with `prukka doctor`)\n", ref)

			return nil
		},
	}
}

// newKeyRmCmd removes one provider's key.
func newKeyRmCmd(store keyStore) *cobra.Command {
	return &cobra.Command{
		Use:   "rm <provider>",
		Short: "Remove a provider key from the OS keychain",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := keyRef(args[0])
			if err != nil {
				return err
			}

			if err := store.Delete(ref); err != nil {
				return err
			}

			cmd.Printf("removed %s\n", ref)

			return nil
		},
	}
}

// keyRef validates the provider against the shared registry and builds its
// keychain reference — the same one the default configuration resolves.
func keyRef(provider string) (string, error) {
	if !slices.Contains(config.KeyProviders(), provider) {
		return "", fmt.Errorf("unknown provider %q (available: %s)",
			provider, strings.Join(config.KeyProviders(), ", "))
	}

	return secret.Scheme + "prukka/" + provider, nil
}

// readKey takes the secret from a hidden terminal prompt, or from one line
// of stdin when piped (printf %s "$KEY" | prukka key set openrouter).
func readKey(cmd *cobra.Command) (string, error) {
	if in, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(int(in.Fd())) {
		cmd.Print("API key (input hidden): ")

		raw, err := term.ReadPassword(int(in.Fd()))

		cmd.Println()

		if err != nil {
			return "", fmt.Errorf("read key: %w", err)
		}

		return keyValue(string(raw))
	}

	scanner := bufio.NewScanner(cmd.InOrStdin())
	scanner.Scan()

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read key: %w", err)
	}

	return keyValue(scanner.Text())
}

// keyValue rejects blank input so a stray Enter never stores an empty key.
func keyValue(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", errors.New("empty key")
	}

	return value, nil
}
