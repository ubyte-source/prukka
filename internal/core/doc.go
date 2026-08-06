// Package core holds the domain model and the port interfaces; adapters
// depend on core, never the reverse. hack/ci/core-boundary-gate.sh enforces
// that shape, with internal/core/config the one exception it spells out.
package core
