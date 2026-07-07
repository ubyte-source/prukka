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
)

// Config is the root daemon configuration.
type Config struct {
	Daemon     Daemon    `yaml:"daemon"`
	Control    Control   `yaml:"control"`
	deprecated []string  // retired fields accepted only for migration
	Defaults   Defaults  `yaml:"defaults"`
	Providers  Providers `yaml:"providers"`
}

// LaneFingerprint captures exactly the hot-reloadable configuration a running
// lane depends on. Dispatch limits are structural and intentionally excluded:
// changing them requires a daemon restart and must not restart lanes under the
// old limits. The rendered structs contain no maps, so %+v is stable within
// one process.
func (c *Config) LaneFingerprint() string {
	return fmt.Sprintf("%s|%+v|%s", c.Providers.Voices, c.Providers.Local, c.Defaults.Bed)
}

// legacyPrivacy accepts pre-release retention fields that were never wired to
// runtime behavior; loadFile drops this block immediately.
type legacyPrivacy struct {
	StoreTranscripts Duration `yaml:"store_transcripts"`
	StoreAudio       bool     `yaml:"store_audio"`
}

// Daemon configures the HTTP/dashboard listener.
type Daemon struct {
	LegacyMedia *legacyMedia `yaml:"media,omitempty"`
	HTTP        string       `yaml:"http"`
	// CORSOrigin is the one web origin allowed to drive this daemon from a
	// browser; empty disables cross-origin access.
	CORSOrigin string `yaml:"cors_origin"`
}

type legacyMedia struct {
	RTMP string `yaml:"rtmp"`
	SRT  string `yaml:"srt"`
}

// Control configures optional TLS on the local IPC transport.
type Control struct {
	LegacyRemote string `yaml:"remote,omitempty"`
	IPCTLS       bool   `yaml:"ipc_tls"`
}

// Providers configures the external local engine and the shared worker pool.
// Voices is local (dub) or off (subtitles only).
type Providers struct {
	Voices   string   `yaml:"voices"`
	Local    Local    `yaml:"local"`
	Dispatch Dispatch `yaml:"dispatch"`
}

// Voice-stage selectors.
const (
	VoicesLocal = "local"
	VoicesOff   = "off"

	defaultSTTModel = "models/stt/ggml-base.bin"
	defaultTTSVoice = "models/tts/en_US-lessac-medium.onnx"
)

// Local configures an operator-provided engine bundle. Bin is its executable;
// each stage invokes a dedicated subcommand. The remaining fields select the
// STT model and TTS voice passed to that executable.
type Local struct {
	Bin           string   `yaml:"bin"`
	LegacyBaseURL string   `yaml:"base_url,omitempty"`
	STT           LocalSTT `yaml:"stt"`
	MT            LocalMT  `yaml:"mt"`
	TTS           LocalTTS `yaml:"tts"`
	LegacyTimeout Duration `yaml:"timeout,omitempty"`
}

// LocalSTT selects the model path passed to the engine's STT subcommand.
type LocalSTT struct {
	LegacyBaseURL string `yaml:"base_url,omitempty"`
	Model         string `yaml:"model"`
}

// LocalTTS selects the Piper voice model and its supported language.
type LocalTTS struct {
	LegacyBaseURL string    `yaml:"base_url,omitempty"`
	LegacyModel   string    `yaml:"model,omitempty"`
	LegacyFormat  string    `yaml:"format,omitempty"`
	Language      core.Lang `yaml:"language"`
	Voice         string    `yaml:"voice"`
	LegacyVoices  []string  `yaml:"voices,omitempty"`
	LegacyRate    int       `yaml:"rate,omitempty"`
}

// LocalMT declares the translation models present in the external bundle.
// Retired remote-provider fields remain decode-only migration inputs.
type LocalMT struct {
	LegacyBaseURL     string            `yaml:"base_url,omitempty"`
	LegacyModel       string            `yaml:"model,omitempty"`
	Pairs             []TranslationPair `yaml:"pairs"`
	LegacyTemperature float64           `yaml:"temperature,omitempty"`
}

// TranslationPair identifies one installed source-to-target MT model.
type TranslationPair struct {
	From core.Lang `yaml:"from"`
	To   core.Lang `yaml:"to"`
}

// Dispatch bounds inference resources. Workers and Queue govern MT/TTS calls;
// MaxLanes caps live STT helpers, while MaxSessions bounds registered and
// waiting definitions so clients cannot create an unbounded admission queue.
type Dispatch struct {
	Workers     int `yaml:"workers"`
	Queue       int `yaml:"queue"`
	MaxLanes    int `yaml:"max_lanes"`
	MaxSessions int `yaml:"max_sessions"`
}

// Defaults seeds new sessions.
type Defaults struct {
	Subs  string      `yaml:"subs"`
	Bed   string      `yaml:"bed"`
	Langs []core.Lang `yaml:"langs"`
	Delay Duration    `yaml:"delay"`
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

// MarshalYAML implements yaml.Marshaler, writing the human form ("8s") the
// unmarshaler reads — without it a saved config would hold raw nanoseconds.
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
			Local: Local{
				STT: LocalSTT{Model: defaultSTTModel},
				MT:  LocalMT{Pairs: []TranslationPair{{From: "it", To: "en"}}},
				TTS: LocalTTS{Language: "en", Voice: defaultTTSVoice},
			},
			Dispatch: Dispatch{Workers: 8, Queue: 256, MaxLanes: 4, MaxSessions: 32},
		},
		Defaults: Defaults{
			Langs: []core.Lang{"it", "en"},
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

// loadFile reads defaults plus the file layer only — no environment, no
// validation — so settings edits never bake env overrides onto disk.
func loadFile(path string) (*Config, error) {
	cfg := Default()

	explicit := path != ""
	if !explicit {
		path = DefaultPath()
	}

	data, err := os.ReadFile(filepath.Clean(path))

	switch {
	case err == nil:
		if decodeErr := decodeStrict(data, cfg); decodeErr != nil {
			return nil, fmt.Errorf("parse %s: %w", path, decodeErr)
		}
		cfg.collectDeprecations()
	case errors.Is(err, fs.ErrNotExist) && !explicit:
		// No config file: built-in defaults apply.
	default:
		return nil, fmt.Errorf("read config: %w", err)
	}

	return cfg, nil
}

// Deprecations reports retired pre-release fields accepted only so existing
// installations can start and rewrite a clean configuration.
func (c *Config) Deprecations() []string {
	return append([]string(nil), c.deprecated...)
}

func (c *Config) collectDeprecations() {
	if c.Daemon.LegacyMedia != nil {
		c.deprecated = append(c.deprecated, "daemon.media")
		c.Daemon.LegacyMedia = nil
	}
	if c.Control.LegacyRemote != "" {
		c.deprecated = append(c.deprecated, "control.remote")
		c.Control.LegacyRemote = ""
	}
	c.collectLocalDeprecations()
}

func (c *Config) collectLocalDeprecations() {
	local := &c.Providers.Local
	if local.LegacyBaseURL != "" {
		c.deprecated = append(c.deprecated, "providers.local.base_url")
		local.LegacyBaseURL = ""
	}
	c.collectMTDeprecations(&local.MT)
	if local.LegacyTimeout != 0 {
		c.deprecated = append(c.deprecated, "providers.local.timeout")
		local.LegacyTimeout = 0
	}
	c.collectSTTDeprecations(&local.STT)
	c.collectTTSDeprecations(&local.TTS)
}

func (c *Config) collectMTDeprecations(mt *LocalMT) {
	if mt.LegacyBaseURL == "" && mt.LegacyModel == "" && mt.LegacyTemperature == 0 {
		return
	}

	c.deprecated = append(c.deprecated, "providers.local.mt legacy tuning")
	mt.LegacyBaseURL, mt.LegacyModel, mt.LegacyTemperature = "", "", 0
}

func (c *Config) collectSTTDeprecations(stt *LocalSTT) {
	if stt.LegacyBaseURL != "" {
		c.deprecated = append(c.deprecated, "providers.local.stt.base_url")
		stt.LegacyBaseURL = ""
	}
	if stt.Model == "whisper-1" {
		c.deprecated = append(c.deprecated, "providers.local.stt.model=whisper-1")
		stt.Model = defaultSTTModel
	}
}

func (c *Config) collectTTSDeprecations(tts *LocalTTS) {
	if tts.LegacyBaseURL != "" || tts.LegacyModel != "" || len(tts.LegacyVoices) != 0 ||
		tts.LegacyFormat != "" || tts.LegacyRate != 0 {
		c.deprecated = append(c.deprecated, "providers.local.tts legacy tuning")
		tts.LegacyBaseURL, tts.LegacyModel, tts.LegacyFormat = "", "", ""
		tts.LegacyVoices = nil
		tts.LegacyRate = 0
	}
	if tts.Voice == "alloy" {
		c.deprecated = append(c.deprecated, "providers.local.tts.voice=alloy")
		tts.Voice = defaultTTSVoice
	}
}

// decodeStrict parses YAML rejecting unknown fields, so config typos surface
// at startup instead of silently defaulting.
func decodeStrict(data []byte, cfg *Config) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	wire := struct {
		*Config `yaml:",inline"`

		LegacyPrivacy *legacyPrivacy `yaml:"privacy"`
	}{Config: cfg}

	if err := dec.Decode(&wire); err != nil && !errors.Is(err, io.EOF) {
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

// applyEnv layers PRUKKA_* variables over the file values. Flags
// layer over both in cmd/prukka. The getter is injected for testability.
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
	if err := validateSubs(c.Defaults.Subs); err != nil {
		return err
	}
	if err := validateBed(c.Defaults.Bed); err != nil {
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

// validateProvider enforces the local-only provider boundary invariants: the
// shared worker pool and the voice-stage selector.
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

	return nil
}

func (c *Config) validateLocalTTS() error {
	if c.Providers.Voices == VoicesLocal && strings.TrimSpace(c.Providers.Local.TTS.Voice) == "" {
		return errors.New("providers.local.tts.voice: required when providers.voices is local")
	}

	ttsLanguage := strings.TrimSpace(string(c.Providers.Local.TTS.Language))
	if ttsLanguage == "" {
		if c.Providers.Voices == VoicesLocal {
			return errors.New("providers.local.tts.language: required when providers.voices is local")
		}

		return nil
	}

	parsedTTSLanguage, err := lang.Parse(ttsLanguage)
	if err != nil {
		return fmt.Errorf("providers.local.tts.language: %w", err)
	}
	if parsedTTSLanguage == core.LangAuto {
		return errors.New("providers.local.tts.language: must name a concrete language")
	}
	c.Providers.Local.TTS.Language = parsedTTSLanguage

	return nil
}

func validateTranslationPairs(pairs []TranslationPair) error {
	seen := make(map[TranslationPair]bool, len(pairs))
	for i := range pairs {
		from, err := lang.Parse(string(pairs[i].From))
		if err != nil || from == core.LangAuto || strings.Contains(string(from), "-") {
			return fmt.Errorf("providers.local.mt.pairs[%d].from %q: expected a base language", i, pairs[i].From)
		}
		to, err := lang.Parse(string(pairs[i].To))
		if err != nil || to == core.LangAuto || strings.Contains(string(to), "-") {
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

// validateSubs checks the subtitle mode flag shared by config and endpoints.
func validateSubs(mode string) error {
	switch mode {
	case "off", "vtt", "burn":
		return nil
	default:
		return fmt.Errorf("defaults.subs %q: expected off, vtt or burn", mode)
	}
}

func validateBed(raw string) error {
	if _, err := core.BedLevel(raw); err != nil {
		return fmt.Errorf("defaults.bed: %w", err)
	}

	return nil
}
