package rest_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/providers/helpers/rest"
)

// statusErr posts one request against a canned handler and returns the
// classifier's verdict on the live reply.
func statusErr(t *testing.T, handler http.HandlerFunc) error {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/stage", http.NoBody)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("post request: %v", err)
	}

	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Errorf("close body: %v", closeErr)
		}
	}()

	return rest.StatusError("/stage", resp)
}

func TestStatusErrorClassifiesStatuses(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		status    int
		transient bool
	}{
		{"too many requests", http.StatusTooManyRequests, true},
		{"server error", http.StatusBadGateway, true},
		{"client error", http.StatusPaymentRequired, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := statusErr(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			})
			if err == nil {
				t.Fatalf("http %d produced no error", tc.status)
			}

			if got := errors.Is(err, core.ErrTransient); got != tc.transient {
				t.Fatalf("http %d transient = %v, want %v", tc.status, got, tc.transient)
			}
		})
	}
}

func TestStatusErrorLiftsEnvelopeMessage(t *testing.T) {
	t.Parallel()

	err := statusErr(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)

		if _, writeErr := io.WriteString(w, `{"error":{"message":"insufficient credits"}}`); writeErr != nil {
			t.Errorf("write reply: %v", writeErr)
		}
	})
	if err == nil || !strings.Contains(err.Error(), "insufficient credits") {
		t.Fatalf("error = %v, want the envelope message surfaced", err)
	}

	if strings.Contains(err.Error(), "{") {
		t.Fatalf("error = %v, want the raw envelope replaced by its message", err)
	}
}

func TestStatusErrorQuotesRawBody(t *testing.T) {
	t.Parallel()

	err := statusErr(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)

		if _, writeErr := io.WriteString(w, "model not loaded"); writeErr != nil {
			t.Errorf("write reply: %v", writeErr)
		}
	})
	if err == nil || !strings.Contains(err.Error(), "model not loaded") {
		t.Fatalf("error = %v, want the raw body quoted", err)
	}
}

func TestTransportErrorIsTransient(t *testing.T) {
	t.Parallel()

	err := rest.TransportError("/stage", errors.New("connection refused"))
	if !errors.Is(err, core.ErrTransient) {
		t.Fatalf("transport error = %v, want core.ErrTransient", err)
	}

	if !strings.Contains(err.Error(), "/stage") || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("error = %v, want the path and the cause named", err)
	}
}

func TestJSONIntoDecodesReply(t *testing.T) {
	t.Parallel()

	var out struct {
		Text string `json:"text"`
	}

	if err := rest.JSONInto(&out)(strings.NewReader(`{"text":"ciao"}`)); err != nil {
		t.Fatalf("JSONInto returned error: %v", err)
	}

	if out.Text != "ciao" {
		t.Fatalf("decoded text = %q, want ciao", out.Text)
	}
}

func TestJSONIntoWrapsDecodeFailure(t *testing.T) {
	t.Parallel()

	err := rest.JSONInto(&struct{}{})(strings.NewReader("not json"))
	if err == nil || !strings.Contains(err.Error(), "decode reply") {
		t.Fatalf("error = %v, want a decode failure", err)
	}
}

func TestReadAllCapturesRawBody(t *testing.T) {
	t.Parallel()

	var raw []byte

	if err := rest.ReadAll(&raw)(strings.NewReader("\x01\x02\x03")); err != nil {
		t.Fatalf("ReadAll returned error: %v", err)
	}

	if len(raw) != 3 || raw[0] != 0x01 {
		t.Fatalf("raw = %v, want the 3 reply bytes", raw)
	}
}

func TestReadAllMarksReadFailureTransient(t *testing.T) {
	t.Parallel()

	var raw []byte

	err := rest.ReadAll(&raw)(iotest.ErrReader(errors.New("reset")))
	if !errors.Is(err, core.ErrTransient) {
		t.Fatalf("read failure = %v, want core.ErrTransient", err)
	}
}
