// Package discover enumerates the machine's capture and playback devices
// so pickers can offer real device names instead of raw device:// URLs.
// Enumeration is best-effort: whatever layer answers contributes, and an
// empty result simply sends the UI back to manual entry.
package discover

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
)

// coldStartBudget bounds the wait of a caller that finds no complete
// generation at all. A wedged enumeration can delay that first, empty answer
// by at most this much; every later read is served from the last generation.
const coldStartBudget = 250 * time.Millisecond

// captureListBudget bounds ONE ffmpeg device listing on its own: the layers
// of an enumeration are probed in sequence, so a single wedged listing must
// fail alone instead of starving the sibling probes and the native playback
// list. On macOS a pending camera-consent decision is the canonical wedge;
// a hung PulseAudio or DirectShow enumeration strands the other legs the
// same way.
const captureListBudget = 2500 * time.Millisecond

// listCaptureRaw is the one path a platform leg runs an ffmpeg device
// listing through: every listing inherits the per-listing budget, so no OS
// can reintroduce an unbounded probe.
func listCaptureRaw(ctx context.Context, bin string, args ...string) (string, error) {
	listCtx, cancel := context.WithTimeout(ctx, captureListBudget)
	defer cancel()

	return ffmpeg.ListRaw(listCtx, bin, args...)
}

// Kind classifies a device's role.
type Kind string

// Device roles exposed by the discovery API.
const (
	AudioIn  Kind = "audio-in"
	VideoIn  Kind = "video-in"
	AudioOut Kind = "audio-out"
	VideoOut Kind = "video-out"
)

// Device is one local media device, ready to use where the daemon accepts
// a device:// URL.
type Device struct {
	// URL is the session source or push target, e.g. "device://audio/1".
	URL string
	// Label is the human-readable device name.
	Label string
	// Kind is the device's role.
	Kind Kind
	// Virtual marks Prukka's own loopback devices.
	Virtual bool
}

// catalog keeps a slow or uncancellable enumeration off its callers'
// goroutines. Refresh requests are coalesced behind one worker: even a
// permanently wedged pass consumes a single goroutine while callers keep
// reading the last complete generation.
type catalog[T any] struct {
	load      func() (*T, bool)
	requests  chan struct{}
	ready     chan struct{}
	snapshot  atomic.Pointer[T]
	readyOnce sync.Once
}

// newCatalog starts the one refresh worker immediately; ctx bounds its
// lifetime, so a test-scoped catalog cannot leak its goroutine.
func newCatalog[T any](ctx context.Context, load func() (*T, bool)) *catalog[T] {
	c := &catalog[T]{
		load: load, requests: make(chan struct{}, 1), ready: make(chan struct{}),
	}
	go c.worker(ctx)

	return c
}

func (c *catalog[T]) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.requests:
			if snapshot, ok := c.load(); ok {
				c.publish(snapshot)
			}
		}
	}
}

func (c *catalog[T]) publish(snapshot *T) {
	if snapshot != nil {
		c.snapshot.Store(snapshot)
		c.readyOnce.Do(func() { close(c.ready) })
	}
}

func (c *catalog[T]) requestRefresh() {
	select {
	case c.requests <- struct{}{}:
	default:
	}
}

func (c *catalog[T]) current() *T {
	// Snapshot the completed generation before scheduling the next one. A
	// caller never waits for the load and never observes a partial result.
	snapshot := c.snapshot.Load()
	c.requestRefresh()

	return snapshot
}

// currentWithin waits only when no complete generation has ever been
// published. It does not start another goroutine: requestRefresh owns the one
// catalog worker, while the caller waits on publication or its own deadline.
func (c *catalog[T]) currentWithin(ctx context.Context) *T {
	if snapshot := c.current(); snapshot != nil {
		return snapshot
	}

	select {
	case <-ctx.Done():
		return nil
	case <-c.ready:
		return c.snapshot.Load()
	}
}

// deviceSnapshot is one complete enumeration, published whole.
type deviceSnapshot struct {
	devices []Device
}

// Inventory answers device listings from the last complete enumeration.
// Enumeration spawns ffmpeg and probes native components, costing seconds on
// some hosts, so it runs on one coalesced worker instead of on the caller.
type Inventory struct {
	catalog *catalog[deviceSnapshot]
}

// NewInventory schedules the first enumeration immediately, so a listing
// requested after startup normally finds a complete inventory waiting; ctx
// bounds the refresh worker's lifetime.
func NewInventory(ctx context.Context, enumerate func() []Device) *Inventory {
	inventory := &Inventory{catalog: newCatalog(ctx, func() (*deviceSnapshot, bool) {
		return &deviceSnapshot{devices: enumerate()}, true
	})}
	inventory.catalog.requestRefresh()

	return inventory
}

// List returns the last complete enumeration and schedules the next one. Only
// a cold list waits, for at most coldStartBudget, and then degrades to manual
// entry until a refresh lands.
func (i *Inventory) List(ctx context.Context) []Device {
	waitCtx, cancel := context.WithTimeout(ctx, coldStartBudget)
	defer cancel()

	snapshot := i.catalog.currentWithin(waitCtx)
	if snapshot == nil {
		return nil
	}

	return snapshot.devices
}

// virtualLabel recognizes Prukka's own loopback devices by name.
func virtualLabel(label string) bool {
	return strings.Contains(label, "Prukka")
}

func appendNativeVideoOutput(devices []Device, label string, available bool) []Device {
	if !available {
		return devices
	}

	return append(devices, Device{
		URL: "device://video/prukka", Label: label, Kind: VideoOut, Virtual: true,
	})
}
