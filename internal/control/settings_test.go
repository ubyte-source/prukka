package control_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ubyte-source/prukka/internal/control"
	"github.com/ubyte-source/prukka/internal/core/config"
	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
)

// settingsFixture is one settings surface over a throwaway config file.
type settingsFixture struct {
	settings *control.Settings
	path     string
}

// newSettingsFixture builds the fixture from optional initial file content.
func newSettingsFixture(t *testing.T, body string) *settingsFixture {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	holder, err := config.NewHolder(path)
	if err != nil {
		t.Fatalf("NewHolder returned error: %v", err)
	}

	return &settingsFixture{settings: control.NewSettings(holder), path: path}
}

// newTestSettings hands service tests a working settings surface.
func newTestSettings(t *testing.T) *control.Settings {
	t.Helper()

	return newSettingsFixture(t, "").settings
}

func TestGetConfigReturnsLocalConfig(t *testing.T) {
	t.Parallel()

	fx := newSettingsFixture(t, `providers:
  local:
    tts:
      voices: [{language: en, voice: echo}, {language: it, voice: paola}]
`)

	reply, err := fx.settings.GetConfig(t.Context(), &v1.GetConfigRequest{})
	if err != nil {
		t.Fatalf("GetConfig returned error: %v", err)
	}

	if langs := reply.GetConfig().GetProviders().GetLocal().GetDubbedLangs(); len(langs) != 2 ||
		langs[0] != "en" || langs[1] != "it" {
		t.Fatalf("wire dubbed langs = %v, want [en it]", langs)
	}
	providers := reply.GetConfig().GetProviders()
	if providers.GetVoices() != config.VoicesLocal {
		t.Fatalf("wire voices = %q, want local", providers.GetVoices())
	}
	assertWireBidirectionalPairs(t, providers.GetLocal().GetMt().GetPairs())
}

// assertWireBidirectionalPairs: the wire must carry both translation directions.
func assertWireBidirectionalPairs(t *testing.T, pairs []*v1.TranslationPair) {
	t.Helper()

	if len(pairs) != 2 || pairs[0].GetFrom() != "it" || pairs[0].GetTo() != "en" ||
		pairs[1].GetFrom() != "en" || pairs[1].GetTo() != "it" {
		t.Fatalf("wire MT pairs = %+v, want it<->en both ways", pairs)
	}
}

func TestUpdateConfigPersistsWholeTransaction(t *testing.T) {
	t.Parallel()

	fx := newSettingsFixture(t, `providers:
  local:
    stt:
      call_model: models/stt/ggml-tiny-q5_1.bin
`)

	current, err := fx.settings.GetConfig(t.Context(), &v1.GetConfigRequest{})
	if err != nil {
		t.Fatalf("GetConfig returned error: %v", err)
	}

	edited := current.GetConfig()
	edited.Providers.Local.SttModel = "models/stt/large.bin"
	edited.Providers.Local.Mt = &v1.TranslationConfig{Pairs: []*v1.TranslationPair{
		{From: "en", To: "it"},
	}}
	edited.Defaults.Langs = []string{"it", "de"}

	reply, err := fx.settings.UpdateConfig(t.Context(), &v1.UpdateConfigRequest{Config: edited})
	if err != nil {
		t.Fatalf("UpdateConfig returned error: %v", err)
	}

	assertAppliedSettings(t, reply)

	persisted, err := config.Load(fx.path)
	if err != nil {
		t.Fatalf("Load after UpdateConfig: %v", err)
	}

	assertPersistedSettings(t, persisted)
}

func assertAppliedSettings(t *testing.T, reply *v1.UpdateConfigResponse) {
	t.Helper()

	if langs := reply.GetConfig().GetProviders().GetLocal().GetDubbedLangs(); len(langs) != 2 ||
		langs[0] != "en" || langs[1] != "it" {
		t.Fatalf("applied dubbed langs = %v, want the default [en it]", langs)
	}
	if len(reply.GetRestartRequired()) != 0 {
		t.Fatalf("restart notes = %v, want none for provider edits", reply.GetRestartRequired())
	}
}

func assertPersistedSettings(t *testing.T, persisted *config.Config) {
	t.Helper()
	assertPersistedLocalSettings(t, persisted)

	if len(persisted.Defaults.Langs) != 2 || persisted.Defaults.Langs[1] != "de" {
		t.Fatalf("persisted langs = %v, want [it de]", persisted.Defaults.Langs)
	}
}

func assertPersistedLocalSettings(t *testing.T, persisted *config.Config) {
	t.Helper()

	voices := persisted.Providers.Local.TTS.Voices
	if len(voices) != 2 || voices[0].Language != "en" || voices[1].Language != "it" {
		t.Fatalf("persisted voices = %+v, want the default voices preserved", voices)
	}
	if got := persisted.Providers.Local.STT.Model; got != "models/stt/large.bin" {
		t.Fatalf("persisted STT model = %q, want models/stt/large.bin", got)
	}
	if got := persisted.Providers.Local.STT.CallModel; got != "models/stt/ggml-tiny-q5_1.bin" {
		t.Fatalf("persisted call STT model = %q, want file-only override", got)
	}
	if pairs := persisted.Providers.Local.MT.Pairs; len(pairs) != 1 || pairs[0].From != "en" || pairs[0].To != "it" {
		t.Fatalf("persisted MT pairs = %+v, want en to it", pairs)
	}
}

func TestUpdateConfigRejectsInvalidEdits(t *testing.T) {
	t.Parallel()

	fx := newSettingsFixture(t, "")

	current, err := fx.settings.GetConfig(t.Context(), &v1.GetConfigRequest{})
	if err != nil {
		t.Fatalf("GetConfig returned error: %v", err)
	}

	edited := current.GetConfig()
	edited.Defaults.Subs = "srt"

	_, updateErr := fx.settings.UpdateConfig(t.Context(), &v1.UpdateConfigRequest{Config: edited})
	st, ok := status.FromError(updateErr)

	if !ok || st.Code() != codes.InvalidArgument || !strings.Contains(st.Message(), "defaults.subs") {
		t.Fatalf("UpdateConfig error = %v, want InvalidArgument naming defaults.subs", updateErr)
	}

	persisted, loadErr := config.Load(fx.path)
	if loadErr != nil {
		t.Fatalf("Load after rejected edit: %v", loadErr)
	}

	if persisted.Defaults.Subs != "vtt" {
		t.Fatalf("subs after rejected edit = %q, want untouched default", persisted.Defaults.Subs)
	}
}

// TestLocalConfigWireRejectsUnknownFields: these JSON keys name no LocalConfig
// field, so the gateway's strict unmarshaler fails the request at decode.
func TestLocalConfigWireRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	cases := []struct {
		key  string
		json string
	}{
		{key: "baseUrl", json: `{"baseUrl":"http://example.test"}`},
		{key: "sttBaseUrl", json: `{"sttBaseUrl":"http://example.test"}`},
		{key: "mtBaseUrl", json: `{"mtBaseUrl":"http://example.test"}`},
		{key: "mtModel", json: `{"mtModel":"m"}`},
		{key: "mtTemperature", json: `{"mtTemperature":0.2}`},
		{key: "ttsBaseUrl", json: `{"ttsBaseUrl":"http://example.test"}`},
		{key: "ttsModel", json: `{"ttsModel":"m"}`},
		{key: "ttsVoice", json: `{"ttsVoice":"v"}`},
		{key: "ttsVoices", json: `{"ttsVoices":{"en":"v"}}`},
		{key: "ttsLanguage", json: `{"ttsLanguage":"en"}`},
		{key: "timeoutSeconds", json: `{"timeoutSeconds":30}`},
	}

	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			t.Parallel()

			err := protojson.Unmarshal([]byte(tc.json), &v1.LocalConfig{})
			if err == nil || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("decode %s = %v, want an unknown-field error", tc.json, err)
			}
		})
	}
}

func TestSettingsChangeHookRunsOnlyAfterSuccessfulWrites(t *testing.T) {
	t.Parallel()

	fx := newSettingsFixture(t, "")
	changes := 0
	fx.settings.SetChangeHook(func(config.Transition) { changes++ })

	current, err := fx.settings.GetConfig(t.Context(), &v1.GetConfigRequest{})
	if err != nil {
		t.Fatalf("GetConfig returned error: %v", err)
	}
	edited := current.GetConfig()
	edited.Providers.Local.SttModel = "models/stt/large.bin"
	if _, err := fx.settings.UpdateConfig(t.Context(), &v1.UpdateConfigRequest{Config: edited}); err != nil {
		t.Fatalf("UpdateConfig returned error: %v", err)
	}
	if changes != 1 {
		t.Fatalf("change hook calls = %d, want 1", changes)
	}

	if _, err := fx.settings.UpdateConfig(t.Context(), &v1.UpdateConfigRequest{}); err == nil {
		t.Fatal("UpdateConfig accepted an empty request")
	}
	if changes != 1 {
		t.Fatalf("failed write changed hook calls to %d, want 1", changes)
	}
}

// TestUpdateConfigKeepsTheDelayAPartialDefaultsBlockOmits: delay 0 is a legal
// configured value, so an omitted delaySeconds must not read as a chosen zero.
func TestUpdateConfigKeepsTheDelayAPartialDefaultsBlockOmits(t *testing.T) {
	t.Parallel()

	fx := newSettingsFixture(t, `defaults:
  delay: 8s
`)

	edited := &v1.Config{}
	if err := protojson.Unmarshal(
		[]byte(`{"defaults":{"langs":["it","en"],"subs":"vtt","bed":"-15dB"}}`), edited,
	); err != nil {
		t.Fatalf("decode partial defaults: %v", err)
	}

	if _, err := fx.settings.UpdateConfig(t.Context(), &v1.UpdateConfigRequest{Config: edited}); err != nil {
		t.Fatalf("UpdateConfig returned error: %v", err)
	}

	persisted, err := config.Load(fx.path)
	if err != nil {
		t.Fatalf("Load after UpdateConfig: %v", err)
	}
	if got := persisted.Defaults.Delay.Std(); got != 8*time.Second {
		t.Fatalf("persisted delay = %v, want the file's 8s preserved", got)
	}
}

func TestUpdateConfigAppliesAnExplicitZeroDelay(t *testing.T) {
	t.Parallel()

	fx := newSettingsFixture(t, `defaults:
  delay: 8s
`)

	edited := &v1.Config{}
	if err := protojson.Unmarshal(
		[]byte(`{"defaults":{"langs":["it","en"],"subs":"vtt","bed":"-15dB","delaySeconds":0}}`), edited,
	); err != nil {
		t.Fatalf("decode zero delay: %v", err)
	}

	reply, err := fx.settings.UpdateConfig(t.Context(), &v1.UpdateConfigRequest{Config: edited})
	if err != nil {
		t.Fatalf("UpdateConfig returned error: %v", err)
	}
	if got := reply.GetConfig().GetDefaults().GetDelaySeconds(); got != 0 {
		t.Fatalf("applied delay = %v, want the explicit 0", got)
	}

	persisted, loadErr := config.Load(fx.path)
	if loadErr != nil {
		t.Fatalf("Load after UpdateConfig: %v", loadErr)
	}
	if got := persisted.Defaults.Delay.Std(); got != 0 {
		t.Fatalf("persisted delay = %v, want 0s", got)
	}
}
