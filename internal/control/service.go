package control

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/lang"
	"github.com/ubyte-source/prukka/internal/core/session"
	"github.com/ubyte-source/prukka/internal/doctor"
	"github.com/ubyte-source/prukka/internal/media/discover"

	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
)

// DoctorFunc runs environment checks for the Doctor RPC. It is injected so
// the control plane stays free of OS and provider concerns.
type DoctorFunc func() []doctor.Check

// Pusher starts an external push of one language's output; subs is the
// overlay mode (off, vtt or burn).
type Pusher interface {
	Push(session, lang, target, subs string) error
}

// SessionDefaults seeds fields the caller left unset on a new session.
type SessionDefaults struct {
	Subs  string
	Bed   string
	Langs []core.Lang
	Delay time.Duration
}

// DefaultsFunc supplies the live defaults, so a SIGHUP reload reaches the
// next created session.
type DefaultsFunc func() SessionDefaults

// DevicesFunc enumerates the machine's media devices for pickers;
// cmd/prukka wires it to the discovery layer so the control plane stays
// free of OS concerns. Nil means no enumeration on this build.
type DevicesFunc func(ctx context.Context) []discover.Device

// DubbedLanguagesFunc projects the configured voice capability onto a
// session's requested targets. Nil means dubbing is unavailable.
type DubbedLanguagesFunc func(*session.Session) []core.Lang

// SessionCapabilityFunc rejects a valid-looking session that the configured
// external engine cannot execute. Nil accepts every definition.
type SessionCapabilityFunc func(*session.Session) error

var errSessionCapability = errors.New("session exceeds configured provider capability")

// Service implements the prukka.v1.Control gRPC surface over the session
// store; the configuration RPCs delegate to the settings surface.
type Service struct {
	v1.UnimplementedControlServer

	store    *session.Store
	doctor   DoctorFunc
	defaults DefaultsFunc
	devices  DevicesFunc
	dubbed   DubbedLanguagesFunc
	capable  SessionCapabilityFunc
	pusher   Pusher
	settings *Settings
	engine   *Engine
	started  time.Time
	version  string
}

// NewService wires the control service.
func NewService(
	store *session.Store, version string,
	doctorFn DoctorFunc, defaults DefaultsFunc, devices DevicesFunc,
	dubbed DubbedLanguagesFunc, capable SessionCapabilityFunc, pusher Pusher, settings *Settings,
	engine *Engine,
) *Service {
	return &Service{
		store:    store,
		version:  version,
		doctor:   doctorFn,
		defaults: defaults,
		devices:  devices,
		dubbed:   dubbed,
		capable:  capable,
		pusher:   pusher,
		settings: settings,
		engine:   engine,
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

// GetEngine implements prukka.v1.Control via the engine surface.
func (s *Service) GetEngine(ctx context.Context, req *v1.GetEngineRequest) (*v1.GetEngineResponse, error) {
	return s.engine.GetEngine(ctx, req)
}

// InstallEngineRuntime implements prukka.v1.Control via the engine surface.
func (s *Service) InstallEngineRuntime(
	ctx context.Context, req *v1.InstallEngineRuntimeRequest,
) (*v1.InstallEngineRuntimeResponse, error) {
	return s.engine.InstallEngineRuntime(ctx, req)
}

// InstallEnginePack implements prukka.v1.Control via the engine surface.
func (s *Service) InstallEnginePack(
	ctx context.Context, req *v1.InstallEnginePackRequest,
) (*v1.InstallEnginePackResponse, error) {
	return s.engine.InstallEnginePack(ctx, req)
}

// RemoveEnginePack implements prukka.v1.Control via the engine surface.
func (s *Service) RemoveEnginePack(
	ctx context.Context, req *v1.RemoveEnginePackRequest,
) (*v1.RemoveEnginePackResponse, error) {
	return s.engine.RemoveEnginePack(ctx, req)
}

// CreateSession implements prukka.v1.Control.
func (s *Service) CreateSession(
	_ context.Context, req *v1.CreateSessionRequest,
) (*v1.CreateSessionResponse, error) {
	wire := req.GetSession()
	sess, err := sessionFromProto(wire)
	if err != nil {
		return nil, err
	}

	s.applyDefaults(sess, wire)
	if s.capable != nil {
		if capabilityErr := s.capable(sess); capabilityErr != nil {
			return nil, status.Error(codes.FailedPrecondition, capabilityErr.Error())
		}
	}

	if createErr := s.store.Create(sess); createErr != nil {
		return nil, statusFromStore(createErr)
	}

	created := s.projectSession(sess)
	created.Status = string(session.StateStarting)

	return &v1.CreateSessionResponse{Session: created}, nil
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

	var updated session.Session
	var updateErr error
	if s.capable == nil {
		updated, updateErr = s.store.UpdateLangs(req.GetSlug(), add, remove)
	} else {
		updated, updateErr = s.store.UpdateLangsChecked(
			req.GetSlug(), add, remove,
			func(candidate session.Session) error {
				if capabilityErr := s.capable(&candidate); capabilityErr != nil {
					return fmt.Errorf("%w: %w", errSessionCapability, capabilityErr)
				}

				return nil
			},
		)
	}
	if updateErr != nil {
		if errors.Is(updateErr, errSessionCapability) {
			return nil, status.Error(codes.FailedPrecondition, updateErr.Error())
		}
		return nil, statusFromStore(updateErr)
	}

	return &v1.UpdateSessionResponse{Session: s.projectSession(&updated)}, nil
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

// applyDefaults seeds fields omitted on the wire from the live daemon
// configuration. Optional numeric fields preserve an explicit zero.
func (s *Service) applyDefaults(sess *session.Session, wire *v1.Session) {
	d := s.defaults()

	if len(sess.Langs) == 0 {
		sess.Langs = slices.Clone(d.Langs)
	}

	if wire.DelaySeconds == nil {
		sess.Delay = defaultDelay(sess.Profile, d.Delay)
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

	applyCallInvariants(sess)
}

// defaultDelay: the configured delay aligns broadcast renditions; a live
// call must speak as soon as the translation exists.
func defaultDelay(profile session.Profile, configured time.Duration) time.Duration {
	if profile == session.ProfileCall {
		return 0
	}

	return configured
}

// applyCallInvariants coerces broadcast-tuned options that break a live
// call, whatever their origin: a call carries no subtitles and the far
// side must hear only the translation — the sidechain would otherwise
// release the bed to full volume between takes.
func applyCallInvariants(sess *session.Session) {
	if sess.Profile != session.ProfileCall {
		return
	}

	sess.Flags["subs"] = "off"
	sess.Flags["bed"] = "off"
}

// ListSessions implements prukka.v1.Control.
func (s *Service) ListSessions(context.Context, *v1.ListSessionsRequest) (*v1.ListSessionsResponse, error) {
	stored := s.store.List()

	sessions := make([]*v1.Session, len(stored))
	for i := range stored {
		sessions[i] = s.projectSession(&stored[i])
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
	if !slices.Contains([]string{"", "off", "vtt", "burn"}, req.GetSubs()) {
		return nil, status.Error(codes.InvalidArgument, "subs must be off, vtt or burn")
	}
	if runtimeErr := runtimePushStatus(sess.Runtime()); runtimeErr != nil {
		return nil, runtimeErr
	}

	if pushErr := s.pusher.Push(req.GetSlug(), string(target), req.GetTargetUrl(), req.GetSubs()); pushErr != nil {
		if errors.Is(pushErr, core.ErrNotReady) {
			return nil, status.Error(codes.Unavailable, pushErr.Error())
		}

		return nil, status.Error(codes.FailedPrecondition, pushErr.Error())
	}

	return &v1.PushResponse{}, nil
}

func runtimePushStatus(runtime session.RuntimeStatus) error {
	switch runtime.State {
	case session.StateFailed:
		if runtime.Error == "" {
			return status.Error(codes.FailedPrecondition, "session failed")
		}

		return status.Errorf(codes.FailedPrecondition, "session failed: %s", runtime.Error)
	case session.StateFinished:
		return status.Error(codes.FailedPrecondition, "session has finished")
	case session.StateStarting, session.StateRunning:
		return nil
	default:
		return status.Error(codes.Unavailable, "session runtime is not ready")
	}
}

// Stats implements prukka.v1.Control.
func (s *Service) Stats(context.Context, *v1.StatsRequest) (*v1.StatsResponse, error) {
	return &v1.StatsResponse{
		SessionsActive: int64(s.store.Count()),
		UptimeSeconds:  time.Since(s.started).Seconds(),
		Version:        s.version,
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

// ListDevices implements prukka.v1.Control: the machine's capture and
// playback devices, best-effort — an empty list sends pickers back to
// manual entry.
func (s *Service) ListDevices(
	ctx context.Context, _ *v1.ListDevicesRequest,
) (*v1.ListDevicesResponse, error) {
	if s.devices == nil {
		return &v1.ListDevicesResponse{}, nil
	}

	list := s.devices(ctx)

	out := make([]*v1.Device, len(list))
	for i, d := range list {
		out[i] = &v1.Device{Url: d.URL, Label: d.Label, Kind: string(d.Kind), Virtual: d.Virtual}
	}

	return &v1.ListDevicesResponse{Devices: out}, nil
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
		resp := &v1.StreamEventsResponse{Type: string(e.Type), Session: s.projectSession(&e.Session)}
		if err := stream.Send(resp); err != nil {
			return fmt.Errorf("send event: %w", err)
		}
	}

	return nil
}

func (s *Service) projectSession(sess *session.Session) *v1.Session {
	wire := sessionToProto(sess)
	wire.EffectiveDubbedLangs = projectedDubbedLanguages(s.dubbed, sess)

	return wire
}
