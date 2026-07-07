package openrouter_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
)

func TestErrorClassification(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		body      string
		contains  string
		status    int
		transient bool
	}{
		{name: "rate limited", status: 429, body: `{}`, transient: true, contains: "http 429"},
		{name: "upstream down", status: 502, body: `{}`, transient: true, contains: "http 502"},
		{
			name: "bad request", status: 400,
			body:      `{"error":{"message":"model not found"}}`,
			transient: false, contains: "model not found",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)

				if _, err := w.Write([]byte(tc.body)); err != nil {
					t.Errorf("write reply: %v", err)
				}
			}))
			defer srv.Close()

			_, err := newClient(srv, &fakeMeter{}).ForSession("demo").
				Translate(t.Context(), core.Transcript{Text: "x", Lang: "it"}, "en", core.MTOpts{})
			if err == nil {
				t.Fatal("Translate succeeded, want error")
			}

			if errors.Is(err, core.ErrTransient) != tc.transient {
				t.Fatalf("transient = %v, want %v (err: %v)", !tc.transient, tc.transient, err)
			}

			if !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.contains)
			}
		})
	}
}
