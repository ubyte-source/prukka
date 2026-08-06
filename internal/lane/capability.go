package lane

import (
	"fmt"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/core/session"
	"github.com/ubyte-source/prukka/internal/providers/pivot"
)

// DubbedLanguages reports which target languages a session actually voices
// under the live config; a target with no voice is captioned only.
func DubbedLanguages(holder *config.Holder) func(*session.Session) []core.Lang {
	return func(s *session.Session) []core.Lang {
		cfg := holder.Current()
		if cfg.Providers.Voices == config.VoicesOff {
			return nil
		}

		return supportedWarmVoices(s, configuredVoices(cfg))
	}
}

// SessionCapability rejects targets the installed engine cannot serve, before a
// lane is ever started.
func SessionCapability(holder *config.Holder) func(*session.Session) error {
	return func(s *session.Session) error {
		source := s.SourceLang
		if source == core.LangAuto {
			return nil
		}

		cfg := holder.Current()
		pairs := make(map[config.TranslationPair]bool, len(cfg.Providers.Local.MT.Pairs))
		for _, pair := range cfg.Providers.Local.MT.Pairs {
			pairs[pair] = true
		}
		// The runtime translator bridges through the English hub, so admission must
		// judge support the same way or it rejects the it->en->de sessions.
		direct := func(from, to core.Lang) bool {
			return pairs[config.TranslationPair{From: from, To: to}]
		}
		from := source.Base()
		for _, target := range s.Langs {
			to := target.Base()
			if from == to {
				continue
			}
			if !pivot.Supported(direct, pivot.English, from, to) {
				return fmt.Errorf("translation model unavailable for %s to %s", source, target)
			}
		}

		return nil
	}
}
