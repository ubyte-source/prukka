package lane

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/core/session"
)

// assertCapability runs one capability case, canonicalizing the source hint
// through ApplyFlags exactly as the wire would.
func assertCapability(
	t *testing.T, validate func(*session.Session) error,
	name, source string, targets []core.Lang, wantErr string,
) {
	t.Helper()

	sess := &session.Session{Langs: targets}
	if source != "" {
		if flagErr := sess.ApplyFlags(map[string]string{session.FlagSource: source}); flagErr != nil {
			t.Fatalf("%s: ApplyFlags: %v", name, flagErr)
		}
	}

	err := validate(sess)
	if wantErr == "" && err != nil {
		t.Errorf("%s: unexpected error: %v", name, err)
	}
	if wantErr != "" && (err == nil || !strings.Contains(err.Error(), wantErr)) {
		t.Errorf("%s: error = %v, want %q", name, err, wantErr)
	}
}

func TestConfiguredSessionCapabilityChecksDirectedModels(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, config.Default()); err != nil {
		t.Fatalf("Save config: %v", err)
	}
	holder, err := config.NewHolder(path)
	if err != nil {
		t.Fatalf("NewHolder: %v", err)
	}
	validate := SessionCapability(holder)

	for _, tc := range []struct {
		name    string
		source  string
		wantErr string
		targets []core.Lang
	}{
		{name: "declared pair", source: "it-IT", targets: []core.Lang{"en"}},
		{name: "declared reverse pair", source: "en", targets: []core.Lang{"it"}},
		{name: "same language", source: "en", targets: []core.Lang{"en-US"}},
		{name: "auto deferred", targets: []core.Lang{"de"}},
		{name: "undeclared direction", source: "en", targets: []core.Lang{"de"}, wantErr: "en to de"},
	} {
		assertCapability(t, validate, tc.name, tc.source, tc.targets, tc.wantErr)
	}
}

// TestConfiguredSessionCapabilityBridgesThroughHub admits it->en->de, whose
// direct pair is absent but whose hub legs are installed, and rejects it->fr.
func TestConfiguredSessionCapabilityBridgesThroughHub(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.Providers.Local.MT.Pairs = []config.TranslationPair{
		{From: "it", To: "en"}, {From: "en", To: "it"},
		{From: "de", To: "en"}, {From: "en", To: "de"},
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save config: %v", err)
	}
	holder, err := config.NewHolder(path)
	if err != nil {
		t.Fatalf("NewHolder: %v", err)
	}
	validate := SessionCapability(holder)

	for _, tc := range []struct {
		name    string
		source  string
		wantErr string
		targets []core.Lang
	}{
		{name: "pinned source bridges it->en->de", source: "it", targets: []core.Lang{"de"}},
		{name: "pinned source bridges de->en->it", source: "de", targets: []core.Lang{"it"}},
		{name: "direct spoke from hub", source: "en", targets: []core.Lang{"de"}},
		{name: "no hub leg to french", source: "it", targets: []core.Lang{"fr"}, wantErr: "it to fr"},
		{name: "multi-target rejects the unbridged one", source: "it", targets: []core.Lang{"de", "fr"}, wantErr: "it to fr"},
	} {
		assertCapability(t, validate, tc.name, tc.source, tc.targets, tc.wantErr)
	}
}

// TestConfiguredDubbedLanguagesSpansEveryVoice: both call directions dub.
func TestConfiguredDubbedLanguagesSpansEveryVoice(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.Providers.Local.TTS.Voices = []config.VoiceModel{
		{Language: "en", Voice: "models/tts/en.onnx"},
		{Language: "it", Voice: "models/tts/it.onnx"},
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save config: %v", err)
	}
	holder, err := config.NewHolder(path)
	if err != nil {
		t.Fatalf("NewHolder: %v", err)
	}

	dubbed := DubbedLanguages(holder)
	sess := &session.Session{Langs: []core.Lang{"en", "it", "de"}}
	if got := dubbed(sess); len(got) != 2 || got[0] != "en" || got[1] != "it" {
		t.Fatalf("DubbedLanguages = %v, want [en it] (de has no voice)", got)
	}
}
