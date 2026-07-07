package observability

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// latencyBuckets span the budgets: sub-frame work up through the
// multi-second broadcast paths.
var latencyBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 1.8, 3.5, 5, 7, 9, 14, 20}

// Metrics holds every Prometheus collector, registered on a
// dedicated registry (no global default, no init side effects).
type Metrics struct {
	reg *prometheus.Registry

	stageLatency   *prometheus.HistogramVec
	e2eLatency     *prometheus.HistogramVec
	sessionsActive prometheus.Gauge
	providerErrors *prometheus.CounterVec
	costTotal      *prometheus.CounterVec
	fallbackActive prometheus.Gauge
}

// NewMetrics builds and registers the collectors.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()

	m := &Metrics{
		reg: reg,
		stageLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "prukka_stage_latency_seconds",
			Help:    "Per-stage processing latency.",
			Buckets: latencyBuckets,
		}, []string{"stage"}),
		e2eLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "prukka_e2e_latency_seconds",
			Help:    "End-to-end latency from speech end to output.",
			Buckets: latencyBuckets,
		}, []string{"kind"}),
		sessionsActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "prukka_sessions_active",
			Help: "Number of active sessions.",
		}),
		providerErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "prukka_provider_errors_total",
			Help: "Provider call failures.",
		}, []string{"provider", "code"}),
		costTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "prukka_cost_eur_total",
			Help: "Cumulative provider cost in euros by session and stage.",
		}, []string{"session", "kind"}),
		fallbackActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "prukka_fallback_active",
			Help: "1 while any session runs in pass-through fallback.",
		}),
	}

	reg.MustRegister(m.stageLatency, m.e2eLatency, m.sessionsActive,
		m.providerErrors, m.costTotal, m.fallbackActive)

	return m
}

// RegisterDispatchQueue publishes the dispatcher's depth and capacity as
// scrape-time gauges so operators can size workers/queue.
func (m *Metrics) RegisterDispatchQueue(depth, capacity func() float64) {
	m.reg.MustRegister(
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "prukka_dispatch_queue_depth",
			Help: "Provider dispatcher jobs waiting for a worker.",
		}, depth),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "prukka_dispatch_queue_capacity",
			Help: "Provider dispatcher queue capacity before submitters block.",
		}, capacity),
	)
}

// Handler serves the metrics registry (/metrics).
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// StageLatency records one stage's processing time.
func (m *Metrics) StageLatency(stage string, d time.Duration) {
	m.stageLatency.WithLabelValues(stage).Observe(d.Seconds())
}

// E2ELatency records an end-to-end path: kind is "caption" or "voice".
func (m *Metrics) E2ELatency(kind string, d time.Duration) {
	m.e2eLatency.WithLabelValues(kind).Observe(d.Seconds())
}

// SetSessionsActive publishes the live session count.
func (m *Metrics) SetSessionsActive(n int) {
	m.sessionsActive.Set(float64(n))
}

// ProviderError counts one provider failure by provider and status code.
func (m *Metrics) ProviderError(provider, code string) {
	m.providerErrors.WithLabelValues(provider, code).Inc()
}

// AddCost accumulates provider cost per session and stage (stt/mt/tts) —
// per-stage is the finest decomposition the meter carries.
func (m *Metrics) AddCost(session, kind string, eur float64) {
	m.costTotal.WithLabelValues(session, kind).Add(eur)
}

// SetFallback marks whether any session is in pass-through fallback.
func (m *Metrics) SetFallback(active bool) {
	m.fallbackActive.Set(boolGauge(active))
}

// boolGauge maps a flag to its gauge value.
func boolGauge(b bool) float64 {
	if b {
		return 1
	}

	return 0
}
