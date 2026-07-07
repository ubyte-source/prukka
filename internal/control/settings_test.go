package control_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// TestGetConfigReturnsLocalConfig: the file's local engine tuning reaches the
// wire unchanged.
func TestGetConfigReturnsLocalConfig(t *testing.T) {
	t.Parallel()

	fx := newSettingsFixture(t, "providers:\n  local:\n    tts:\n      voice: echo\n")

	reply, err := fx.settings.GetConfig(t.Context(), &v1.GetConfigRequest{})
	if err != nil {
		t.Fatalf("GetConfig returned error: %v", err)
	}

	if got := reply.GetConfig().GetProviders().GetLocal().GetTtsVoice(); got != "echo" {
		t.Fatalf("wire TTS voice = %q, want echo", got)
	}
	providers := reply.GetConfig().GetProviders()
	if providers.GetVoices() != config.VoicesLocal {
		t.Fatalf("wire voices = %q, want local", providers.GetVoices())
	}
	pairs := providers.GetLocal().GetMt().GetPairs()
	if len(pairs) != 1 || pairs[0].GetFrom() != "it" || pairs[0].GetTo() != "en" {
		t.Fatalf("wire MT pairs = %+v, want it to en", pairs)
	}
}

// TestUpdateConfigPersistsWholeTransaction: an edit answers with the applied
// state and survives a fresh load from the file the daemon reads.
func TestUpdateConfigPersistsWholeTransaction(t *testing.T) {
	t.Parallel()

	fx := newSettingsFixture(t, "")

	current, err := fx.settings.GetConfig(t.Context(), &v1.GetConfigRequest{})
	if err != nil {
		t.Fatalf("GetConfig returned error: %v", err)
	}

	edited := current.GetConfig()
	edited.Providers.Local.TtsVoice = "nova"
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

	if got := reply.GetConfig().GetProviders().GetLocal().GetTtsVoice(); got != "nova" {
		t.Fatalf("applied TTS voice = %q, want nova", got)
	}
	if len(reply.GetRestartRequired()) != 0 {
		t.Fatalf("restart notes = %v, want none for provider edits", reply.GetRestartRequired())
	}
}

func assertPersistedSettings(t *testing.T, persisted *config.Config) {
	t.Helper()

	if persisted.Providers.Local.TTS.Voice != "nova" {
		t.Fatalf("persisted TTS voice = %q, want nova", persisted.Providers.Local.TTS.Voice)
	}
	if got := persisted.Providers.Local.STT.Model; got != "models/stt/large.bin" {
		t.Fatalf("persisted STT model = %q, want models/stt/large.bin", got)
	}
	if pairs := persisted.Providers.Local.MT.Pairs; len(pairs) != 1 || pairs[0].From != "en" || pairs[0].To != "it" {
		t.Fatalf("persisted MT pairs = %+v, want en to it", pairs)
	}
	if len(persisted.Defaults.Langs) != 2 || persisted.Defaults.Langs[1] != "de" {
		t.Fatalf("persisted langs = %v, want [it de]", persisted.Defaults.Langs)
	}
}

// TestUpdateConfigRejectsInvalidEdits: the transaction fails whole with a
// field-naming InvalidArgument and the file stays untouched.
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

func TestUpdateConfigRejectsRetiredLocalFields(t *testing.T) {
	t.Parallel()

	cases := []struct {
		field string
		json  string
	}{
		{field: "providers.local.base_url", json: `{"baseUrl":"http://legacy"}`},
		{field: "providers.local.stt_base_url", json: `{"sttBaseUrl":"http://legacy"}`},
		{field: "providers.local.mt_base_url", json: `{"mtBaseUrl":"http://legacy"}`},
		{field: "providers.local.mt_model", json: `{"mtModel":"legacy"}`},
		{field: "providers.local.mt_temperature", json: `{"mtTemperature":0.2}`},
		{field: "providers.local.tts_base_url", json: `{"ttsBaseUrl":"http://legacy"}`},
		{field: "providers.local.tts_model", json: `{"ttsModel":"legacy"}`},
		{field: "providers.local.timeout_seconds", json: `{"timeoutSeconds":30}`},
		{field: "providers.local.tts_voices", json: `{"ttsVoices":["legacy"]}`},
	}

	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()

			fx := newSettingsFixture(t, "")
			current, err := fx.settings.GetConfig(t.Context(), &v1.GetConfigRequest{})
			if err != nil {
				t.Fatalf("GetConfig returned error: %v", err)
			}
			local := current.GetConfig().GetProviders().GetLocal()
			if err := protojson.Unmarshal([]byte(tc.json), local); err != nil {
				t.Fatalf("decode legacy local config: %v", err)
			}

			_, updateErr := fx.settings.UpdateConfig(t.Context(), &v1.UpdateConfigRequest{Config: current.GetConfig()})
			if status.Code(updateErr) != codes.InvalidArgument || !strings.Contains(updateErr.Error(), tc.field) {
				t.Fatalf("UpdateConfig error = %v, want InvalidArgument naming %s", updateErr, tc.field)
			}
		})
	}
}

// TestSettingsChangeHookRunsOnlyAfterSuccessfulWrites: the live-reconfigure
// signal fires for an applied edit and never for a rejected one.
func TestSettingsChangeHookRunsOnlyAfterSuccessfulWrites(t *testing.T) {
	t.Parallel()

	fx := newSettingsFixture(t, "")
	changes := 0
	fx.settings.SetChangeHook(func() { changes++ })

	current, err := fx.settings.GetConfig(t.Context(), &v1.GetConfigRequest{})
	if err != nil {
		t.Fatalf("GetConfig returned error: %v", err)
	}
	edited := current.GetConfig()
	edited.Providers.Local.TtsVoice = "nova"
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
