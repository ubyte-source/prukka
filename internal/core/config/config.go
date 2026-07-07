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
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/lang"
)

// Config is the root daemon configuration.
type Config struct {
	Daemon    Daemon    `yaml:"daemon"`
	Control   Control   `yaml:"control"`
	Defaults  Defaults  `yaml:"defaults"`
	Providers Providers `yaml:"providers"`
	Budgets   Budgets   `yaml:"budgets"`
	Privacy   Privacy   `yaml:"privacy"`
}

// Daemon configures the HTTP/dashboard listener and the media-plane
// listen addresses.
type Daemon struct {
	HTTP string `yaml:"http"`
	// CORSOrigin is the one web origin allowed to drive this daemon from a
	// browser; empty disables cross-origin access.
	CORSOrigin string `yaml:"cors_origin"`
	Media      Media  `yaml:"media"`
}

// Media holds the media-plane listen addresses.
type Media struct {
	RTMP string `yaml:"rtmp"`
	SRT  string `yaml:"srt"`
}

// Control configures the control plane; Remote, when set, must be tls://
// with mandatory mTLS.
type Control struct {
	Remote string `yaml:"remote"`
	IPCTLS bool   `yaml:"ipc_tls"`
}

// Providers configures the AI providers: Backend selects openrouter or
// local (equals), Clone layers voice adaptation (off, pitch, cartesia).
type Providers struct {
	Backend    string     `yaml:"backend"`
	Clone      string     `yaml:"clone"`
	Local      Local      `yaml:"local"`
	Cartesia   Cartesia   `yaml:"cartesia"`
	OpenRouter OpenRouter `yaml:"openrouter"`
	Dispatch   Dispatch   `yaml:"dispatch"`
}

// backend names.
const (
	BackendOpenRouter = "openrouter"
	BackendLocal      = "local"
)

// KeyProviders names the providers whose API keys live in the OS keychain —
// the single list `prukka key`, the SetKey RPC and the dashboard share.
func KeyProviders() []string {
	return []string{"openrouter", "cartesia"}
}

// voice-adaptation modes: preset voices, in-engine register matching, or
// cloud timbre cloning.
const (
	CloneOff      = "off"
	ClonePitch    = "pitch"
	CloneCartesia = "cartesia"
)

// Local configures the on-machine OpenAI-compatible backend; each stage
// carries its own optional base URL, falling back to BaseURL.
type Local struct {
	BaseURL string   `yaml:"base_url"`
	STT     LocalSTT `yaml:"stt"`
	MT      LocalMT  `yaml:"mt"`
	TTS     LocalTTS `yaml:"tts"`
	Timeout Duration `yaml:"timeout"`
}

// LocalSTT selects the transcription server and model.
type LocalSTT struct {
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`
}

// LocalMT selects the translation server, model and sampling temperature.
type LocalMT struct {
	BaseURL     string  `yaml:"base_url"`
	Model       string  `yaml:"model"`
	Temperature float64 `yaml:"temperature"`
}

// LocalTTS selects the voice server, model, preset voice, wire format and the
// sample rate of the returned PCM.
type LocalTTS struct {
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`
	Voice   string `yaml:"voice"`
	Format  string `yaml:"format"`
	Rate    int    `yaml:"rate"`
}

// Cartesia configures the cloud timbre-cloning voice provider (used when
// Clone is "cartesia").
type Cartesia struct {
	Key     string   `yaml:"key"`
	BaseURL string   `yaml:"base_url"`
	Model   string   `yaml:"model"`
	Timeout Duration `yaml:"timeout"`
	Rate    int      `yaml:"rate"`
}

// Dispatch bounds the shared provider pool: Workers caps concurrency,
// Queue absorbs bursts; non-positive values select the defaults.
type Dispatch struct {
	Workers int `yaml:"workers"`
	Queue   int `yaml:"queue"`
}

// OpenRouter holds the API-key reference, endpoint and per-stage model
// choices; nothing in the codebase hardcodes them.
type OpenRouter struct {
	Key     string   `yaml:"key"`
	BaseURL string   `yaml:"base_url"`
	STT     Model    `yaml:"stt"`
	TTS     TTSModel `yaml:"tts"`
	MT      MTModel  `yaml:"mt"`
	Timeout Duration `yaml:"timeout"`
	// EURPerUSD converts OpenRouter's USD-denominated usage cost into the
	// euros the meter reports.
	EURPerUSD float64 `yaml:"eur_per_usd"`
}

// Model selects the model for a pipeline stage.
type Model struct {
	Model string `yaml:"model"`
}

// MTModel adds the glossary path and sampling temperature to the
// machine-translation model choice.
type MTModel struct {
	Model       string  `yaml:"model"`
	Glossary    string  `yaml:"glossary"`
	Temperature float64 `yaml:"temperature"`
}

// TTSModel adds the audio format to the text-to-speech model choice.
type TTSModel struct {
	Model  string `yaml:"model"`
	Format string `yaml:"format"`
}

// Defaults seeds new sessions.
type Defaults struct {
	Subs  string      `yaml:"subs"`
	Bed   string      `yaml:"bed"`
	Langs []core.Lang `yaml:"langs"`
	Delay Duration    `yaml:"delay"`
}

// Budgets caps provider spend.
type Budgets struct {
	PerSessionEURPerHour float64 `yaml:"per_session_eur_h"`
	HardStop             bool    `yaml:"hard_stop"`
}

// Privacy controls what is stored locally. Audio is never stored
// by default.
type Privacy struct {
	StoreTranscripts Duration `yaml:"store_transcripts"`
	StoreAudio       bool     `yaml:"store_audio"`
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
			Media:      Media{RTMP: ":1935", SRT: ":8890"},
		},
		Providers: Providers{
			Backend: BackendOpenRouter,
			Clone:   CloneOff,
			OpenRouter: OpenRouter{
				Key:     "keychain://prukka/openrouter",
				BaseURL: "https://openrouter.ai/api/v1",
				STT:     Model{Model: "openai/whisper-large-v3"},
				MT:      MTModel{Model: "google/gemini-2.5-flash", Temperature: 0.2},
				// gpt-audio-mini is the audio-output family OpenRouter routes.
				TTS:       TTSModel{Model: "openai/gpt-audio-mini", Format: "pcm16"},
				Timeout:   Duration(30 * time.Second),
				EURPerUSD: 1.0,
			},
			// The base URL points at Ollama's default; whisper and voice
			// servers usually listen elsewhere, so stt/tts carry their own.
			Local: Local{
				BaseURL: "http://127.0.0.1:11434/v1",
				STT:     LocalSTT{BaseURL: "http://127.0.0.1:8000/v1", Model: "whisper-1"},
				MT:      LocalMT{Model: "llama3.1", Temperature: 0.2},
				TTS:     LocalTTS{BaseURL: "http://127.0.0.1:8880/v1", Model: "tts-1", Voice: "alloy", Format: "pcm", Rate: 24000},
				Timeout: Duration(120 * time.Second),
			},
			Cartesia: Cartesia{
				Key:     "keychain://prukka/cartesia",
				BaseURL: "https://api.cartesia.ai",
				Model:   "sonic-3",
				Timeout: Duration(30 * time.Second),
				Rate:    24000,
			},
			Dispatch: Dispatch{Workers: 8, Queue: 256},
		},
		Defaults: Defaults{
			Langs: []core.Lang{"it", "en"},
			Subs:  "vtt",
			Bed:   "-15dB",
			Delay: Duration(8 * time.Second),
		},
		Budgets: Budgets{PerSessionEURPerHour: 3.0},
		Privacy: Privacy{StoreTranscripts: Duration(24 * time.Hour)},
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
	case errors.Is(err, fs.ErrNotExist) && !explicit:
		// No config file: built-in defaults apply.
	default:
		return nil, fmt.Errorf("read config: %w", err)
	}

	return cfg, nil
}

// decodeStrict parses YAML rejecting unknown fields, so config typos surface
// at startup instead of silently defaulting.
func decodeStrict(data []byte, cfg *Config) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
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
		{key: "PRUKKA_MEDIA_RTMP", apply: func(c *Config, v string) { c.Daemon.Media.RTMP = v }},
		{key: "PRUKKA_MEDIA_SRT", apply: func(c *Config, v string) { c.Daemon.Media.SRT = v }},
		{key: "PRUKKA_CONTROL_REMOTE", apply: func(c *Config, v string) { c.Control.Remote = v }},
		{key: "PRUKKA_OPENROUTER_KEY", apply: func(c *Config, v string) { c.Providers.OpenRouter.Key = v }},
		{key: "PRUKKA_CARTESIA_KEY", apply: func(c *Config, v string) { c.Providers.Cartesia.Key = v }},
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

	normalized := make([]core.Lang, 0, len(c.Defaults.Langs))

	for _, l := range c.Defaults.Langs {
		parsed, err := lang.Parse(string(l))
		if err != nil {
			return fmt.Errorf("defaults.langs: %w", err)
		}

		normalized = append(normalized, parsed)
	}

	c.Defaults.Langs = normalized

	if err := validateSubs(c.Defaults.Subs); err != nil {
		return err
	}

	if c.Control.Remote != "" && !strings.HasPrefix(c.Control.Remote, "tls://") {
		return fmt.Errorf("control.remote %q: only tls:// with mandatory mTLS is offered", c.Control.Remote)
	}

	if c.Defaults.Delay < 0 {
		return fmt.Errorf("defaults.delay %v: must not be negative", c.Defaults.Delay.Std())
	}

	return c.validateProvider()
}

// validateDaemon enforces the listener invariants.
func (c *Config) validateDaemon() error {
	if _, _, err := net.SplitHostPort(c.Daemon.HTTP); err != nil {
		return fmt.Errorf("daemon.http %q: %w", c.Daemon.HTTP, err)
	}

	if o := c.Daemon.CORSOrigin; o != "" && !strings.HasPrefix(o, "https://") && !strings.HasPrefix(o, "http://") {
		return fmt.Errorf("daemon.cors_origin %q: must be an http(s) origin or empty", o)
	}

	return nil
}

// validateProvider enforces the provider boundary invariants for the selected
// backend and the optional timbre-cloning provider.
func (c *Config) validateProvider() error {
	switch c.Providers.Backend {
	case "", BackendOpenRouter:
		c.Providers.Backend = BackendOpenRouter

		if err := c.validateOpenRouter(); err != nil {
			return err
		}
	case BackendLocal:
		if err := c.validateLocal(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("providers.backend %q: expected %s or %s",
			c.Providers.Backend, BackendOpenRouter, BackendLocal)
	}

	return c.validateClone()
}

// validateOpenRouter enforces the hosted backend's invariants.
func (c *Config) validateOpenRouter() error {
	or := &c.Providers.OpenRouter

	if err := validateURL("providers.openrouter.base_url", or.BaseURL); err != nil {
		return err
	}

	if or.Timeout <= 0 {
		return fmt.Errorf("providers.openrouter.timeout %v: must be positive", or.Timeout.Std())
	}

	if or.EURPerUSD <= 0 {
		return fmt.Errorf("providers.openrouter.eur_per_usd %v: must be positive", or.EURPerUSD)
	}

	return nil
}

// validateLocal enforces the local backend's invariants: http(s) URLs and
// a positive timeout.
func (c *Config) validateLocal() error {
	l := &c.Providers.Local

	if err := validateURL("providers.local.base_url", l.BaseURL); err != nil {
		return err
	}

	overrides := [][2]string{
		{"providers.local.stt.base_url", l.STT.BaseURL},
		{"providers.local.mt.base_url", l.MT.BaseURL},
		{"providers.local.tts.base_url", l.TTS.BaseURL},
	}

	for _, o := range overrides {
		if o[1] == "" {
			continue
		}

		if err := validateURL(o[0], o[1]); err != nil {
			return err
		}
	}

	if l.Timeout <= 0 {
		return fmt.Errorf("providers.local.timeout %v: must be positive", l.Timeout.Std())
	}

	return nil
}

// validateClone enforces the voice-adaptation invariants when a mode is
// selected. Pitch runs in-engine and needs nothing beyond the config default.
func (c *Config) validateClone() error {
	switch c.Providers.Clone {
	case "", CloneOff:
		c.Providers.Clone = CloneOff

		return nil
	case ClonePitch:
		return nil
	case CloneCartesia:
		car := &c.Providers.Cartesia

		if err := validateURL("providers.cartesia.base_url", car.BaseURL); err != nil {
			return err
		}

		if car.Timeout <= 0 {
			return fmt.Errorf("providers.cartesia.timeout %v: must be positive", car.Timeout.Std())
		}

		return nil
	default:
		return fmt.Errorf("providers.clone %q: expected %s, %s or %s",
			c.Providers.Clone, CloneOff, ClonePitch, CloneCartesia)
	}
}

// validateURL checks that raw is an http(s) URL, naming the field on error.
func validateURL(field, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%s %q: must be an http(s) URL", field, raw)
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
