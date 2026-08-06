// Package config loads, defaults and validates the daemon configuration —
// the single source of truth every component reads through.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/lang"
	"github.com/ubyte-source/prukka/internal/core/session"
	"github.com/ubyte-source/prukka/internal/paths"
)

// Config is the root daemon configuration.
type Config struct {
	Daemon    Daemon    `yaml:"daemon"`
	Defaults  Defaults  `yaml:"defaults"`
	Providers Providers `yaml:"providers"`
	Control   Control   `yaml:"control"`
}

// LaneFingerprint captures the hot-reloadable configuration a running lane
// depends on; the rendered structs contain no maps, so %+v is stable.
func (c *Config) LaneFingerprint() string {
	return fmt.Sprintf("%s|%+v|%s", c.Providers.Voices, c.Providers.Local, c.Defaults.Bed)
}

// Daemon configures the HTTP/dashboard listener.
type Daemon struct {
	HTTP       string `yaml:"http"`
	CORSOrigin string `yaml:"cors_origin"` // empty disables cross-origin access
}

// Control configures optional TLS on the local IPC transport.
type Control struct {
	IPCTLS bool `yaml:"ipc_tls"`
}

// Providers configures the external local engine and the shared worker pool.
type Providers struct {
	Voices   string   `yaml:"voices"`
	Local    Local    `yaml:"local"`
	Dispatch Dispatch `yaml:"dispatch"`
}

// Voice-stage selectors.
const (
	VoicesLocal = "local"
	VoicesOff   = "off"

	defaultSTTModel      = "models/stt/ggml-base.bin"
	defaultCallSTTModel  = defaultSTTModel
	defaultTTSVoice      = "models/tts/en_US-lessac-medium.onnx"
	defaultTTSVoiceItaly = "models/tts/it_IT-paola-medium.onnx"
)

// The default bundle's language pair.
const (
	langEnglish = core.Lang("en")
	langItalian = core.Lang("it")
)

// Local configures an operator-provided engine bundle; Bin is its executable
// and each stage invokes a dedicated subcommand.
type Local struct {
	Bin string   `yaml:"bin"`
	STT LocalSTT `yaml:"stt"`
	TTS LocalTTS `yaml:"tts"`
	MT  LocalMT  `yaml:"mt"`
}

// LocalSTT selects the primary STT model and an optional call-profile override.
type LocalSTT struct {
	Model     string `yaml:"model"`
	CallModel string `yaml:"call_model,omitempty"`
}

// ModelForCall returns the call-profile override when configured, else Model.
func (s LocalSTT) ModelForCall() string {
	if strings.TrimSpace(s.CallModel) != "" {
		return s.CallModel
	}

	return s.Model
}

// LocalTTS lists the Piper voices the bundle can synthesize, one per language.
type LocalTTS struct {
	Voices []VoiceModel `yaml:"voices"`
}

// VoiceModel binds one Piper voice model to the language it synthesizes.
type VoiceModel struct {
	Language core.Lang `yaml:"language"`
	Voice    string    `yaml:"voice"`
}

// DubbedLanguages reports the languages this configuration can synthesize, in
// configured order.
func (t *LocalTTS) DubbedLanguages() []core.Lang {
	langs := make([]core.Lang, len(t.Voices))
	for i := range t.Voices {
		langs[i] = t.Voices[i].Language
	}

	return langs
}

// SetVoice records the voice that synthesizes language, replacing any already
// bound to it.
func (t *LocalTTS) SetVoice(language core.Lang, voice string) {
	for i := range t.Voices {
		if t.Voices[i].Language == language {
			t.Voices[i].Voice = voice

			return
		}
	}
	t.Voices = append(t.Voices, VoiceModel{Language: language, Voice: voice})
}

// RemoveVoice drops the voice bound to language, if any.
func (t *LocalTTS) RemoveVoice(language core.Lang) {
	kept := t.Voices[:0]
	for _, v := range t.Voices {
		if v.Language != language {
			kept = append(kept, v)
		}
	}
	t.Voices = kept
}

// LocalMT declares the translation models present in the external bundle.
type LocalMT struct {
	Pairs []TranslationPair `yaml:"pairs"`
}

// TranslationPair identifies one installed source-to-target MT model.
type TranslationPair struct {
	From core.Lang `yaml:"from"`
	To   core.Lang `yaml:"to"`
}

// AddPair records a translation model, ignoring a direction already present.
func (m *LocalMT) AddPair(from, to core.Lang) {
	for _, pair := range m.Pairs {
		if pair.From == from && pair.To == to {
			return
		}
	}
	m.Pairs = append(m.Pairs, TranslationPair{From: from, To: to})
}

// RemovePair drops one translation direction, if present.
func (m *LocalMT) RemovePair(from, to core.Lang) {
	kept := m.Pairs[:0]
	for _, pair := range m.Pairs {
		if pair.From != from || pair.To != to {
			kept = append(kept, pair)
		}
	}
	m.Pairs = kept
}

// Dispatch bounds inference resources: Workers and Queue govern MT/TTS calls,
// MaxLanes caps live STT helpers, MaxSessions caps registered definitions.
type Dispatch struct {
	Workers     int `yaml:"workers"`
	Queue       int `yaml:"queue"`
	MaxLanes    int `yaml:"max_lanes"`
	MaxSessions int `yaml:"max_sessions"`
}

// Defaults seeds new sessions.
type Defaults struct {
	Subs  session.SubsMode `yaml:"subs"`
	Bed   session.BedMode  `yaml:"bed"`
	Langs []core.Lang      `yaml:"langs"`
	Delay Duration         `yaml:"delay"`
}

// Duration is a time.Duration that unmarshals from YAML strings like "8s".
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var raw string
	if err := value.Decode(&raw); err != nil {
		return fmt.Errorf("duration: %w", err)
	}

	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("duration %q: %w", raw, err)
	}

	*d = Duration(parsed)

	return nil
}

// MarshalYAML implements yaml.Marshaler, writing the human form ("8s") rather
// than raw nanoseconds.
func (d Duration) MarshalYAML() (any, error) {
	return d.Std().String(), nil
}

// Std returns the standard-library representation.
func (d Duration) Std() time.Duration {
	return time.Duration(d)
}

// Default returns the built-in configuration.
func Default() *Config {
	return &Config{
		Daemon: Daemon{
			HTTP:       "127.0.0.1:8080",
			CORSOrigin: "https://prukka.ubyte.it",
		},
		Providers: Providers{
			Voices: VoicesLocal,
			// Mirrors the bidirectional engine bundle layout (engine/build.sh).
			Local: Local{
				STT: LocalSTT{Model: defaultSTTModel, CallModel: defaultCallSTTModel},
				MT: LocalMT{Pairs: []TranslationPair{
					{From: langItalian, To: langEnglish},
					{From: langEnglish, To: langItalian},
				}},
				TTS: LocalTTS{Voices: []VoiceModel{
					{Language: langEnglish, Voice: defaultTTSVoice},
					{Language: langItalian, Voice: defaultTTSVoiceItaly},
				}},
			},
			Dispatch: Dispatch{Workers: 8, Queue: 256, MaxLanes: 2, MaxSessions: 32},
		},
		Defaults: Defaults{
			Langs: []core.Lang{langItalian, langEnglish},
			Subs:  "vtt",
			Bed:   "-15dB",
			Delay: Duration(8 * time.Second),
		},
	}
}

// Load reads the config file (platform default when path is empty), layers
// PRUKKA_* overrides and validates; only an explicit missing path errors.
func Load(path string) (*Config, error) {
	cfg, err := loadFile(path)
	if err != nil {
		return nil, err
	}

	applyEnv(cfg, os.Getenv)

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// loadFile reads defaults plus the file layer only: no environment, no
// validation.
func loadFile(path string) (*Config, error) {
	cfg := Default()

	explicit := path != ""
	if !explicit {
		path = paths.DefaultPath()
	}

	data, err := os.ReadFile(filepath.Clean(path))

	switch {
	case err == nil:
		if decodeErr := decodeStrict(data, cfg); decodeErr != nil {
			return nil, fmt.Errorf("parse %s: %w", path, decodeErr)
		}
		useDefaultCallModel, presenceErr := defaultCallModelApplies(data)
		if presenceErr != nil {
			return nil, fmt.Errorf("parse %s: %w", path, presenceErr)
		}
		if !useDefaultCallModel {
			// A file naming a primary model without a call override serves calls
			// with that model; unrelated partial configs keep the bundled default.
			cfg.Providers.Local.STT.CallModel = ""
		}
	case errors.Is(err, fs.ErrNotExist) && !explicit:
		// No config file: built-in defaults apply.
	default:
		return nil, fmt.Errorf("read config: %w", err)
	}

	return cfg, nil
}

func defaultCallModelApplies(data []byte) (bool, error) {
	var layer struct {
		Providers struct {
			Local struct {
				STT struct {
					Model     yaml.Node `yaml:"model"`
					CallModel yaml.Node `yaml:"call_model"`
				} `yaml:"stt"`
			} `yaml:"local"`
		} `yaml:"providers"`
	}
	if err := yaml.Unmarshal(data, &layer); err != nil {
		return false, err
	}

	model := &layer.Providers.Local.STT.Model
	callModel := &layer.Providers.Local.STT.CallModel
	if callModel.Kind != 0 {
		return callModel.ShortTag() != "!!null", nil
	}

	return model.Kind == 0, nil
}

// decodeStrict parses YAML rejecting unknown fields, so config typos surface
// at startup instead of silently defaulting.
func decodeStrict(data []byte, cfg *Config) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return err
	}

	var extra any
	if err := dec.Decode(&extra); err == nil {
		return errors.New("multiple YAML documents are not supported")
	} else if !errors.Is(err, io.EOF) {
		return err
	}

	return nil
}

// applyEnv layers PRUKKA_* variables over the file values.
func applyEnv(cfg *Config, get func(string) string) {
	overrides := []struct {
		apply func(*Config, string)
		key   string
	}{
		{key: "PRUKKA_HTTP", apply: func(c *Config, v string) { c.Daemon.HTTP = v }},
		{key: "PRUKKA_ENGINE_BIN", apply: func(c *Config, v string) { c.Providers.Local.Bin = v }},
	}

	for _, o := range overrides {
		if v := get(o.key); v != "" {
			o.apply(cfg, v)
		}
	}
}

// validate enforces the boundary invariants: everything downstream
// trusts a *Config that passed here.
func (c *Config) validate() error {
	if err := c.validateDaemon(); err != nil {
		return err
	}
	if err := c.validateDefaults(); err != nil {
		return err
	}

	return c.validateProvider()
}

func (c *Config) validateDefaults() error {
	if err := c.normalizeDefaultLanguages(); err != nil {
		return err
	}
	if err := c.normalizeDefaultSubs(); err != nil {
		return err
	}
	if err := c.normalizeDefaultBed(); err != nil {
		return err
	}
	if delay := c.Defaults.Delay.Std(); delay < 0 || delay > core.MaxSessionDelay {
		return fmt.Errorf("defaults.delay %v: expected 0s to %s", delay, core.MaxSessionDelay)
	}

	return nil
}

func (c *Config) normalizeDefaultLanguages() error {
	normalized := make([]core.Lang, 0, len(c.Defaults.Langs))
	seen := make(map[core.Lang]bool, len(c.Defaults.Langs))

	for _, l := range c.Defaults.Langs {
		parsed, err := lang.Parse(string(l))
		if err != nil || parsed == core.LangAuto {
			if err == nil {
				err = errors.New("auto is valid only for a source language")
			}

			return fmt.Errorf("defaults.langs: %w", err)
		}
		if seen[parsed] {
			return fmt.Errorf("defaults.langs: duplicate target language %q", parsed)
		}

		seen[parsed] = true
		normalized = append(normalized, parsed)
	}
	if len(normalized) == 0 {
		return errors.New("defaults.langs: at least one target language is required")
	}

	c.Defaults.Langs = normalized

	return nil
}

// validateDaemon enforces the listener invariants.
func (c *Config) validateDaemon() error {
	if _, _, err := net.SplitHostPort(c.Daemon.HTTP); err != nil {
		return fmt.Errorf("daemon.http %q: %w", c.Daemon.HTTP, err)
	}

	if o := c.Daemon.CORSOrigin; o != "" && !validOrigin(o) {
		return fmt.Errorf(
			"daemon.cors_origin %q: must be an http(s) origin without path, query or credentials", o,
		)
	}

	return nil
}

func validOrigin(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}

	return u.Host != "" && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}

// validateProvider enforces the provider invariants.
func (c *Config) validateProvider() error {
	if err := validateDispatch(&c.Providers.Dispatch); err != nil {
		return err
	}

	if err := stageOneOf("providers.voices", &c.Providers.Voices, VoicesLocal, VoicesLocal, VoicesOff); err != nil {
		return err
	}
	if err := validateLocalSTT(&c.Providers.Local.STT); err != nil {
		return err
	}
	if err := validateTranslationPairs(c.Providers.Local.MT.Pairs); err != nil {
		return err
	}

	return c.validateLocalTTS()
}

func validateLocalSTT(stt *LocalSTT) error {
	if strings.TrimSpace(stt.Model) == "" {
		return errors.New("providers.local.stt.model: required")
	}
	if stt.CallModel != "" && strings.TrimSpace(stt.CallModel) == "" {
		return errors.New("providers.local.stt.call_model: must be a model path or omitted")
	}

	return nil
}

func (c *Config) validateLocalTTS() error {
	tts := &c.Providers.Local.TTS
	if c.Providers.Voices != VoicesLocal {
		return nil
	}
	if len(tts.Voices) == 0 {
		return errors.New(
			"providers.local.tts.voices: at least one voice is required when providers.voices is local",
		)
	}

	return validateVoiceModels(tts.Voices)
}

func validateVoiceModels(voices []VoiceModel) error {
	seen := make(map[core.Lang]bool, len(voices))
	for i := range voices {
		if strings.TrimSpace(voices[i].Voice) == "" {
			return fmt.Errorf("providers.local.tts.voices[%d].voice: required", i)
		}

		parsed, err := lang.Parse(strings.TrimSpace(string(voices[i].Language)))
		if err != nil {
			return fmt.Errorf("providers.local.tts.voices[%d].language: %w", i, err)
		}
		if parsed == core.LangAuto {
			return fmt.Errorf("providers.local.tts.voices[%d].language: must name a concrete language", i)
		}

		base := parsed.Base()
		if seen[base] {
			return fmt.Errorf("providers.local.tts.voices[%d]: duplicate voice language %s", i, base)
		}
		seen[base] = true
		voices[i].Language = parsed
	}

	return nil
}

func validateTranslationPairs(pairs []TranslationPair) error {
	seen := make(map[TranslationPair]bool, len(pairs))
	for i := range pairs {
		from, err := lang.Parse(string(pairs[i].From))
		if err != nil || from == core.LangAuto || from.Base() != from {
			return fmt.Errorf("providers.local.mt.pairs[%d].from %q: expected a base language", i, pairs[i].From)
		}
		to, err := lang.Parse(string(pairs[i].To))
		if err != nil || to == core.LangAuto || to.Base() != to {
			return fmt.Errorf("providers.local.mt.pairs[%d].to %q: expected a base language", i, pairs[i].To)
		}
		pair := TranslationPair{From: from, To: to}
		if from == to {
			return fmt.Errorf("providers.local.mt.pairs[%d]: %s to itself needs no model", i, from)
		}
		if seen[pair] {
			return fmt.Errorf("providers.local.mt.pairs[%d]: duplicate %s to %s", i, from, to)
		}
		seen[pair] = true
		pairs[i] = pair
	}

	return nil
}

// stageOneOf defaults an empty stage selector and rejects an unknown one.
func stageOneOf(field string, value *string, fallback string, allowed ...string) error {
	if *value == "" {
		*value = fallback

		return nil
	}

	if slices.Contains(allowed, *value) {
		return nil
	}

	return fmt.Errorf("%s %q: expected one of %s", field, *value, strings.Join(allowed, ", "))
}

const (
	maxDispatchWorkers = 256
	maxDispatchQueue   = 1 << 16
	maxActiveLanes     = 64
	maxStoredSessions  = 256
)

func validateDispatch(d *Dispatch) error {
	if d.Workers < 1 || d.Workers > maxDispatchWorkers {
		return fmt.Errorf("providers.dispatch.workers %d: expected 1 to %d", d.Workers, maxDispatchWorkers)
	}
	if d.Queue < 1 || d.Queue > maxDispatchQueue {
		return fmt.Errorf("providers.dispatch.queue %d: expected 1 to %d", d.Queue, maxDispatchQueue)
	}
	if d.MaxLanes < 1 || d.MaxLanes > maxActiveLanes {
		return fmt.Errorf("providers.dispatch.max_lanes %d: expected 1 to %d", d.MaxLanes, maxActiveLanes)
	}
	if d.MaxSessions < d.MaxLanes || d.MaxSessions > maxStoredSessions {
		return fmt.Errorf(
			"providers.dispatch.max_sessions %d: expected max_lanes (%d) to %d",
			d.MaxSessions, d.MaxLanes, maxStoredSessions,
		)
	}

	return nil
}

// normalizeDefaultSubs stores what session.ParseSubs returns, not the raw text.
func (c *Config) normalizeDefaultSubs() error {
	mode, err := session.ParseSubs(string(c.Defaults.Subs))
	if err != nil {
		return fmt.Errorf("defaults.subs: %w", err)
	}
	c.Defaults.Subs = mode

	return nil
}

func (c *Config) normalizeDefaultBed() error {
	level, err := session.ParseBed(string(c.Defaults.Bed))
	if err != nil {
		return fmt.Errorf("defaults.bed: %w", err)
	}
	c.Defaults.Bed = level

	return nil
}
