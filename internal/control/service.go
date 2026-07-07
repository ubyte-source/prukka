package control

import (
	"context"
	"fmt"
	"slices"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ubyte-source/prukka/internal/core/lang"
	"github.com/ubyte-source/prukka/internal/core/session"
	"github.com/ubyte-source/prukka/internal/doctor"

	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
)

// DoctorFunc runs environment checks for the Doctor RPC. It is injected so
// the control plane stays free of OS and provider concerns.
type DoctorFunc func() []doctor.Check

// RateFunc reports the daemon-wide cost rate in euros per hour; cmd/prukka wires it to the meter book.
type RateFunc func() float64

// Pusher starts an external push of one language's output; subs is the
// overlay mode (off, vtt or burn).
type Pusher interface {
	Push(session, lang, target, subs string) error
}

// SessionDefaults seeds fields the caller left unset on a new session.
type SessionDefaults struct {
	Subs             string
	Bed              string
	BudgetEURPerHour float64
	Delay            time.Duration
}

// DefaultsFunc supplies the live defaults, so a SIGHUP reload reaches the
// next created session.
type DefaultsFunc func() SessionDefaults

// Service implements the prukka.v1.Control gRPC surface over the session
// store; the configuration RPCs delegate to the settings surface.
type Service struct {
	v1.UnimplementedControlServer

	store    *session.Store
	doctor   DoctorFunc
	costRate RateFunc
	defaults DefaultsFunc
	pusher   Pusher
	settings *Settings
	started  time.Time
	version  string
}

// NewService wires the control service.
func NewService(
	store *session.Store, version string,
	doctorFn DoctorFunc, rate RateFunc, defaults DefaultsFunc, pusher Pusher, settings *Settings,
) *Service {
	return &Service{
		store:    store,
		version:  version,
		doctor:   doctorFn,
		costRate: rate,
		defaults: defaults,
		pusher:   pusher,
		settings: settings,
		started:  time.Now(),
	}
}

// GetConfig implements prukka.v1.Control via the settings surface.
func (s *Service) GetConfig(ctx context.Context, req *v1.GetConfigRequest) (*v1.GetConfigResponse, error) {
	return s.settings.GetConfig(ctx, req)
}

// UpdateConfig implements prukka.v1.Control via the settings surface.
func (s *Service) UpdateConfig(ctx context.Context, req *v1.UpdateConfigRequest) (*v1.UpdateConfigResponse, error) {
	return s.settings.UpdateConfig(ctx, req)
}

// SetKey implements prukka.v1.Control via the settings surface.
func (s *Service) SetKey(ctx context.Context, req *v1.SetKeyRequest) (*v1.SetKeyResponse, error) {
	return s.settings.SetKey(ctx, req)
}

// CreateSession implements prukka.v1.Control.
func (s *Service) CreateSession(
	_ context.Context, req *v1.CreateSessionRequest,
) (*v1.CreateSessionResponse, error) {
	sess, err := sessionFromProto(req.GetSession())
	if err != nil {
		return nil, err
	}

	s.applyDefaults(sess)

	if createErr := s.store.Create(sess); createErr != nil {
		return nil, statusFromStore(createErr)
	}

	return &v1.CreateSessionResponse{Session: sessionToProto(sess)}, nil
}

// UpdateSession implements prukka.v1.Control: hot add/remove of languages.
func (s *Service) UpdateSession(
	_ context.Context, req *v1.UpdateSessionRequest,
) (*v1.UpdateSessionResponse, error) {
	add, err := parseLangs(req.GetAddLangs())
	if err != nil {
		return nil, err
	}

	remove, removeErr := parseLangs(req.GetRemoveLangs())
	if removeErr != nil {
		return nil, removeErr
	}

	updated, updateErr := s.store.UpdateLangs(req.GetSlug(), add, remove)
	if updateErr != nil {
		return nil, statusFromStore(updateErr)
	}

	return &v1.UpdateSessionResponse{Session: sessionToProto(&updated)}, nil
}

// DeleteSession implements prukka.v1.Control.
func (s *Service) DeleteSession(
	_ context.Context, req *v1.DeleteSessionRequest,
) (*v1.DeleteSessionResponse, error) {
	if err := s.store.Delete(req.GetSlug()); err != nil {
		return nil, statusFromStore(err)
	}

	return &v1.DeleteSessionResponse{}, nil
}

// applyDefaults seeds unset fields from the live configuration; a zero
// broadcast delay means unset.
func (s *Service) applyDefaults(sess *session.Session) {
	d := s.defaults()

	if sess.BudgetEURPerHour == 0 {
		sess.BudgetEURPerHour = d.BudgetEURPerHour
	}

	if sess.Delay == 0 && sess.Profile == session.ProfileBroadcast {
		sess.Delay = d.Delay
	}

	if sess.Flags == nil {
		sess.Flags = map[string]string{}
	}

	if sess.Flags["subs"] == "" {
		sess.Flags["subs"] = d.Subs
	}

	if sess.Flags["bed"] == "" {
		sess.Flags["bed"] = d.Bed
	}
}

// ListSessions implements prukka.v1.Control.
func (s *Service) ListSessions(context.Context, *v1.ListSessionsRequest) (*v1.ListSessionsResponse, error) {
	stored := s.store.List()

	sessions := make([]*v1.Session, len(stored))
	for i := range stored {
		sessions[i] = sessionToProto(&stored[i])
	}

	return &v1.ListSessionsResponse{Sessions: sessions}, nil
}

// Push implements prukka.v1.Control, validating session and language
// before the media plane is asked to push.
func (s *Service) Push(_ context.Context, req *v1.PushRequest) (*v1.PushResponse, error) {
	sess, err := s.store.Get(req.GetSlug())
	if err != nil {
		return nil, statusFromStore(err)
	}

	target, parseErr := lang.Parse(req.GetLang())
	if parseErr != nil {
		return nil, status.Error(codes.InvalidArgument, parseErr.Error())
	}

	if !slices.Contains(sess.Langs, target) {
		return nil, status.Errorf(codes.FailedPrecondition,
			"session %q does not target %q", req.GetSlug(), target)
	}

	if req.GetTargetUrl() == "" {
		return nil, status.Error(codes.InvalidArgument, "target_url is required")
	}

	if pushErr := s.pusher.Push(req.GetSlug(), string(target), req.GetTargetUrl(), req.GetSubs()); pushErr != nil {
		return nil, status.Error(codes.FailedPrecondition, pushErr.Error())
	}

	return &v1.PushResponse{}, nil
}

// Stats implements prukka.v1.Control.
func (s *Service) Stats(context.Context, *v1.StatsRequest) (*v1.StatsResponse, error) {
	return &v1.StatsResponse{
		SessionsActive: int64(s.store.Count()),
		UptimeSeconds:  time.Since(s.started).Seconds(),
		Version:        s.version,
		CostEurPerHour: s.costRate(),
	}, nil
}

// Doctor implements prukka.v1.Control.
func (s *Service) Doctor(context.Context, *v1.DoctorRequest) (*v1.DoctorResponse, error) {
	checks := s.doctor()

	out := make([]*v1.Check, len(checks))
	for i, c := range checks {
		out[i] = &v1.Check{Name: c.Name, Status: string(c.Status), Detail: c.Detail}
	}

	return &v1.DoctorResponse{Checks: out}, nil
}

// ListLanguages implements prukka.v1.Control: the one registry, over the
// wire — dropdowns never hardcode languages.
func (*Service) ListLanguages(
	context.Context, *v1.ListLanguagesRequest,
) (*v1.ListLanguagesResponse, error) {
	registry := lang.All()

	out := make([]*v1.Language, len(registry))
	for i, l := range registry {
		out[i] = &v1.Language{
			Tag:    string(l.Tag),
			Name:   l.Name,
			Native: l.Native,
			Label:  l.Label(),
		}
	}

	return &v1.ListLanguagesResponse{Languages: out}, nil
}

// StreamEvents implements prukka.v1.Control, forwarding store events until
// the client disconnects.
func (s *Service) StreamEvents(_ *v1.StreamEventsRequest, stream v1.Control_StreamEventsServer) error {
	events := s.store.Subscribe(stream.Context())

	for e := range events {
		resp := &v1.StreamEventsResponse{Type: string(e.Type), Session: sessionToProto(&e.Session)}
		if err := stream.Send(resp); err != nil {
			return fmt.Errorf("send event: %w", err)
		}
	}

	return nil
}
