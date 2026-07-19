// Package session manages session lifecycles: one source re-emitted in N
// languages under a profile with a playout delay.
package session

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/lang"
)

// Profile selects which pipeline shape a session runs.
type Profile string

// Supported session profiles.
const (
	ProfileBroadcast Profile = "broadcast"
	ProfileCall      Profile = "call"
)

// Sentinel errors for programmatic handling at the control-plane boundary.
var (
	ErrNotFound        = errors.New("session not found")
	ErrExists          = errors.New("session already exists")
	ErrCapacity        = errors.New("session capacity exhausted")
	ErrInvalidSlug     = errors.New("invalid session slug")
	ErrInvalidSource   = errors.New("invalid session source")
	ErrUnknownProfile  = errors.New("unknown profile")
	ErrNoLanguages     = errors.New("session needs at least one target language")
	ErrInvalidLanguage = errors.New("invalid target language")
	ErrInvalidDelay    = errors.New("invalid session delay")
	ErrInvalidFlags    = errors.New("invalid session flags")
)

// ParseProfile validates user input from the CLI, API or config as a Profile.
func ParseProfile(input string) (Profile, error) {
	p := Profile(strings.ToLower(strings.TrimSpace(input)))
	switch p {
	case ProfileBroadcast, ProfileCall:
		return p, nil
	default:
		return "", fmt.Errorf("%w %q (expected broadcast or call)", ErrUnknownProfile, input)
	}
}

// Session describes one unit of work. Language tags and the
// profile are validated at the boundary; code holding a Session trusts them.
type Session struct {
	Flags    map[string]string // per-session flags (subs, bed, ...)
	Slug     string
	Profile  Profile
	Source   core.SourceSpec
	Langs    []core.Lang
	runtime  RuntimeStatus
	Delay    time.Duration // the per-session delay D
	revision uint64
}

// reservedSlugs are path roots a session slug would shadow on the data
// plane.
var reservedSlugs = map[string]bool{"ui": true, "api": true, "healthz": true, "metrics": true}

// validate checks the invariants the store enforces on every write.
func (s *Session) validate() error {
	if err := s.validateIdentity(); err != nil {
		return err
	}
	if err := s.validateLimits(); err != nil {
		return err
	}
	if err := s.validateSource(); err != nil {
		return err
	}
	if err := s.validateLanguages(); err != nil {
		return err
	}
	if err := validateMediaFlags(s.Flags); err != nil {
		return err
	}
	if pair := s.Flags["pair"]; pair != "" && pair == s.Slug {
		return fmt.Errorf("%w: pair must name a different session than %q", ErrInvalidFlags, s.Slug)
	}

	return validateDubLanguages(s)
}

func (s *Session) validateSource() error {
	raw := s.Source.URL
	if raw == "" {
		return fmt.Errorf("%w: URL is required", ErrInvalidSource)
	}
	if strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w: URL contains control characters", ErrInvalidSource)
	}

	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok || scheme != strings.ToLower(scheme) {
		return fmt.Errorf("%w: URL needs a lowercase scheme", ErrInvalidSource)
	}
	switch scheme {
	case "file":
		return validateFileSource(rest)
	case "device":
		return validateDeviceSource(rest)
	case "rtmp", "srt":
		return validateNetworkSource(raw, scheme)
	default:
		return fmt.Errorf("%w: unsupported scheme", ErrInvalidSource)
	}
}

func validateDeviceSource(rest string) error {
	kind, id, ok := strings.Cut(rest, "/")
	if !ok || id == "" {
		return fmt.Errorf("%w: device identifier is required", ErrInvalidSource)
	}

	switch kind {
	case "audio":
		return nil
	case "av":
		video, audio, paired := strings.Cut(id, "|")
		if !paired || video == "" || audio == "" {
			return fmt.Errorf("%w: paired device needs camera and microphone identifiers", ErrInvalidSource)
		}

		return nil
	default:
		return fmt.Errorf("%w: unsupported device kind", ErrInvalidSource)
	}
}

func validateFileSource(rest string) error {
	path, query, _ := strings.Cut(rest, "?")
	if path == "" {
		return fmt.Errorf("%w: file path is required", ErrInvalidSource)
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		return fmt.Errorf("%w: malformed query", ErrInvalidSource)
	}
	for key := range values {
		if key != "loop" {
			return fmt.Errorf("%w: unsupported file query parameter", ErrInvalidSource)
		}
	}
	loop, ok := values["loop"]
	if ok && (len(loop) != 1 || (loop[0] != "true" && loop[0] != "false")) {
		return fmt.Errorf("%w: loop must be exactly true or false", ErrInvalidSource)
	}

	return nil
}

func validateNetworkSource(raw, scheme string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != scheme || parsed.Hostname() == "" {
		return fmt.Errorf("%w: malformed %s URL", ErrInvalidSource, scheme)
	}
	if scheme == "rtmp" && strings.Trim(parsed.EscapedPath(), "/") == "" {
		return fmt.Errorf("%w: RTMP stream path is required", ErrInvalidSource)
	}
	return nil
}

func (s *Session) validateIdentity() error {
	if !slugOK(s.Slug) {
		return fmt.Errorf("%w %q (lowercase letters, digits and inner hyphens, max 63 chars)", ErrInvalidSlug, s.Slug)
	}

	if reservedSlugs[s.Slug] {
		return fmt.Errorf("%w %q (reserved path)", ErrInvalidSlug, s.Slug)
	}

	if _, err := ParseProfile(string(s.Profile)); err != nil {
		return err
	}

	return nil
}

func (s *Session) validateLimits() error {
	if s.Delay < 0 || s.Delay > core.MaxSessionDelay {
		return fmt.Errorf("%w %v", ErrInvalidDelay, s.Delay)
	}

	return nil
}

func (s *Session) validateLanguages() error {
	if len(s.Langs) == 0 {
		return ErrNoLanguages
	}
	seen := make(map[core.Lang]bool, len(s.Langs))
	for _, target := range s.Langs {
		parsed, err := lang.Parse(string(target))
		if err != nil || parsed == core.LangAuto || parsed != target {
			return fmt.Errorf("%w %q", ErrInvalidLanguage, target)
		}
		if seen[target] {
			return fmt.Errorf("%w %q: duplicate target", ErrInvalidLanguage, target)
		}
		seen[target] = true
	}

	return nil
}

func validateMediaFlags(flags map[string]string) error {
	for key, value := range flags {
		if err := validateMediaFlag(key, value); err != nil {
			return err
		}
	}

	return nil
}

func validateMediaFlag(key, value string) error {
	switch key {
	case "subs":
		return requireFlagValue(key, value, "", "off", "vtt", "burn")
	case "dub":
		return requireFlagValue(key, value, "", "off")
	case "source":
		return requireSourceFlag(value)
	case "voices":
		return requireFlagValue(key, value, "", "manual")
	case "bed":
		return validateBedFlag(value)
	case "dub_langs":
		return nil
	case "pair":
		return validatePairFlag(value)
	default:
		return fmt.Errorf("%w: unknown option %q", ErrInvalidFlags, key)
	}
}

func requireFlagValue(key, value string, allowed ...string) error {
	if slices.Contains(allowed, value) {
		return nil
	}
	return invalidFlag(key, value)
}

func requireSourceFlag(value string) error {
	if value == "" {
		return nil
	}
	if _, err := lang.Parse(value); err == nil {
		return nil
	}
	return invalidFlag("source", value)
}

func invalidFlag(key, value string) error {
	return fmt.Errorf("%w: %s=%q", ErrInvalidFlags, key, value)
}

func validBedFlag(raw string) bool {
	if raw == "" {
		return true
	}
	_, err := core.BedLevel(raw)

	return err == nil
}

func validateBedFlag(value string) error {
	if validBedFlag(value) {
		return nil
	}

	return invalidFlag("bed", value)
}

func validatePairFlag(value string) error {
	if slugOK(value) {
		return nil
	}

	return invalidFlag("pair", value)
}

func validateDubLanguages(s *Session) error {
	raw, configured := s.Flags["dub_langs"]
	if !configured {
		return nil
	}
	if s.Flags["dub"] == "off" {
		return fmt.Errorf("%w: dub=off conflicts with dub_langs", ErrInvalidFlags)
	}

	available := make(map[core.Lang]bool, len(s.Langs))
	for _, target := range s.Langs {
		available[target] = true
	}
	seen := make(map[core.Lang]bool)
	for value := range strings.SplitSeq(raw, ",") {
		if strings.TrimSpace(value) == "" {
			continue
		}
		target, err := lang.Parse(value)
		if err != nil || !available[target] || seen[target] {
			return fmt.Errorf("%w: dub_langs contains %q", ErrInvalidFlags, value)
		}
		seen[target] = true
	}

	return nil
}

// clone deep-copies a session so store internals and callers never share
// mutable maps or slices.
func clone(s *Session) Session {
	out := *s
	out.Langs = slices.Clone(s.Langs)
	out.Flags = maps.Clone(s.Flags)

	return out
}

// slugOK reports whether s is a valid slug: DNS-label shaped, safe in URLs
// and file paths unescaped.
func slugOK(s string) bool {
	if s == "" || len(s) > 63 || s[0] == '-' || s[len(s)-1] == '-' {
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
