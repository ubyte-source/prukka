package control

import (
	"errors"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/lang"
	"github.com/ubyte-source/prukka/internal/core/session"

	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
)

// sessionFromProto validates a wire session at the trust boundary:
// everything past here holds validated types.
func sessionFromProto(p *v1.Session) (*session.Session, error) {
	if p == nil {
		return nil, status.Error(codes.InvalidArgument, "session is required")
	}

	profile, err := session.ParseProfile(p.GetProfile())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	langs, langErr := parseLangs(p.GetLangs())
	if langErr != nil {
		return nil, langErr
	}

	voices := make(map[string]core.Voice, len(p.GetVoiceMap()))
	for track, voice := range p.GetVoiceMap() {
		voices[track] = core.Voice{ID: voice}
	}

	return &session.Session{
		Slug:             p.GetSlug(),
		Profile:          profile,
		Source:           core.SourceSpec{URL: p.GetSourceUrl()},
		Langs:            langs,
		VoiceMap:         voices,
		Flags:            p.GetFlags(),
		BudgetEURPerHour: p.GetBudgetEurPerHour(),
		Delay:            time.Duration(p.GetDelaySeconds() * float64(time.Second)),
	}, nil
}

// sessionToProto mirrors a stored session back onto the wire.
func sessionToProto(s *session.Session) *v1.Session {
	langs := make([]string, len(s.Langs))
	for i, l := range s.Langs {
		langs[i] = string(l)
	}

	voices := make(map[string]string, len(s.VoiceMap))
	for track, v := range s.VoiceMap {
		voices[track] = v.ID
	}

	return &v1.Session{
		Slug:             s.Slug,
		Profile:          string(s.Profile),
		SourceUrl:        s.Source.URL,
		Langs:            langs,
		VoiceMap:         voices,
		Flags:            s.Flags,
		BudgetEurPerHour: s.BudgetEURPerHour,
		DelaySeconds:     s.Delay.Seconds(),
	}
}

// parseLangs funnels wire tags through the single registry validator.
func parseLangs(tags []string) ([]core.Lang, error) {
	parsed, err := lang.ParseList(strings.Join(tags, ","))
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	return parsed, nil
}

// statusFromStore maps store sentinels onto gRPC codes.
func statusFromStore(err error) error {
	switch {
	case errors.Is(err, session.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, session.ErrExists):
		return status.Error(codes.AlreadyExists, err.Error())
	default:
		return status.Error(codes.InvalidArgument, err.Error())
	}
}
