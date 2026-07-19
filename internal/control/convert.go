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
	"github.com/ubyte-source/prukka/internal/redact"

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

	sess := &session.Session{
		Slug:    p.GetSlug(),
		Profile: profile,
		Source:  core.SourceSpec{URL: p.GetSourceUrl()},
		Langs:   langs,
		Delay:   time.Duration(p.GetDelaySeconds() * float64(time.Second)),
	}
	// The open flag map is parsed exactly once, here; past this point the
	// media options are typed fields and no layer reads a string key.
	if flagsErr := sess.ApplyFlags(p.GetFlags()); flagsErr != nil {
		return nil, status.Error(codes.InvalidArgument, flagsErr.Error())
	}

	return sess, nil
}

// langsToStrings renders validated tags for the wire.
func langsToStrings(langs []core.Lang) []string {
	out := make([]string, 0, len(langs))
	for _, l := range langs {
		out = append(out, string(l))
	}

	return out
}

// sessionToProto mirrors a stored session back onto the wire.
func sessionToProto(s *session.Session) *v1.Session {
	langs := langsToStrings(s.Langs)

	runtime := s.Runtime()

	return &v1.Session{
		Slug:         s.Slug,
		Profile:      string(s.Profile),
		SourceLabel:  PublicSourceLabel(s.Source.URL),
		Langs:        langs,
		Flags:        s.FlagsWire(),
		DelaySeconds: new(s.Delay.Seconds()),
		Status:       string(runtime.State),
		Error:        runtime.Error,
	}
}

// PublicSourceLabel identifies a source without exposing credentials, stream
// keys, passphrases, local paths or device identifiers on read endpoints.
// The stripping itself is redact.Split's contract; this wrapper only picks
// the user-facing words: a local file reads "file://[local]", and a device
// source shows its class — the host — while the private identifier lives in
// the path Split already dropped.
func PublicSourceLabel(raw string) string {
	parts, ok := redact.Split(raw)
	switch {
	case !ok:
		return "[source]"
	case parts.Scheme == "file":
		return "file://[local]"
	case parts.Host == "":
		return parts.Scheme + "://[source]"
	default:
		return parts.Scheme + "://" + parts.Host
	}
}

func projectedDubbedLanguages(project DubbedLanguagesFunc, s *session.Session) []string {
	if project == nil {
		return []string{}
	}

	return langsToStrings(project(s))
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
	case errors.Is(err, session.ErrCapacity):
		return status.Error(codes.ResourceExhausted, err.Error())
	default:
		return status.Error(codes.InvalidArgument, err.Error())
	}
}
