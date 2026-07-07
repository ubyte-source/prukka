// The settings surface: the dashboard reads and edits the daemon
// configuration through one validated transaction.

package control

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/config"

	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
)

// Settings implements the configuration RPCs over the config holder.
type Settings struct {
	holder *config.Holder
	change func()
}

// NewSettings wires the settings surface.
func NewSettings(holder *config.Holder) *Settings {
	return &Settings{holder: holder}
}

// SetChangeHook registers the daemon's live-reconfiguration signal. Wiring
// calls it once before the server starts; nil leaves standalone settings inert.
func (s *Settings) SetChangeHook(change func()) {
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
	if err := rejectDeprecatedLocalConfig(edited); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
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
	s.changed()

	return &v1.UpdateConfigResponse{
		Config:          toProto(s.holder.Current()),
		RestartRequired: notes,
	}, nil
}

func (s *Settings) changed() {
	if s.change != nil {
		s.change()
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
				TtsVoice:    l.TTS.Voice,
				TtsLanguage: new(string(l.TTS.Language)),
				Mt:          translationConfigToProto(l.MT.Pairs),
			},
		},
		Defaults: &v1.DefaultsConfig{
			Langs:        langsToStrings(c.Defaults.Langs),
			Subs:         c.Defaults.Subs,
			Bed:          c.Defaults.Bed,
			DelaySeconds: c.Defaults.Delay.Std().Seconds(),
		},
	}
}

// applyProto copies the wire's editable fields onto the base config;
// fields the wire does not carry keep their file values.
func applyProto(c *config.Config, p *v1.Config) {
	applyProviders(c, p.GetProviders())
	applyDefaults(c, p.GetDefaults())
}

// applyProviders copies the local engine tuning.
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
		dst.TTS.Voice = l.GetTtsVoice()
		if l.TtsLanguage != nil {
			dst.TTS.Language = core.Lang(l.GetTtsLanguage())
		}
		if l.GetMt() != nil {
			dst.MT.Pairs = translationPairsFromProto(l.GetMt().GetPairs())
		}
	}
}

// rejectDeprecatedLocalConfig prevents old v1 clients from receiving a false
// success for remote-provider settings the stdio engine cannot apply. Proto3
// cannot distinguish an absent scalar from its default, so default values stay
// valid while every effective legacy value fails before the transaction.
func rejectDeprecatedLocalConfig(p *v1.Config) error {
	local := p.GetProviders().GetLocal()
	if local == nil {
		return nil
	}

	retiredFields := []struct {
		configName string
		protoName  protoreflect.Name
	}{
		{configName: "providers.local.base_url", protoName: "base_url"},
		{configName: "providers.local.stt_base_url", protoName: "stt_base_url"},
		{configName: "providers.local.mt_base_url", protoName: "mt_base_url"},
		{configName: "providers.local.mt_model", protoName: "mt_model"},
		{configName: "providers.local.mt_temperature", protoName: "mt_temperature"},
		{configName: "providers.local.tts_base_url", protoName: "tts_base_url"},
		{configName: "providers.local.tts_model", protoName: "tts_model"},
		{configName: "providers.local.timeout_seconds", protoName: "timeout_seconds"},
		{configName: "providers.local.tts_voices", protoName: "tts_voices"},
	}
	message := local.ProtoReflect()
	for _, retired := range retiredFields {
		if protoFieldHasValue(message, retired.protoName) {
			return fmt.Errorf("%s: retired; remove this field", retired.configName)
		}
	}

	return nil
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
