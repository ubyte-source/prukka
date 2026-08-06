package ffmpeg

import (
	"github.com/ubyte-source/prukka/internal/media/deviceurl"
	"github.com/ubyte-source/prukka/internal/redact"
)

// endpointLabel names a media source or sink in this supervisor's logs; the
// stripping rule is redact.Split's, and a kept path is marked "/…" rather than
// shown.
func endpointLabel(raw string) string {
	parts, ok := redact.Split(raw)
	switch {
	case !ok:
		return "file"
	case parts.Scheme == "device":
		return deviceurl.Scheme + parts.Host
	case parts.Host == "":
		return parts.Scheme + "://…"
	case parts.HasPath:
		return parts.Scheme + "://" + parts.Host + "/…"
	default:
		return parts.Scheme + "://" + parts.Host
	}
}
