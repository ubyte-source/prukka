// Package redact is the one spelling of prukka's URL-redaction rule: a URL
// rendered to anyone keeps at most its scheme, host and port; userinfo, query,
// fragment and path are guaranteed gone. The port stays: it carries no secret
// and it tells two endpoints on one host apart. file: and device: URLs hide
// their authority too: a device URL names capture hardware. Callers rendering
// their own label build it from Split.
package redact

import (
	"net/url"
	"regexp"
)

// Redacted names a URL that cannot be reduced to a scheme and a host.
const Redacted = "[redacted-url]"

// hidden replaces an authority the contract refuses to show.
const hidden = "[redacted]"

// Parts is the skeleton of a URL that survives redaction: scheme, host with
// its port and without userinfo, and whether a path existed — never the path.
type Parts struct {
	Scheme  string
	Host    string
	HasPath bool
}

// Split reduces raw to its Parts and reports whether raw parsed as a URL with
// a scheme.
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

// URL reduces raw to scheme://host, to scheme://[redacted] when the authority
// must stay hidden, or to Redacted when raw does not parse as a URL.
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

// Untrusted is an error carrying foreign text — a child process's output, a
// third-party library's prose — that names the exact span it did not author, so
// a bound renderer removes it instead of guessing which tokens look dangerous.
type Untrusted interface {
	error
	Untrusted() string
}

// urlToken ends a token at whitespace or a quote-like delimiter, which is
// where process stderr and wrapped error prose end a URL.
var urlToken = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s"'<>]+`)

// Text applies URL to every URL-shaped token inside free-form text, leaving
// the surrounding prose intact.
func Text(text string) string {
	return urlToken.ReplaceAllStringFunc(text, URL)
}
