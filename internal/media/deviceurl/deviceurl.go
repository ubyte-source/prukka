// Package deviceurl owns the device:// grammar: the scheme, the device
// classes, and the one parser and formatter over them. docs/DEVICES.md maps
// each kind onto the platform layer that serves it.
package deviceurl

import (
	"fmt"
	neturl "net/url"
	"strings"

	"github.com/ubyte-source/prukka/internal/redact"
)

// Scheme prefixes every device URL.
const Scheme = "device://"

// labelKey is the query parameter carrying the rebinding hint.
const labelKey = "label"

// pairSeparator joins the camera and the microphone of a paired capture.
const pairSeparator = "|"

// Kind is the device class a URL selects.
type Kind string

// The device classes the daemon can address.
const (
	// Audio is a capture source or a playback target: device://audio/<id>.
	Audio Kind = "audio"
	// Video is a camera or an injection node: device://video/<id>.
	Video Kind = "video"
	// AV is one camera paired with one microphone, captured together.
	AV Kind = "av"
)

// NativeVideo is the stable URL of Prukka's platform webcam feeder.
const NativeVideo = Scheme + string(Video) + "/prukka"

// known reports whether k is a class this package defines. The switch has no
// default arm so a new Kind fails the exhaustiveness check here.
func (k Kind) known() bool {
	switch k {
	case Audio, Video, AV:
		return true
	}

	return false
}

// Ref is one device URL taken apart.
type Ref struct {
	Kind Kind
	// ID is the platform's own identifier: an index, a PulseAudio or DirectShow
	// name, a WASAPI endpoint id, a /dev node, or the two halves of a pair
	// joined by "|".
	ID string
	// Label is an optional display-name rebinding hint, not a stable hardware
	// identifier: positional ids reshuffle as devices appear and vanish.
	Label string
}

// Parse splits device://<kind>/<id>[?label=<name>].
func Parse(raw string) (Ref, error) {
	rest, scoped := strings.CutPrefix(raw, Scheme)
	if !scoped {
		return Ref{}, shapeError(raw)
	}

	kind, id, cut := strings.Cut(rest, "/")
	if !cut || kind == "" || id == "" {
		return Ref{}, shapeError(raw)
	}

	if !Kind(kind).known() {
		return Ref{}, fmt.Errorf("device URL %s: unknown device kind %q", redact.URL(raw), kind)
	}

	var label string
	if bare, query, tagged := strings.Cut(id, "?"); tagged {
		id = bare
		if values, parseErr := neturl.ParseQuery(query); parseErr == nil {
			label = values.Get(labelKey)
		}
	}
	if id == "" {
		return Ref{}, fmt.Errorf("device URL %s: empty device id", redact.URL(raw))
	}

	return Ref{Kind: Kind(kind), ID: id, Label: label}, nil
}

// String renders the URL, writing the id verbatim: platform identifiers are
// published unescaped.
func (r Ref) String() string {
	raw := Scheme + string(r.Kind) + "/" + r.ID
	if r.Label == "" {
		return raw
	}

	return raw + "?" + labelKey + "=" + neturl.QueryEscape(r.Label)
}

// Pair splits a paired capture into its camera and its microphone; only the
// AV kind pairs.
func (r Ref) Pair() (camera, microphone string, err error) {
	camera, microphone, cut := strings.Cut(r.ID, pairSeparator)
	if r.Kind != AV || !cut || camera == "" || microphone == "" {
		return "", "", fmt.Errorf(
			"device URL %s: want %s%s/<camera>%s<microphone>", redact.URL(r.String()), Scheme, AV, pairSeparator)
	}

	return camera, microphone, nil
}

// Is reports whether raw claims the device scheme, so a malformed device URL
// is answered with the grammar's own error instead of demoted to a file path.
func Is(raw string) bool {
	return strings.HasPrefix(raw, Scheme)
}

// IsKind reports whether raw is a well-formed device URL of one kind.
func IsKind(raw string, kind Kind) bool {
	ref, err := Parse(raw)

	return err == nil && ref.Kind == kind
}

// IsNativeVideo reports whether target selects the platform webcam feeder.
func IsNativeVideo(target string) bool {
	return target == NativeVideo
}

// AudioLabel returns the rebinding hint an audio device URL carries, or ""
// when it carries none or does not parse.
func AudioLabel(raw string) string {
	ref, err := Parse(raw)
	if err != nil || ref.Kind != Audio {
		return ""
	}

	return ref.Label
}

// shapeError names the single-device grammar a raw string failed to match.
func shapeError(raw string) error {
	return fmt.Errorf(
		"device URL %s: want %s%s/<id> or %s%s/<id>", redact.URL(raw), Scheme, Audio, Scheme, Video)
}
