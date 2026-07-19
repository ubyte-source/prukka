package ffmpeg

import (
	"github.com/ubyte-source/prukka/internal/redact"
)

// endpointLabel names a media source or sink in this supervisor's logs. The
// stripping rule is redact.Split's; only the labels are local: a bare path
// (no scheme) is a local file, a device URL shows its class — the host —
// while the private identifier lives in the path Split already dropped, and
// a kept path is marked "/…" without being shown, so an operator can tell a
// push to a path from a push to the host root.
func endpointLabel(raw string) string {
	parts, ok := redact.Split(raw)
	switch {
	case !ok:
		return "file"
	case parts.Scheme == "device":
		return "device://" + parts.Host
	case parts.Host == "":
		return parts.Scheme + "://…"
	case parts.HasPath:
		return parts.Scheme + "://" + parts.Host + "/…"
	default:
		return parts.Scheme + "://" + parts.Host
	}
}
