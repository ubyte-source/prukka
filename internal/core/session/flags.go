package session

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/lang"
)

// Off is the token that disables a media option.
const Off = "off"

// SubsMode selects how a session renders captions; the zero value is filled
// from the daemon configuration at the control boundary.
type SubsMode string

// Caption render modes.
const (
	SubsOff  SubsMode = Off
	SubsVTT  SubsMode = "vtt"
	SubsBurn SubsMode = "burn"
)

// ParseSubs validates CLI, API or configuration input as a SubsMode.
func ParseSubs(input string) (SubsMode, error) {
	mode := SubsMode(strings.ToLower(strings.TrimSpace(input)))
	switch mode {
	case SubsOff, SubsVTT, SubsBurn:
		return mode, nil
	default:
		return "", fmt.Errorf("%w: subs %q (expected off, vtt or burn)", ErrInvalidFlags, input)
	}
}

// DubMode says whether a session synthesizes speech; the zero value dubs.
type DubMode string

// Dubbing modes.
const (
	DubOn  DubMode = ""
	DubOff DubMode = Off
)

// ParseDub validates CLI or API input as a DubMode.
func ParseDub(input string) (DubMode, error) {
	mode := DubMode(strings.ToLower(strings.TrimSpace(input)))
	switch mode {
	case DubOn, DubOff:
		return mode, nil
	default:
		return "", fmt.Errorf("%w: dub %q (expected off)", ErrInvalidFlags, input)
	}
}

// BedMode is the original-audio level under the dubbed voice, either Off or a
// decibel figure; the zero value takes the lane's configured fallback.
type BedMode string

// ParseBed validates CLI or API input as a BedMode.
func ParseBed(input string) (BedMode, error) {
	if _, err := core.BedLevel(input); err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidFlags, err)
	}

	return BedMode(input), nil
}

// DubSelection is the tri-state the wire's dub_langs entry carries: absent
// dubs every target language, present names the listed subset, and present
// but empty means captions only.
type DubSelection struct {
	langs  []core.Lang
	chosen bool
}

// AllDubLangs dubs every target language.
func AllDubLangs() DubSelection { return DubSelection{} }

// DubOnly restricts dubbing to langs; an empty list means captions only.
func DubOnly(langs ...core.Lang) DubSelection {
	return DubSelection{langs: slices.Clone(langs), chosen: true}
}

// ParseDubLangs reads the wire's comma-separated subset; an empty list means
// captions only.
func ParseDubLangs(input string) (DubSelection, error) {
	parsed, err := lang.ParseList(input)
	if err != nil {
		return DubSelection{}, fmt.Errorf("%w: dub_langs: %w", ErrInvalidFlags, err)
	}

	return DubSelection{langs: parsed, chosen: true}, nil
}

// Chosen reports whether the session named a dubbed subset at all.
func (d DubSelection) Chosen() bool { return d.chosen }

// Langs returns the named subset, empty when none was named.
func (d DubSelection) Langs() []core.Lang { return slices.Clone(d.langs) }

// String renders the selection for the wire; only call it when Chosen.
func (d DubSelection) String() string {
	tags := make([]string, 0, len(d.langs))
	for _, target := range d.langs {
		tags = append(tags, string(target))
	}

	return strings.Join(tags, ",")
}

// retain drops languages that are no longer targets.
func (d DubSelection) retain(langs []core.Lang) DubSelection {
	if !d.chosen {
		return d
	}

	kept := make([]core.Lang, 0, len(d.langs))
	for _, target := range langs {
		if slices.Contains(d.langs, target) {
			kept = append(kept, target)
		}
	}

	return DubSelection{langs: kept, chosen: true}
}

// validate reports whether the selection names only distinct target languages.
func (d DubSelection) validate(targets []core.Lang) error {
	if !d.chosen {
		return nil
	}

	seen := make(map[core.Lang]bool, len(d.langs))
	for _, target := range d.langs {
		if !slices.Contains(targets, target) || seen[target] {
			return fmt.Errorf("%w: dub_langs contains %q", ErrInvalidFlags, target)
		}
		seen[target] = true
	}

	return nil
}

// Wire flag keys; ApplyFlags and FlagsWire are the only code that spells them.
const (
	FlagSubs     = "subs"
	FlagDub      = "dub"
	FlagDubLangs = "dub_langs"
	FlagBed      = "bed"
	FlagSource   = "source"
	FlagPair     = "pair"
)

// ApplyFlags parses the wire's open flag map onto the session's typed options,
// refusing unknown keys; checks that span fields belong to validate.
func (s *Session) ApplyFlags(flags map[string]string) error {
	for key, value := range flags {
		if err := s.applyFlag(key, value); err != nil {
			return err
		}
	}

	return nil
}

func (s *Session) applyFlag(key, value string) error {
	switch key {
	case FlagSubs:
		return assign(&s.Subs, value, ParseSubs)
	case FlagDub:
		return assign(&s.Dub, value, ParseDub)
	case FlagBed:
		return assign(&s.Bed, value, ParseBed)
	case FlagDubLangs:
		// Present-and-empty is itself a choice, so this must not take assign's
		// "value omitted" path.
		selection, err := ParseDubLangs(value)
		if err != nil {
			return err
		}
		s.DubLangs = selection

		return nil
	case FlagSource:
		return assign(&s.SourceLang, value, lang.Parse)
	case FlagPair:
		return s.applyPair(value)
	default:
		return fmt.Errorf("%w: unknown option %q", ErrInvalidFlags, key)
	}
}

// assign parses one flag value onto its field, treating an empty value as
// "not set".
func assign[T any](field *T, value string, parse func(string) (T, error)) error {
	if value == "" {
		return nil
	}

	parsed, err := parse(value)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidFlags, err)
	}

	*field = parsed

	return nil
}

func (s *Session) applyPair(value string) error {
	if value != "" && !slugOK(value) {
		return fmt.Errorf("%w: pair=%q", ErrInvalidFlags, value)
	}

	s.Pair = value

	return nil
}

// FlagsWire renders the typed options back onto the v1 flag map, omitting any
// left at its zero value.
func (s *Session) FlagsWire() map[string]string {
	flags := make(map[string]string, 6)
	setFlag(flags, FlagSubs, string(s.Subs))
	setFlag(flags, FlagDub, string(s.Dub))
	setFlag(flags, FlagBed, string(s.Bed))
	setFlag(flags, FlagSource, string(s.SourceLang))
	setFlag(flags, FlagPair, s.Pair)
	if s.DubLangs.chosen {
		flags[FlagDubLangs] = s.DubLangs.String()
	}

	return flags
}

func setFlag(flags map[string]string, key, value string) {
	if value != "" {
		flags[key] = value
	}
}

// validateMedia checks the typed options against each other and the targets.
func (s *Session) validateMedia() error {
	if err := s.validateModes(); err != nil {
		return err
	}
	if err := s.validatePair(); err != nil {
		return err
	}
	if s.DubLangs.chosen && s.Dub == DubOff {
		return fmt.Errorf("%w: dub=off conflicts with dub_langs", ErrInvalidFlags)
	}

	return s.DubLangs.validate(s.Langs)
}

// validateModes re-checks the vocabularies for a Session built in process
// rather than parsed.
func (s *Session) validateModes() error {
	if s.Subs != "" {
		if _, err := ParseSubs(string(s.Subs)); err != nil {
			return err
		}
	}
	if _, err := ParseDub(string(s.Dub)); err != nil {
		return err
	}
	if s.Bed == "" {
		return nil
	}
	_, err := ParseBed(string(s.Bed))

	return err
}

// validatePair accepts a one-sided pair; only a reciprocal link cascades on
// delete.
func (s *Session) validatePair() error {
	switch {
	case s.Pair == "":
		return nil
	case !slugOK(s.Pair):
		return fmt.Errorf("%w: pair=%q", ErrInvalidFlags, s.Pair)
	case s.Pair == s.Slug:
		return fmt.Errorf("%w: pair must name a different session than %q", ErrInvalidFlags, s.Slug)
	default:
		return nil
	}
}

// sameMedia reports whether two definitions carry the same media options;
// DubSelection holds a slice, so == cannot.
func sameMedia(a, b *Session) bool {
	return a.Subs == b.Subs && a.Dub == b.Dub && a.Bed == b.Bed &&
		a.Pair == b.Pair && a.SourceLang == b.SourceLang &&
		a.DubLangs.chosen == b.DubLangs.chosen && slices.Equal(a.DubLangs.langs, b.DubLangs.langs)
}

// DubbedLangs returns the target languages to synthesize, in target order.
func (s *Session) DubbedLangs() []core.Lang {
	if s.Dub == DubOff {
		return nil
	}
	if !s.DubLangs.chosen {
		return slices.Clone(s.Langs)
	}

	out := make([]core.Lang, 0, len(s.DubLangs.langs))
	for _, target := range s.Langs {
		if slices.Contains(s.DubLangs.langs, target) {
			out = append(out, target)
		}
	}

	return out
}
