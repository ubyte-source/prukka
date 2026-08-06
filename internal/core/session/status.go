package session

import (
	"cmp"
	"io/fs"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ubyte-source/prukka/internal/redact"
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
	gen   generation
}

const maxRuntimeErrorBytes = 512

const maxRuntimeErrorNodes = 1024

const runtimeErrorEllipsis = "…"

const (
	localPathLabel   = "[local-path]"
	foreignTextLabel = "[helper-output]"
)

// Runtime returns a value snapshot; callers cannot mutate stored state.
func (s *Session) Runtime() RuntimeStatus {
	return RuntimeStatus{State: s.runtime.State, Error: s.runtime.Error}
}

// sanitizeRuntimeError renders one lane failure for publication, removing the
// spans the error tree declares it did not author.
func sanitizeRuntimeError(err error) string {
	if err == nil {
		return ""
	}

	detail := printableRuntimeText(err.Error())
	detail = redactUntrusted(detail, err)
	detail = strings.Join(strings.Fields(detail), " ")

	return truncateUTF8(detail, maxRuntimeErrorBytes)
}

// untrustedSpan is one exact substring an error declared it did not author,
// paired with the label that replaces it.
type untrustedSpan struct {
	value string
	label string
}

func redactUntrusted(detail string, err error) string {
	for _, span := range runtimeUntrustedSpans(err) {
		// detail was folded to printable runes, so a span only matches in that same
		// form; %q additionally escapes separators and embedded quotes.
		detail = strings.ReplaceAll(detail, printableRuntimeText(strconv.Quote(span.value)), `"`+span.label+`"`)
		detail = strings.ReplaceAll(detail, printableRuntimeText(span.value), span.label)
	}

	return detail
}

// runtimeUntrustedSpans walks both unwrap forms; the node limit stops a cyclic
// custom error from hanging the walk. Longer spans sort first so a span nested
// inside another is not half-replaced.
func runtimeUntrustedSpans(root error) []untrustedSpan {
	pending := []error{root}
	seen := make(map[error]struct{})
	unique := make(map[untrustedSpan]struct{})

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

		collectUntrustedSpans(unique, current)
		pending = append(pending, unwrapRuntimeError(current)...)
	}

	spans := make([]untrustedSpan, 0, len(unique))
	for span := range unique {
		spans = append(spans, span)
	}
	slices.SortFunc(spans, func(left, right untrustedSpan) int {
		if byLength := cmp.Compare(len(right.value), len(left.value)); byLength != 0 {
			return byLength
		}

		return cmp.Compare(left.value, right.value)
	})

	return spans
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

func collectUntrustedSpans(spans map[untrustedSpan]struct{}, current error) {
	// errors.As would descend again and select only the first matching joined
	// child, so this inspects the node directly (errorlint waived in
	// .golangci.yml).
	switch declared := current.(type) {
	case *fs.PathError:
		addUntrustedSpan(spans, declared.Path, localPathLabel)
	case *os.LinkError:
		addUntrustedSpan(spans, declared.Old, localPathLabel)
		addUntrustedSpan(spans, declared.New, localPathLabel)
	case redact.Untrusted:
		addUntrustedSpan(spans, declared.Untrusted(), foreignTextLabel)
	}
}

func unwrapRuntimeError(current error) []error {
	// errors.Unwrap supports only the single-error form, so both shapes are
	// inspected directly to preserve every Join branch (errorlint waived in
	// .golangci.yml).
	switch wrapped := current.(type) {
	case interface{ Unwrap() []error }:
		return wrapped.Unwrap()
	case interface{ Unwrap() error }:
		return []error{wrapped.Unwrap()}
	default:
		return nil
	}
}

func addUntrustedSpan(spans map[untrustedSpan]struct{}, value, label string) {
	if value != "" {
		spans[untrustedSpan{value: value, label: label}] = struct{}{}
	}
}

func printableRuntimeText(value string) string {
	return strings.Map(printableRuntimeRune, value)
}

func printableRuntimeRune(r rune) rune {
	if !unicode.IsPrint(r) {
		return ' '
	}

	return r
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
