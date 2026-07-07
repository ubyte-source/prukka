package observability

import "sync/atomic"

// FallbackState counts open circuit breakers and keeps the
// prukka_fallback_active gauge in sync.
type FallbackState struct {
	metrics *Metrics
	open    atomic.Int64
}

// NewFallbackState wires the tracker to the metrics gauge.
func NewFallbackState(metrics *Metrics) *FallbackState {
	return &FallbackState{metrics: metrics}
}

// Observe is a breaker.Observer: it counts open breakers, republishes the
// gauge and records each opening as a provider error.
func (f *FallbackState) Observe(open bool) {
	if open {
		f.metrics.ProviderError("openrouter", "breaker_open")
		f.metrics.SetFallback(f.open.Add(1) > 0)

		return
	}

	f.metrics.SetFallback(f.open.Add(-1) > 0)
}
