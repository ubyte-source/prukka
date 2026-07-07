package control_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ubyte-source/prukka/internal/control"
	"github.com/ubyte-source/prukka/internal/core/config"
	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
)

// fakeKeychain is an in-memory control.Keychain mirroring the real resolve
// contract: a non-reference value passes through as the secret itself.
type fakeKeychain struct {
	vals map[string]string
}

// Store implements control.Keychain.
func (f *fakeKeychain) Store(ref, value string) error {
	f.vals[ref] = value

	return nil
}

// Resolve implements control.Keychain.
func (f *fakeKeychain) Resolve(ref string) (string, error) {
	if !strings.HasPrefix(ref, "keychain://") {
		return ref, nil
	}

	return f.vals[ref], nil
}

// settingsFixture is one settings surface over a throwaway config file.
type settingsFixture struct {
	settings *control.Settings
	keys     *fakeKeychain
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

	keys := &fakeKeychain{vals: map[string]string{}}

	return &settingsFixture{settings: control.NewSettings(holder, keys), keys: keys, path: path}
}

// newTestSettings hands service tests a working settings surface.
func newTestSettings(t *testing.T) *control.Settings {
	t.Helper()

	return newSettingsFixture(t, "").settings
}

// TestGetConfigRedactsSecrets: a raw key in the file reaches the wire only
// as a key-set boolean — the secret itself never crosses the RPC.
func TestGetConfigRedactsSecrets(t *testing.T) {
	t.Parallel()

	fx := newSettingsFixture(t, "providers:\n  openrouter:\n    key: sk-raw-secret\n")

	reply, err := fx.settings.GetConfig(t.Context(), &v1.GetConfigRequest{})
	if err != nil {
		t.Fatalf("GetConfig returned error: %v", err)
	}

	or := reply.GetConfig().GetProviders().GetOpenrouter()
	if !or.GetKeySet() {
		t.Fatal("a configured raw key reports key_set=false")
	}

	if wire := reply.String(); strings.Contains(wire, "sk-raw-secret") {
		t.Fatalf("the secret leaked onto the wire: %s", wire)
	}

	// The default cartesia reference resolves to nothing yet.
	if reply.GetConfig().GetProviders().GetCartesia().GetKeySet() {
		t.Fatal("an unset cartesia key reports key_set=true")
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
	edited.Providers.Clone = config.ClonePitch
	edited.Budgets.PerSessionEurH = 7.5
	edited.Defaults.Langs = []string{"it", "de"}

	reply, err := fx.settings.UpdateConfig(t.Context(), &v1.UpdateConfigRequest{Config: edited})
	if err != nil {
		t.Fatalf("UpdateConfig returned error: %v", err)
	}

	if got := reply.GetConfig().GetProviders().GetClone(); got != config.ClonePitch {
		t.Fatalf("applied clone = %q, want pitch", got)
	}

	if len(reply.GetRestartRequired()) != 0 {
		t.Fatalf("restart notes = %v, want none for provider edits", reply.GetRestartRequired())
	}

	persisted, err := config.Load(fx.path)
	if err != nil {
		t.Fatalf("Load after UpdateConfig: %v", err)
	}

	if persisted.Providers.Clone != config.ClonePitch || persisted.Budgets.PerSessionEURPerHour != 7.5 {
		t.Fatalf("persisted = %q/%v, want pitch/7.5",
			persisted.Providers.Clone, persisted.Budgets.PerSessionEURPerHour)
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
	edited.Providers.Backend = "azure"

	_, updateErr := fx.settings.UpdateConfig(t.Context(), &v1.UpdateConfigRequest{Config: edited})
	st, ok := status.FromError(updateErr)

	if !ok || st.Code() != codes.InvalidArgument || !strings.Contains(st.Message(), "providers.backend") {
		t.Fatalf("UpdateConfig error = %v, want InvalidArgument naming providers.backend", updateErr)
	}

	persisted, loadErr := config.Load(fx.path)
	if loadErr != nil {
		t.Fatalf("Load after rejected edit: %v", loadErr)
	}

	if persisted.Providers.Backend != config.BackendOpenRouter {
		t.Fatalf("backend after rejected edit = %q, want untouched default", persisted.Providers.Backend)
	}
}

// TestSetKeyStoresBehindTheConfiguredReference: keychain gets the key,
// config gets the reference, the file never sees the secret.
func TestSetKeyStoresBehindTheConfiguredReference(t *testing.T) {
	t.Parallel()

	fx := newSettingsFixture(t, "providers:\n  cartesia:\n    key: \"\"\n")

	if _, err := fx.settings.SetKey(t.Context(), &v1.SetKeyRequest{
		Provider: "cartesia", Key: "sk_car_live",
	}); err != nil {
		t.Fatalf("SetKey returned error: %v", err)
	}

	if got := fx.keys.vals["keychain://prukka/cartesia"]; got != "sk_car_live" {
		t.Fatalf("keychain holds %q, want the stored key", got)
	}

	persisted, err := config.Load(fx.path)
	if err != nil {
		t.Fatalf("Load after SetKey: %v", err)
	}

	if persisted.Providers.Cartesia.Key != "keychain://prukka/cartesia" {
		t.Fatalf("config key = %q, want the canonical keychain reference", persisted.Providers.Cartesia.Key)
	}

	raw, err := os.ReadFile(fx.path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	if strings.Contains(string(raw), "sk_car_live") {
		t.Fatal("the key leaked into the config file")
	}

	reply, err := fx.settings.GetConfig(t.Context(), &v1.GetConfigRequest{})
	if err != nil {
		t.Fatalf("GetConfig returned error: %v", err)
	}

	if !reply.GetConfig().GetProviders().GetCartesia().GetKeySet() {
		t.Fatal("key_set stayed false after SetKey")
	}
}

// TestSetKeyRejectsUnknownProviderAndEmptyKey: both are caller errors named
// as InvalidArgument.
func TestSetKeyRejectsUnknownProviderAndEmptyKey(t *testing.T) {
	t.Parallel()

	fx := newSettingsFixture(t, "")

	_, err := fx.settings.SetKey(t.Context(), &v1.SetKeyRequest{Provider: "elevenlabs", Key: "x"})
	if st, ok := status.FromError(err); !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("unknown provider error = %v, want InvalidArgument", err)
	}

	_, err = fx.settings.SetKey(t.Context(), &v1.SetKeyRequest{Provider: "openrouter", Key: ""})
	if st, ok := status.FromError(err); !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("empty key error = %v, want InvalidArgument", err)
	}
}
