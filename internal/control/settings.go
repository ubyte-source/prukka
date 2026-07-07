// The settings surface: the dashboard edits the configuration and stores
// provider keys; secrets are write-only and never return through any RPC.

package control

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/secret"

	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
)

// Keychain is the settings surface's port onto the OS keychain.
type Keychain interface {
	Store(ref, value string) error
	Resolve(ref string) (string, error)
}

// Settings implements the configuration RPCs over the config holder.
type Settings struct {
	holder *config.Holder
	keys   Keychain
}

// NewSettings wires the settings surface.
func NewSettings(holder *config.Holder, keys Keychain) *Settings {
	return &Settings{holder: holder, keys: keys}
}

// GetConfig implements prukka.v1.Control.
func (s *Settings) GetConfig(context.Context, *v1.GetConfigRequest) (*v1.GetConfigResponse, error) {
	return &v1.GetConfigResponse{Config: s.toProto(s.holder.Current())}, nil
}

// UpdateConfig implements prukka.v1.Control: one validated transaction from
// the wire onto the file and the live snapshot.
func (s *Settings) UpdateConfig(
	_ context.Context, req *v1.UpdateConfigRequest,
) (*v1.UpdateConfigResponse, error) {
	edited := req.GetConfig()
	if edited == nil {
		return nil, status.Error(codes.InvalidArgument, "config: required")
	}

	notes, err := s.holder.Update(func(c *config.Config) { applyProto(c, edited) })
	if err != nil {
		// A write failure is the daemon's environment, not the caller's edit;
		// validation errors name the offending field and go back verbatim.
		if errors.Is(err, config.ErrPersist) {
			return nil, status.Error(codes.Internal, err.Error())
		}

		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	return &v1.UpdateConfigResponse{
		Config:          s.toProto(s.holder.Current()),
		RestartRequired: notes,
	}, nil
}

// SetKey implements prukka.v1.Control: the key lands in the OS keychain
// behind the reference the config names, never in a file or a reply.
func (s *Settings) SetKey(_ context.Context, req *v1.SetKeyRequest) (*v1.SetKeyResponse, error) {
	provider := req.GetProvider()
	if !slices.Contains(config.KeyProviders(), provider) {
		return nil, status.Errorf(codes.InvalidArgument,
			"provider %q: known providers are %v", provider, config.KeyProviders())
	}

	if req.GetKey() == "" {
		return nil, status.Error(codes.InvalidArgument, "key: required")
	}

	ref, err := s.keyRef(provider)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	if storeErr := s.keys.Store(ref, req.GetKey()); storeErr != nil {
		return nil, status.Errorf(codes.Internal, "store key: %v", storeErr)
	}

	return &v1.SetKeyResponse{}, nil
}

// keyRef resolves where a provider's key lives, repointing raw or empty
// config values at the canonical keychain reference first.
func (s *Settings) keyRef(provider string) (string, error) {
	current := keyField(s.holder.Current(), provider)
	if secret.IsRef(current) {
		return current, nil
	}

	canonical := secret.Scheme + "prukka/" + provider

	if _, err := s.holder.Update(func(c *config.Config) {
		switch provider {
		case "openrouter":
			c.Providers.OpenRouter.Key = canonical
		case "cartesia":
			c.Providers.Cartesia.Key = canonical
		}
	}); err != nil {
		return "", fmt.Errorf("point config at the keychain: %w", err)
	}

	return canonical, nil
}

// keyField reads a provider's configured key value.
func keyField(c *config.Config, provider string) string {
	if provider == "cartesia" {
		return c.Providers.Cartesia.Key
	}

	return c.Providers.OpenRouter.Key
}

// keySet reports whether a provider key resolves to a non-empty secret —
// the boolean the dashboard shows instead of the secret itself.
func (s *Settings) keySet(ref string) bool {
	got, err := s.keys.Resolve(ref)

	return err == nil && got != ""
}

// toProto renders the editable configuration onto the wire, secrets
// reduced to key-set booleans.
func (s *Settings) toProto(c *config.Config) *v1.Config {
	or := &c.Providers.OpenRouter
	l := &c.Providers.Local
	car := &c.Providers.Cartesia

	return &v1.Config{
		Providers: &v1.ProvidersConfig{
			Backend: c.Providers.Backend,
			Clone:   c.Providers.Clone,
			Openrouter: &v1.OpenRouterConfig{
				BaseUrl:        or.BaseURL,
				SttModel:       or.STT.Model,
				MtModel:        or.MT.Model,
				MtTemperature:  or.MT.Temperature,
				TtsModel:       or.TTS.Model,
				EurPerUsd:      or.EURPerUSD,
				TimeoutSeconds: or.Timeout.Std().Seconds(),
				KeySet:         s.keySet(or.Key),
			},
			Local: &v1.LocalConfig{
				BaseUrl:        l.BaseURL,
				SttBaseUrl:     l.STT.BaseURL,
				SttModel:       l.STT.Model,
				MtBaseUrl:      l.MT.BaseURL,
				MtModel:        l.MT.Model,
				MtTemperature:  l.MT.Temperature,
				TtsBaseUrl:     l.TTS.BaseURL,
				TtsModel:       l.TTS.Model,
				TtsVoice:       l.TTS.Voice,
				TimeoutSeconds: l.Timeout.Std().Seconds(),
			},
			Cartesia: &v1.CartesiaConfig{
				BaseUrl:        car.BaseURL,
				Model:          car.Model,
				TimeoutSeconds: car.Timeout.Std().Seconds(),
				KeySet:         s.keySet(car.Key),
			},
		},
		Defaults: &v1.DefaultsConfig{
			Langs:        langsToStrings(c.Defaults.Langs),
			Subs:         c.Defaults.Subs,
			Bed:          c.Defaults.Bed,
			DelaySeconds: c.Defaults.Delay.Std().Seconds(),
		},
		Budgets: &v1.BudgetsConfig{
			PerSessionEurH: c.Budgets.PerSessionEURPerHour,
			HardStop:       c.Budgets.HardStop,
		},
		Privacy: &v1.PrivacyConfig{
			StoreTranscriptsHours: c.Privacy.StoreTranscripts.Std().Hours(),
			StoreAudio:            c.Privacy.StoreAudio,
		},
	}
}

// applyProto copies the wire's editable fields onto the base config;
// fields the wire does not carry keep their file values.
func applyProto(c *config.Config, p *v1.Config) {
	applyProviders(c, p.GetProviders())
	applyDefaults(c, p.GetDefaults())

	if b := p.GetBudgets(); b != nil {
		c.Budgets.PerSessionEURPerHour = b.GetPerSessionEurH()
		c.Budgets.HardStop = b.GetHardStop()
	}

	if pr := p.GetPrivacy(); pr != nil {
		c.Privacy.StoreTranscripts = config.Duration(time.Duration(pr.GetStoreTranscriptsHours() * float64(time.Hour)))
		c.Privacy.StoreAudio = pr.GetStoreAudio()
	}
}

// applyProviders copies the provider selection and tuning.
func applyProviders(c *config.Config, p *v1.ProvidersConfig) {
	if p == nil {
		return
	}

	c.Providers.Backend = p.GetBackend()
	c.Providers.Clone = p.GetClone()

	if or := p.GetOpenrouter(); or != nil {
		dst := &c.Providers.OpenRouter
		dst.BaseURL = or.GetBaseUrl()
		dst.STT.Model = or.GetSttModel()
		dst.MT.Model = or.GetMtModel()
		dst.MT.Temperature = or.GetMtTemperature()
		dst.TTS.Model = or.GetTtsModel()
		dst.EURPerUSD = or.GetEurPerUsd()
		dst.Timeout = seconds(or.GetTimeoutSeconds())
	}

	if l := p.GetLocal(); l != nil {
		dst := &c.Providers.Local
		dst.BaseURL = l.GetBaseUrl()
		dst.STT.BaseURL = l.GetSttBaseUrl()
		dst.STT.Model = l.GetSttModel()
		dst.MT.BaseURL = l.GetMtBaseUrl()
		dst.MT.Model = l.GetMtModel()
		dst.MT.Temperature = l.GetMtTemperature()
		dst.TTS.BaseURL = l.GetTtsBaseUrl()
		dst.TTS.Model = l.GetTtsModel()
		dst.TTS.Voice = l.GetTtsVoice()
		dst.Timeout = seconds(l.GetTimeoutSeconds())
	}

	if car := p.GetCartesia(); car != nil {
		dst := &c.Providers.Cartesia
		dst.BaseURL = car.GetBaseUrl()
		dst.Model = car.GetModel()
		dst.Timeout = seconds(car.GetTimeoutSeconds())
	}
}

// applyDefaults copies the session seeds.
func applyDefaults(c *config.Config, d *v1.DefaultsConfig) {
	if d == nil {
		return
	}

	langs := make([]core.Lang, 0, len(d.GetLangs()))
	for _, tag := range d.GetLangs() {
		langs = append(langs, core.Lang(tag))
	}

	c.Defaults.Langs = langs
	c.Defaults.Subs = d.GetSubs()
	c.Defaults.Bed = d.GetBed()
	c.Defaults.Delay = seconds(d.GetDelaySeconds())
}

// seconds converts a wire float of seconds into a config duration.
func seconds(v float64) config.Duration {
	return config.Duration(time.Duration(v * float64(time.Second)))
}

// langsToStrings renders validated tags for the wire.
func langsToStrings(langs []core.Lang) []string {
	out := make([]string, 0, len(langs))
	for _, l := range langs {
		out = append(out, string(l))
	}

	return out
}
