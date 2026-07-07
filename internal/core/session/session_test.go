package session_test

import (
	"errors"
	"testing"

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
		{
			name:    "bad profile",
			mutate:  func(s *session.Session) { s.Profile = "livestream" },
			wantErr: session.ErrUnknownProfile,
		},
		{name: "no languages", mutate: func(s *session.Session) { s.Langs = nil }, wantErr: session.ErrNoLanguages},
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

func TestParseProfile(t *testing.T) {
	t.Parallel()

	if _, err := session.ParseProfile("Broadcast "); err != nil {
		t.Fatalf("ParseProfile returned error: %v", err)
	}

	if _, err := session.ParseProfile("webinar"); !errors.Is(err, session.ErrUnknownProfile) {
		t.Fatalf("ParseProfile error = %v, want ErrUnknownProfile", err)
	}
}
