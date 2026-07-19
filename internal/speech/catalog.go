package speech

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/lang"
	"github.com/ubyte-source/prukka/internal/strictjson"
)

// Catalog wire contract: the release pipeline generates it, the daemon and
// `prukka setup` consume it. Every artifact carries its own SHA-256, so the
// catalog is the only download trusted by transport alone.
const (
	// CatalogSchema and CatalogVersion pin the producer/consumer wire
	// contract; the engine-catalog tool builds documents against them.
	CatalogSchema   = "prukka.engine.catalog"
	CatalogVersion  = 1
	catalogMaxBytes = 1 << 20

	// SupportedProtocol is the engine bundle protocol this daemon speaks;
	// a catalog for any other protocol is rejected outright.
	SupportedProtocol = 2
)

// Pack kinds: the three capability units the dashboard composes into
// languages.
const (
	PackSTT   = "stt"
	PackMT    = "mt"
	PackVoice = "voice"

	// PackIDSTTCore is the one mandatory pack: the STT models every install
	// needs regardless of language selection.
	PackIDSTTCore = "stt-core"
)

// Catalog lists every downloadable engine artifact for one bundle protocol.
type Catalog struct {
	Schema   string    `json:"schema"`
	Runtimes []Runtime `json:"runtimes"`
	Packs    []Pack    `json:"packs"`
	Version  int       `json:"version"`
	Protocol int       `json:"protocol"`
}

// Runtime is the per-platform executable half of the bundle.
type Runtime struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Pack is one arch-independent model artifact. From/To describe an MT route,
// Lang and Voice describe a voice; the STT pack uses neither.
type Pack struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	Lang    string `json:"lang,omitempty"`
	Voice   string `json:"voice,omitempty"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	License string `json:"license,omitempty"`
	Size    int64  `json:"size"`
}

var (
	errCatalogTooLarge = fmt.Errorf("engine catalog exceeds %d bytes", catalogMaxBytes)
	hexSHA256          = regexp.MustCompile(`^[0-9a-f]{64}$`)
	voiceModelPath     = regexp.MustCompile(`^models/tts/[A-Za-z0-9._-]+\.onnx$`)
)

// ParseCatalog strictly decodes and validates one catalog document.
func ParseCatalog(r io.Reader) (*Catalog, error) {
	raw, err := io.ReadAll(io.LimitReader(r, catalogMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read engine catalog: %w", err)
	}
	if len(raw) > catalogMaxBytes {
		return nil, errCatalogTooLarge
	}

	c := new(Catalog)
	if err := strictjson.Decode(raw, c); err != nil {
		return nil, fmt.Errorf("decode engine catalog: %w", err)
	}
	if err := c.validate(); err != nil {
		return nil, err
	}

	return c, nil
}

// RuntimeFor selects the runtime artifact for one platform.
func (c *Catalog) RuntimeFor(goos, goarch string) (Runtime, error) {
	for _, r := range c.Runtimes {
		if r.OS == goos && r.Arch == goarch {
			return r, nil
		}
	}

	return Runtime{}, fmt.Errorf("engine catalog has no runtime for %s/%s", goos, goarch)
}

// PackByID selects one pack.
func (c *Catalog) PackByID(id string) (Pack, error) {
	for i := range c.Packs {
		if c.Packs[i].ID == id {
			return c.Packs[i], nil
		}
	}

	return Pack{}, fmt.Errorf("engine catalog has no pack %q", id)
}

func (c *Catalog) validate() error {
	if c.Schema != CatalogSchema {
		return fmt.Errorf("engine catalog schema is %q, want %q", c.Schema, CatalogSchema)
	}
	if c.Version != CatalogVersion {
		return fmt.Errorf("engine catalog version is %d, want %d", c.Version, CatalogVersion)
	}
	if c.Protocol != SupportedProtocol {
		return fmt.Errorf("engine catalog protocol is %d; this daemon needs %d", c.Protocol, SupportedProtocol)
	}
	if err := c.validateRuntimes(); err != nil {
		return err
	}

	return c.validatePacks()
}

func (c *Catalog) validateRuntimes() error {
	if len(c.Runtimes) == 0 {
		return errors.New("engine catalog lists no runtimes")
	}
	seen := make(map[string]bool, len(c.Runtimes))
	for i := range c.Runtimes {
		r := &c.Runtimes[i]
		if r.OS == "" || r.Arch == "" {
			return errors.New("engine catalog runtime is missing os or arch")
		}
		key := r.OS + "/" + r.Arch
		if seen[key] {
			return fmt.Errorf("engine catalog lists runtime %s twice", key)
		}
		seen[key] = true
		if err := validateArtifact("runtime "+key, r.URL, r.SHA256, r.Size); err != nil {
			return err
		}
	}

	return nil
}

func (c *Catalog) validatePacks() error {
	seen := make(map[string]bool, len(c.Packs))
	for i := range c.Packs {
		p := &c.Packs[i]
		if seen[p.ID] {
			return fmt.Errorf("engine catalog lists pack %q twice", p.ID)
		}
		seen[p.ID] = true
		if err := p.validate(); err != nil {
			return err
		}
	}
	if !seen[PackIDSTTCore] {
		return fmt.Errorf("engine catalog is missing the mandatory %q pack", PackIDSTTCore)
	}

	return nil
}

func (p *Pack) validate() error {
	if err := p.validateShape(); err != nil {
		return err
	}

	return validateArtifact("pack "+p.ID, p.URL, p.SHA256, p.Size)
}

// validateShape pins each pack to its canonical, kind-derived identity so an
// id can never disagree with the capability it installs.
func (p *Pack) validateShape() error {
	switch p.Kind {
	case PackSTT:
		return p.validateSTTShape()
	case PackMT:
		return p.validateMTShape()
	case PackVoice:
		return p.validateVoiceShape()
	default:
		return fmt.Errorf("pack %q has unknown kind %q", p.ID, p.Kind)
	}
}

func (p *Pack) validateSTTShape() error {
	if p.ID != PackIDSTTCore {
		return fmt.Errorf("stt pack must be %q, got %q", PackIDSTTCore, p.ID)
	}
	if p.From != "" || p.To != "" || p.Lang != "" || p.Voice != "" {
		return fmt.Errorf("pack %q carries fields of another kind", p.ID)
	}

	return nil
}

func (p *Pack) validateMTShape() error {
	from, to, err := baseRoute(p.From, p.To)
	if err != nil {
		return fmt.Errorf("pack %q: %w", p.ID, err)
	}
	if want := MTPackID(from, to); p.ID != want {
		return fmt.Errorf("mt pack id is %q, want %q", p.ID, want)
	}
	if p.Lang != "" || p.Voice != "" {
		return fmt.Errorf("pack %q carries fields of another kind", p.ID)
	}

	return nil
}

func (p *Pack) validateVoiceShape() error {
	base, err := baseLang(p.Lang)
	if err != nil {
		return fmt.Errorf("pack %q: %w", p.ID, err)
	}
	if want := VoicePackID(base); p.ID != want {
		return fmt.Errorf("voice pack id is %q, want %q", p.ID, want)
	}
	if !voiceModelPath.MatchString(p.Voice) {
		return fmt.Errorf("pack %q voice path %q is not a bundled tts model path", p.ID, p.Voice)
	}
	if p.From != "" || p.To != "" {
		return fmt.Errorf("pack %q carries fields of another kind", p.ID)
	}

	return nil
}

// MTPackID names the pack installing one directed translation route.
func MTPackID(from, to string) string { return "mt-" + from + "-" + to }

// VoicePackID names the pack installing one language's voice.
func VoicePackID(base string) string { return "voice-" + base }

func validateArtifact(name, rawURL, sha string, size int64) error {
	if err := requireHTTPSOrLoopback(rawURL); err != nil {
		return fmt.Errorf("engine catalog %s: %w", name, err)
	}
	if !hexSHA256.MatchString(sha) {
		return fmt.Errorf("engine catalog %s sha256 %q is not 64 hex digits", name, sha)
	}
	if size <= 0 || size > maxArtifactBytes {
		return fmt.Errorf("engine catalog %s size %d is outside (0, %d]", name, size, int64(maxArtifactBytes))
	}

	return nil
}

// baseRoute validates one directed MT route between distinct base languages.
func baseRoute(rawFrom, rawTo string) (from, to string, err error) {
	from, err = baseLang(rawFrom)
	if err != nil {
		return "", "", err
	}
	to, err = baseLang(rawTo)
	if err != nil {
		return "", "", err
	}
	if from == to {
		return "", "", fmt.Errorf("route %s-%s translates a language into itself", from, to)
	}

	return from, to, nil
}

// baseLang admits only concrete registered base tags: catalog entries never
// carry regions or auto, so one pack maps to exactly one config capability.
func baseLang(tag string) (string, error) {
	trimmed := strings.TrimSpace(tag)
	parsed, err := lang.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("language %q: %w", tag, err)
	}
	if parsed == core.LangAuto || strings.Contains(trimmed, "-") || string(parsed) != trimmed {
		return "", fmt.Errorf("language %q is not a canonical base tag", tag)
	}

	return trimmed, nil
}
