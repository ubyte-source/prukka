package ffmpeg

import (
	"net/url"
	"strings"
)

func endpointLabel(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" {
		return "file"
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme == "device" {
		return "device://" + parsed.Host
	}
	if parsed.Host == "" {
		return scheme + "://…"
	}

	label := scheme + "://" + parsed.Host
	if parsed.EscapedPath() != "" && parsed.EscapedPath() != "/" {
		label += "/…"
	}

	return label
}
