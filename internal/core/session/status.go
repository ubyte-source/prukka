package session

import (
	"cmp"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// RuntimeState describes the observed lifecycle of one session lane.
type RuntimeState string

// Runtime states exposed by the control plane.
const (
	StateStarting RuntimeState = "starting"
	StateRunning  RuntimeState = "running"
	StateFinished RuntimeState = "finished"
	StateFailed   RuntimeState = "failed"
)

// RuntimeStatus is observed state, separate from the session definition.
type RuntimeStatus struct {
	State RuntimeState
	Error string
	gen   uint64
}

const maxRuntimeErrorBytes = 512

const maxRuntimeErrorNodes = 1024

const runtimeErrorEllipsis = "…"

var (
	runtimeURL           = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s"'<>]+`)
	runtimeAuthorization = regexp.MustCompile(
		`(?i)(authorization)\s*[:=]\s*(?:bearer\s+)?[^\s,;]+`,
	)
	runtimeSecret = regexp.MustCompile(
		`(?i)(api[-_]?key|token|secret|password)\s*[:=]\s*[^\s,;]+`,
	)
	runtimeQuotedPOSIXPath   = regexp.MustCompile(`"/[^"\r\n]+"|'/(?:[^'\r\n]+)'`)
	runtimeQuotedWindowsPath = regexp.MustCompile(
		`(?i)"(?:[a-z]:\\|\\\\)[^"\r\n]+"|'(?:[a-z]:\\|\\\\)[^'\r\n]+'`,
	)
	runtimePOSIXPath   = regexp.MustCompile(`(^|[\s("'=])/(?:[^\s"'<>]+/?)+`)
	runtimeWindowsPath = regexp.MustCompile(
		`(?i)(^|[\s("'=])(?:[a-z]:\\|\\\\)[^\s"'<>]+`,
	)
)

// Runtime returns a value snapshot; callers cannot mutate stored state.
func (s *Session) Runtime() RuntimeStatus {
	return RuntimeStatus{State: s.runtime.State, Error: s.runtime.Error}
}

func sanitizeRuntimeError(err error, sources ...string) string {
	if err == nil {
		return ""
	}

	detail := strings.Map(printableRuntimeRune, err.Error())
	for _, source := range sources {
		detail = redactSource(detail, source)
	}
	detail = redactPathErrors(detail, err)
	detail = runtimeURL.ReplaceAllStringFunc(detail, redactRuntimeURL)
	detail = runtimeAuthorization.ReplaceAllString(detail, "$1=[redacted]")
	detail = runtimeSecret.ReplaceAllString(detail, "$1=[redacted]")
	detail = runtimeQuotedPOSIXPath.ReplaceAllString(detail, `"[local-path]"`)
	detail = runtimeQuotedWindowsPath.ReplaceAllString(detail, `"[local-path]"`)
	detail = runtimePOSIXPath.ReplaceAllString(detail, "$1[local-path]")
	detail = runtimeWindowsPath.ReplaceAllString(detail, "$1[local-path]")
	detail = strings.Join(strings.Fields(detail), " ")

	return truncateUTF8(detail, maxRuntimeErrorBytes)
}

func redactSource(detail, source string) string {
	if source == "" {
		return detail
	}
	detail = strings.ReplaceAll(detail, source, redactRuntimeURL(source))

	if path := sourceFilePath(source); path != "" {
		detail = strings.ReplaceAll(detail, path, "[local-path]")
	}

	return detail
}

func sourceFilePath(source string) string {
	rest, ok := strings.CutPrefix(source, "file://")
	if !ok {
		return ""
	}
	rest, _, _ = strings.Cut(rest, "?")
	if rest == "" {
		return ""
	}
	if decoded, err := url.PathUnescape(rest); err == nil {
		rest = decoded
	}
	if len(rest) > 1 && rest[0] == '/' && filepath.VolumeName(rest[1:]) != "" {
		rest = rest[1:]
	}

	clean := filepath.Clean(filepath.FromSlash(rest))
	if clean == "." || clean == string(filepath.Separator) {
		return ""
	}

	return clean
}

func redactPathErrors(detail string, err error) string {
	paths := runtimeErrorPaths(err)
	for _, path := range paths {
		// %q escapes Windows separators and embedded quotes, so redact its
		// rendered form before replacing the raw path.
		detail = strings.ReplaceAll(detail, strconv.Quote(path), `"[local-path]"`)
		detail = strings.ReplaceAll(detail, path, "[local-path]")
	}

	return detail
}

// runtimeErrorPaths walks both standard unwrap forms. The node limit prevents
// a hostile cyclic custom error from hanging a control-plane projection;
// comparable nodes are also deduplicated so ordinary shared graphs stay cheap.
func runtimeErrorPaths(root error) []string {
	pending := []error{root}
	seen := make(map[error]struct{})
	unique := make(map[string]struct{})

	for visited := 0; len(pending) != 0 && visited < maxRuntimeErrorNodes; visited++ {
		last := len(pending) - 1
		current := pending[last]
		pending = pending[:last]
		if current == nil {
			continue
		}

		if runtimeErrorSeen(current, seen) {
			continue
		}

		collectRuntimeErrorPaths(unique, current)
		pending = append(pending, unwrapRuntimeError(current)...)
	}

	paths := make([]string, 0, len(unique))
	for path := range unique {
		paths = append(paths, path)
	}
	slices.SortFunc(paths, func(left, right string) int {
		if byLength := cmp.Compare(len(right), len(left)); byLength != 0 {
			return byLength
		}

		return cmp.Compare(left, right)
	})

	return paths
}

func runtimeErrorSeen(current error, seen map[error]struct{}) bool {
	if !reflect.TypeOf(current).Comparable() {
		return false
	}
	if _, duplicate := seen[current]; duplicate {
		return true
	}
	seen[current] = struct{}{}

	return false
}

func collectRuntimeErrorPaths(paths map[string]struct{}, current error) {
	// The walker has already isolated this exact node; errors.As would descend
	// again and repeatedly select only the first matching joined child, so the
	// type switch inspects this node directly (errorlint waived in .golangci.yml).
	switch pathErr := current.(type) {
	case *fs.PathError:
		addRuntimeErrorPath(paths, pathErr.Path)
	case *os.LinkError:
		addRuntimeErrorPath(paths, pathErr.Old)
		addRuntimeErrorPath(paths, pathErr.New)
	}
}

func unwrapRuntimeError(current error) []error {
	// Both unwrap shapes must be inspected directly to preserve every Join
	// branch; errors.Unwrap deliberately supports only the single-error form
	// (errorlint waived in .golangci.yml).
	switch wrapped := current.(type) {
	case interface{ Unwrap() []error }:
		return wrapped.Unwrap()
	case interface{ Unwrap() error }:
		return []error{wrapped.Unwrap()}
	default:
		return nil
	}
}

func addRuntimeErrorPath(paths map[string]struct{}, path string) {
	if path != "" {
		paths[path] = struct{}{}
	}
}

func printableRuntimeRune(r rune) rune {
	if !unicode.IsPrint(r) {
		return ' '
	}

	return r
}

func redactRuntimeURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" {
		return "[redacted-url]"
	}
	if parsed.Scheme == "file" || parsed.Scheme == "device" {
		return parsed.Scheme + "://[redacted]"
	}
	if parsed.Host == "" {
		return parsed.Scheme + "://[redacted]"
	}

	return parsed.Scheme + "://" + parsed.Host
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}

	value = value[:limit-len(runtimeErrorEllipsis)]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}

	return strings.TrimSpace(value) + runtimeErrorEllipsis
}

func validRuntimeTransition(from, to RuntimeState) bool {
	switch from {
	case StateStarting:
		return to == StateRunning || to == StateFinished || to == StateFailed
	case StateRunning:
		return to == StateFinished || to == StateFailed
	case StateFinished, StateFailed:
		return false
	default:
		return to == StateStarting
	}
}
