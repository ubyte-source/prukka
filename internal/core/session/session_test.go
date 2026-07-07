package session_test

import (
	"errors"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/session"
)

func TestCreateValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		mutate  func(*session.Session)
		wantErr error
		name    string
	}{
		{name: "valid", mutate: func(*session.Session) {}, wantErr: nil},
		{name: "empty slug", mutate: func(s *session.Session) { s.Slug = "" }, wantErr: session.ErrInvalidSlug},
		{name: "uppercase slug", mutate: func(s *session.Session) { s.Slug = "Demo" }, wantErr: session.ErrInvalidSlug},
		{name: "leading hyphen", mutate: func(s *session.Session) { s.Slug = "-demo" }, wantErr: session.ErrInvalidSlug},
		{name: "trailing hyphen", mutate: func(s *session.Session) { s.Slug = "demo-" }, wantErr: session.ErrInvalidSlug},
		{
			name:    "bad profile",
			mutate:  func(s *session.Session) { s.Profile = "livestream" },
			wantErr: session.ErrUnknownProfile,
		},
		{name: "no languages", mutate: func(s *session.Session) { s.Langs = nil }, wantErr: session.ErrNoLanguages},
		{
			name:    "dub language outside targets",
			mutate:  func(s *session.Session) { s.Flags = map[string]string{"dub_langs": "de"} },
			wantErr: session.ErrInvalidFlags,
		},
		{
			name: "conflicting dub flags",
			mutate: func(s *session.Session) {
				s.Flags = map[string]string{"dub": "off", "dub_langs": "it"}
			},
			wantErr: session.ErrInvalidFlags,
		},
		{
			name:    "unknown flag",
			mutate:  func(s *session.Session) { s.Flags = map[string]string{"sub": "vtt"} },
			wantErr: session.ErrInvalidFlags,
		},
		{
			name:    "invalid bed flag",
			mutate:  func(s *session.Session) { s.Flags = map[string]string{"bed": "loud"} },
			wantErr: session.ErrInvalidFlags,
		},
		{
			name:    "invalid pair slug",
			mutate:  func(s *session.Session) { s.Flags = map[string]string{"pair": "Meeting Out"} },
			wantErr: session.ErrInvalidFlags,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := demo("demo")
			tc.mutate(s)

			err := session.NewStore().Create(s)
			if tc.wantErr == nil && err != nil {
				t.Fatalf("Create returned error: %v", err)
			}

			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("Create error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestCreateRejectsAutoTarget(t *testing.T) {
	t.Parallel()

	s := demo("demo")
	s.Langs = []core.Lang{core.LangAuto}
	if err := session.NewStore().Create(s); !errors.Is(err, session.ErrInvalidLanguage) {
		t.Fatalf("Create error = %v, want ErrInvalidLanguage", err)
	}
}

func TestCreateRejectsDuplicateTarget(t *testing.T) {
	t.Parallel()

	s := demo("demo")
	s.Langs = []core.Lang{"it", "it"}
	if err := session.NewStore().Create(s); !errors.Is(err, session.ErrInvalidLanguage) {
		t.Fatalf("Create error = %v, want ErrInvalidLanguage", err)
	}
}

func TestCreateRejectsInvalidLimits(t *testing.T) {
	t.Parallel()

	cases := []struct {
		mutate  func(*session.Session)
		wantErr error
		name    string
	}{
		{
			name:    "negative delay",
			mutate:  func(s *session.Session) { s.Delay = -1 },
			wantErr: session.ErrInvalidDelay,
		},
		{
			name:    "excessive delay",
			mutate:  func(s *session.Session) { s.Delay = core.MaxSessionDelay + time.Second },
			wantErr: session.ErrInvalidDelay,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := demo("demo")
			tc.mutate(s)
			if err := session.NewStore().Create(s); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Create error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestParseProfile(t *testing.T) {
	t.Parallel()

	if _, err := session.ParseProfile("Broadcast "); err != nil {
		t.Fatalf("ParseProfile returned error: %v", err)
	}

	if _, err := session.ParseProfile("webinar"); !errors.Is(err, session.ErrUnknownProfile) {
		t.Fatalf("ParseProfile error = %v, want ErrUnknownProfile", err)
	}

	if _, err := session.ParseProfile("agent"); !errors.Is(err, session.ErrUnknownProfile) {
		t.Fatalf("removed agent profile error = %v, want ErrUnknownProfile", err)
	}
}
