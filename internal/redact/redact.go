// Package redact is the one spelling of prukka's URL-redaction rule: a URL
// rendered to anyone — an error, a log line, a control-plane label — keeps at
// most its scheme and host. Userinfo (a credentialed mirror), the query (a
// presigned signature, an SRT passphrase, a token), the fragment and the path
// (a stream key, a private file name) are guaranteed gone. The port stays:
// distinct services on one host — an RTMP ingest on 1935, an SRT relay on
// 9000 — are distinct endpoints, and the port carries no secret. URLs naming
// local resources, file: and device:, hide even their authority by default: a
// file host names a network share and a device URL names capture hardware.
// Callers that render a caller-specific label build it from Split, so the
// stripping rule itself still has exactly one spelling.
package redact

import (
	"net/url"
	"regexp"
)

// Redacted names a URL that cannot be reduced to a scheme and a host.
const Redacted = "[redacted-url]"

// hidden replaces an authority the contract refuses to show.
const hidden = "[redacted]"

// Parts is the skeleton of a URL that survives redaction: the scheme
// (lowercased by net/url), the host with its port and without userinfo, and
// whether a path existed — never the path itself.
type Parts struct {
	Scheme  string
	Host    string
	HasPath bool
}

// Split reduces raw to its Parts and reports whether raw parsed as a URL
// with a scheme. It is the single parser behind URL and Text; a caller that
// renders its own label (the control plane's source label, the media
// supervisor's endpoint label) builds it from Parts, never from raw.
func Split(raw string) (parts Parts, ok bool) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" {
		return Parts{}, false
	}

	path := parsed.EscapedPath()

	return Parts{
		Scheme:  parsed.Scheme,
		Host:    parsed.Host,
		HasPath: path != "" && path != "/",
	}, true
}

// URL is the canonical reduction: scheme://host for a network endpoint,
// scheme://[redacted] when the authority itself must stay hidden — a file: or
// device: URL, or a URL with no host — and Redacted when raw does not parse
// as a URL at all, so a malformed input is never echoed.
func URL(raw string) string {
	parts, ok := Split(raw)
	switch {
	case !ok:
		return Redacted
	case parts.Scheme == "file", parts.Scheme == "device", parts.Host == "":
		return parts.Scheme + "://" + hidden
	default:
		return parts.Scheme + "://" + parts.Host
	}
}

// urlToken matches one URL-shaped token in free-form text: a scheme, "://",
// then everything up to whitespace or a quote-like delimiter, which is where
// process stderr and wrapped error prose end a URL.
var urlToken = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s"'<>]+`)

// Text applies URL to every URL-shaped token inside free-form text — child
// process stderr, a wrapped error chain — leaving the surrounding prose
// intact, so a diagnostic stays readable without carrying what any of its
// URLs carried.
func Text(text string) string {
	return urlToken.ReplaceAllStringFunc(text, URL)
}
