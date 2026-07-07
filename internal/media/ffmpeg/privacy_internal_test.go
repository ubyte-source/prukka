package ffmpeg

import (
	"net/url"
	"strings"
	"testing"
)

func TestEndpointLabelRemovesCredentialsPathsAndQueries(t *testing.T) {
	t.Parallel()

	endpoint := &url.URL{
		Scheme: "rtmps", Host: "example.test:443", Path: "/live/material", RawQuery: "q=value",
		User: url.UserPassword("alice", "opaque"),
	}
	raw := endpoint.String()
	label := endpointLabel(raw)
	if label != "rtmps://example.test:443/…" {
		t.Fatalf("endpointLabel = %q", label)
	}
	if strings.Contains(label, "alice") || strings.Contains(label, "material") || strings.Contains(label, "value") {
		t.Fatalf("endpointLabel leaked sensitive URL content: %q", label)
	}
}

func TestRedactEndpointsRemovesRawValues(t *testing.T) {
	t.Parallel()

	endpoint := "srt://example.test:9000?passphrase=private"
	redacted := redactEndpoints("ffmpeg failed for "+endpoint, endpoint)
	if strings.Contains(redacted, "passphrase") || strings.Contains(redacted, "private") {
		t.Fatalf("redacted text leaked endpoint: %q", redacted)
	}
	if !strings.Contains(redacted, "srt://example.test:9000") {
		t.Fatalf("redacted text lost useful endpoint class: %q", redacted)
	}
}
