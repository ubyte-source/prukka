package control

import (
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/session"
	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
)

func TestSessionRoundTrip(t *testing.T) {
	t.Parallel()

	in := &v1.Session{
		Slug:             "demo",
		Profile:          "broadcast",
		SourceUrl:        "rtmp://0.0.0.0:1935/in/demo",
		Langs:            []string{"it", "de-ch"},
		VoiceMap:         map[string]string{"main": "nova"},
		Flags:            map[string]string{"subs": "vtt"},
		BudgetEurPerHour: 3,
		DelaySeconds:     8,
	}

	sess, err := sessionFromProto(in)
	if err != nil {
		t.Fatalf("sessionFromProto returned error: %v", err)
	}

	// Language tags are normalized through the registry at the boundary.
	if len(sess.Langs) != 2 || sess.Langs[1] != "de-CH" {
		t.Fatalf("langs = %v, want normalized [it de-CH]", sess.Langs)
	}

	if sess.VoiceMap["main"].ID != "nova" || sess.Delay != 8*time.Second {
		t.Fatalf("voice/delay = %+v / %v", sess.VoiceMap, sess.Delay)
	}

	assertProtoRoundTrip(t, sess)
}

// assertProtoRoundTrip checks sessionToProto preserves the mapped fields.
func assertProtoRoundTrip(t *testing.T, sess *session.Session) {
	t.Helper()

	out := sessionToProto(sess)
	if out.GetSlug() != "demo" || out.GetDelaySeconds() != 8 || out.GetVoiceMap()["main"] != "nova" {
		t.Fatalf("round-trip lost data: %+v", out)
	}

	if len(out.GetLangs()) != 2 || out.GetLangs()[1] != "de-CH" {
		t.Fatalf("round-trip langs = %v", out.GetLangs())
	}
}

func TestSessionFromProtoRejectsBadInput(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   *v1.Session
		name string
		code codes.Code
	}{
		{name: "nil session", in: nil, code: codes.InvalidArgument},
		{
			name: "bad profile",
			in:   &v1.Session{Slug: "x", Profile: "live", Langs: []string{"it"}},
			code: codes.InvalidArgument,
		},
		{
			name: "bad language",
			in:   &v1.Session{Slug: "x", Profile: "broadcast", Langs: []string{"ch"}},
			code: codes.InvalidArgument,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := sessionFromProto(tc.in)
			if status.Code(err) != tc.code {
				t.Fatalf("code = %v, want %v (err %v)", status.Code(err), tc.code, err)
			}
		})
	}
}

func TestStatusFromStore(t *testing.T) {
	t.Parallel()

	cases := []struct {
		err  error
		want codes.Code
	}{
		{err: session.ErrNotFound, want: codes.NotFound},
		{err: session.ErrExists, want: codes.AlreadyExists},
		{err: session.ErrNoLanguages, want: codes.InvalidArgument},
		{err: errors.New("other"), want: codes.InvalidArgument},
	}

	for _, tc := range cases {
		if got := status.Code(statusFromStore(tc.err)); got != tc.want {
			t.Errorf("statusFromStore(%v) code = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestParseLangs(t *testing.T) {
	t.Parallel()

	got, err := parseLangs([]string{"it", "en"})
	if err != nil {
		t.Fatalf("parseLangs returned error: %v", err)
	}

	if len(got) != 2 || got[0] != core.Lang("it") {
		t.Fatalf("parseLangs = %v, want [it en]", got)
	}

	if _, err := parseLangs([]string{"nope"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("parseLangs bad tag code = %v, want InvalidArgument", status.Code(err))
	}
}
