package control

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"

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
	if protoFieldHasValue(p.ProtoReflect(), "voice_map") {
		return nil, status.Error(
			codes.InvalidArgument,
			"session.voice_map: retired; configure providers.local.tts.voice instead",
		)
	}

	profile, err := session.ParseProfile(p.GetProfile())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	langs, langErr := parseLangs(p.GetLangs())
	if langErr != nil {
		return nil, langErr
	}

	return &session.Session{
		Slug:    p.GetSlug(),
		Profile: profile,
		Source:  core.SourceSpec{URL: p.GetSourceUrl()},
		Langs:   langs,
		Flags:   p.GetFlags(),
		Delay:   time.Duration(p.GetDelaySeconds() * float64(time.Second)),
	}, nil
}

func protoFieldHasValue(message protoreflect.Message, name protoreflect.Name) bool {
	field := message.Descriptor().Fields().ByName(name)

	return field != nil && message.Has(field)
}

// sessionToProto mirrors a stored session back onto the wire.
func sessionToProto(s *session.Session) *v1.Session {
	langs := make([]string, len(s.Langs))
	for i, l := range s.Langs {
		langs[i] = string(l)
	}

	runtime := s.Runtime()

	return &v1.Session{
		Slug:         s.Slug,
		Profile:      string(s.Profile),
		SourceLabel:  PublicSourceLabel(s.Source.URL),
		Langs:        langs,
		Flags:        s.Flags,
		DelaySeconds: new(s.Delay.Seconds()),
		Status:       string(runtime.State),
		Error:        runtime.Error,
	}
}

// PublicSourceLabel identifies a source without exposing credentials, stream
// keys, passphrases, local paths or device identifiers on read endpoints.
func PublicSourceLabel(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" {
		return "[source]"
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme == "file" {
		return "file://[local]"
	}
	if parsed.Host == "" {
		return scheme + "://[source]"
	}

	return scheme + "://" + parsed.Host
}

func projectedDubbedLanguages(project DubbedLanguagesFunc, s *session.Session) []string {
	if project == nil {
		return []string{}
	}

	langs := project(s)
	out := make([]string, len(langs))
	for i, tag := range langs {
		out[i] = string(tag)
	}

	return out
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
