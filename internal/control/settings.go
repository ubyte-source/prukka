package control

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/core/session"

	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
)

// Settings implements the configuration RPCs over the config holder.
type Settings struct {
	holder *config.Holder
	change func(config.Transition)
}

// NewSettings wires the settings surface.
func NewSettings(holder *config.Holder) *Settings {
	return &Settings{holder: holder}
}

// SetChangeHook registers the daemon's live-reconfiguration signal; nil leaves
// standalone settings inert.
func (s *Settings) SetChangeHook(change func(config.Transition)) {
	s.change = change
}

// GetConfig implements prukka.v1.Control.
func (s *Settings) GetConfig(context.Context, *v1.GetConfigRequest) (*v1.GetConfigResponse, error) {
	return &v1.GetConfigResponse{Config: toProto(s.holder.Current())}, nil
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

	transition, err := s.holder.Update(func(c *config.Config) { applyProto(c, edited) })
	if err != nil {
		// A write failure is the daemon's environment, not the caller's edit.
		if errors.Is(err, config.ErrPersist) {
			return nil, status.Error(codes.Internal, err.Error())
		}

		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	s.changed(transition)

	return &v1.UpdateConfigResponse{
		Config:          toProto(transition.Current),
		RestartRequired: transition.Notes,
	}, nil
}

func (s *Settings) changed(t config.Transition) {
	if s.change != nil {
		s.change(t)
	}
}

// toProto renders the editable configuration onto the wire.
func toProto(c *config.Config) *v1.Config {
	l := &c.Providers.Local

	return &v1.Config{
		Providers: &v1.ProvidersConfig{
			Voices: new(c.Providers.Voices),
			Local: &v1.LocalConfig{
				SttModel:    l.STT.Model,
				DubbedLangs: langsToStrings(l.TTS.DubbedLanguages()),
				Mt:          translationConfigToProto(l.MT.Pairs),
			},
		},
		Defaults: &v1.DefaultsConfig{
			Langs:        langsToStrings(c.Defaults.Langs),
			Subs:         string(c.Defaults.Subs),
			Bed:          string(c.Defaults.Bed),
			DelaySeconds: new(c.Defaults.Delay.Std().Seconds()),
		},
	}
}

// applyProto copies the wire's editable fields onto the base config;
// fields the wire does not carry keep their file values.
func applyProto(c *config.Config, p *v1.Config) {
	applyProviders(c, p.GetProviders())
	applyDefaults(c, p.GetDefaults())
}

func applyProviders(c *config.Config, p *v1.ProvidersConfig) {
	if p == nil {
		return
	}

	if p.Voices != nil {
		c.Providers.Voices = p.GetVoices()
	}
	if l := p.GetLocal(); l != nil {
		dst := &c.Providers.Local
		dst.STT.Model = l.GetSttModel()
		// Voices are bundle-level, not editable through settings, so they keep
		// their file values; dubbed_langs is a read-only capability projection.
		if l.GetMt() != nil {
			dst.MT.Pairs = translationPairsFromProto(l.GetMt().GetPairs())
		}
	}
}

func translationConfigToProto(pairs []config.TranslationPair) *v1.TranslationConfig {
	wire := make([]*v1.TranslationPair, len(pairs))
	for i, pair := range pairs {
		wire[i] = &v1.TranslationPair{From: string(pair.From), To: string(pair.To)}
	}

	return &v1.TranslationConfig{Pairs: wire}
}

func translationPairsFromProto(pairs []*v1.TranslationPair) []config.TranslationPair {
	out := make([]config.TranslationPair, len(pairs))
	for i, pair := range pairs {
		out[i] = config.TranslationPair{From: core.Lang(pair.GetFrom()), To: core.Lang(pair.GetTo())}
	}

	return out
}

func applyDefaults(c *config.Config, d *v1.DefaultsConfig) {
	if d == nil {
		return
	}

	langs := make([]core.Lang, 0, len(d.GetLangs()))
	for _, tag := range d.GetLangs() {
		langs = append(langs, core.Lang(tag))
	}

	c.Defaults.Langs = langs
	c.Defaults.Subs = session.SubsMode(d.GetSubs())
	c.Defaults.Bed = session.BedMode(d.GetBed())
	// Zero is a legal delay, so only presence can tell a chosen 0 from a
	// partial block that never mentioned the field.
	if d.DelaySeconds != nil {
		c.Defaults.Delay = seconds(d.GetDelaySeconds())
	}
}

// seconds converts a wire float of seconds into a config duration.
func seconds(v float64) config.Duration {
	return config.Duration(time.Duration(v * float64(time.Second)))
}
