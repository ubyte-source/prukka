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

	e2eLatency         *prometheus.HistogramVec
	sessionsRegistered prometheus.Gauge
	sessionsByState    *prometheus.GaugeVec
}

// SessionCounts is a bounded aggregate of the session registry. It avoids a
// per-session label while distinguishing live and terminal definitions.
type SessionCounts struct {
	Registered int
	Starting   int
	Running    int
	Finished   int
	Failed     int
}

// NewMetrics builds and registers the collectors.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()

	m := &Metrics{
		reg: reg,
		e2eLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "prukka_e2e_latency_seconds",
			Help:    "End-to-end latency from speech end to output.",
			Buckets: latencyBuckets,
		}, []string{"kind"}),
		sessionsRegistered: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "prukka_sessions_registered",
			Help: "Number of registered session definitions, including terminal states.",
		}),
		sessionsByState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "prukka_sessions_by_state",
			Help: "Number of registered sessions in each runtime state.",
		}, []string{"state"}),
	}

	reg.MustRegister(m.e2eLatency, m.sessionsRegistered, m.sessionsByState)
	m.SetSessionCounts(SessionCounts{})

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

// E2ELatency records an end-to-end path: kind is "caption" or "voice".
func (m *Metrics) E2ELatency(kind string, d time.Duration) {
	m.e2eLatency.WithLabelValues(kind).Observe(d.Seconds())
}

// SetSessionCounts publishes one bounded registry snapshot.
func (m *Metrics) SetSessionCounts(counts SessionCounts) {
	m.sessionsRegistered.Set(float64(counts.Registered))

	states := [...]struct {
		name  string
		count int
	}{
		{name: "starting", count: counts.Starting},
		{name: "running", count: counts.Running},
		{name: "finished", count: counts.Finished},
		{name: "failed", count: counts.Failed},
	}
	for _, state := range states {
		m.sessionsByState.WithLabelValues(state.name).Set(float64(state.count))
	}
}
