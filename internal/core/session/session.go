// Package session manages session lifecycles: one source re-emitted in N
// languages under a profile, a budget and a delay.
package session

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
)

// Profile selects which pipeline shape a session runs.
type Profile string

// The three session profiles
const (
	ProfileBroadcast Profile = "broadcast"
	ProfileCall      Profile = "call"
	ProfileAgent     Profile = "agent"
)

// Sentinel errors for programmatic handling at the control-plane boundary.
var (
	ErrNotFound       = errors.New("session not found")
	ErrExists         = errors.New("session already exists")
	ErrInvalidSlug    = errors.New("invalid session slug")
	ErrUnknownProfile = errors.New("unknown profile")
	ErrNoLanguages    = errors.New("session needs at least one target language")
)

// ParseProfile validates user input from the CLI, API or config as a Profile.
func ParseProfile(input string) (Profile, error) {
	p := Profile(strings.ToLower(strings.TrimSpace(input)))
	switch p {
	case ProfileBroadcast, ProfileCall, ProfileAgent:
		return p, nil
	default:
		return "", fmt.Errorf("%w %q (expected broadcast, call or agent)", ErrUnknownProfile, input)
	}
}

// Session describes one unit of work. Language tags and the
// profile are validated at the boundary; code holding a Session trusts them.
type Session struct {
	VoiceMap         map[string]core.Voice // Track identity to TTS voice
	Flags            map[string]string     // per-session flags (subs, bed, ...)
	Slug             string
	Profile          Profile
	Source           core.SourceSpec
	Langs            []core.Lang
	BudgetEURPerHour float64
	Delay            time.Duration // the per-session delay D
}

// reservedSlugs are path roots a session slug would shadow on the data
// plane.
var reservedSlugs = map[string]bool{"ui": true, "api": true, "healthz": true, "metrics": true}

// validate checks the invariants the store enforces on every write.
func (s *Session) validate() error {
	if !slugOK(s.Slug) {
		return fmt.Errorf("%w %q (lowercase letters, digits and inner hyphens, max 63 chars)", ErrInvalidSlug, s.Slug)
	}

	if reservedSlugs[s.Slug] {
		return fmt.Errorf("%w %q (reserved path)", ErrInvalidSlug, s.Slug)
	}

	if _, err := ParseProfile(string(s.Profile)); err != nil {
		return err
	}

	if len(s.Langs) == 0 {
		return ErrNoLanguages
	}

	return nil
}

// clone deep-copies a session so store internals and callers never share
// mutable maps or slices.
func clone(s *Session) Session {
	out := *s
	out.Langs = slices.Clone(s.Langs)
	out.VoiceMap = maps.Clone(s.VoiceMap)
	out.Flags = maps.Clone(s.Flags)

	return out
}

// slugOK reports whether s is a valid slug: DNS-label shaped, safe in URLs
// and file paths unescaped.
func slugOK(s string) bool {
	if s == "" || len(s) > 63 || s[0] == '-' {
		return false
	}

	for _, r := range s {
		if !slugRune(r) {
			return false
		}
	}

	return true
}

// slugRune reports whether r is legal anywhere in a slug.
func slugRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
}
