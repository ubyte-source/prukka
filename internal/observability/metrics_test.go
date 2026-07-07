package observability_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/observability"
)

// scrape renders the metrics registry as text.
func scrape(t *testing.T, m *observability.Metrics) string {
	t.Helper()

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), "GET", "/metrics", http.NoBody)
	m.Handler().ServeHTTP(rec, req)

	body, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatalf("read metrics: %v", err)
	}

	return string(body)
}

func TestMetricsRecordAndExpose(t *testing.T) {
	t.Parallel()

	m := observability.NewMetrics()

	m.StageLatency("stt", 1500*time.Millisecond)
	m.E2ELatency("caption", 2*time.Second)
	m.SetSessionsActive(3)
	m.ProviderError("openrouter", "429")
	m.AddCost("demo", "tts", 0.0002)
	m.SetFallback(true)

	out := scrape(t, m)

	for _, want := range []string{
		`prukka_stage_latency_seconds_count{stage="stt"} 1`,
		`prukka_e2e_latency_seconds_count{kind="caption"} 1`,
		`prukka_sessions_active 3`,
		`prukka_provider_errors_total{code="429",provider="openrouter"} 1`,
		`prukka_cost_eur_total{kind="tts",session="demo"} 0.0002`,
		`prukka_fallback_active 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics output missing %q", want)
		}
	}
}

func TestSessionsGaugeReflectsLatestSet(t *testing.T) {
	t.Parallel()

	m := observability.NewMetrics()
	m.SetSessionsActive(5)
	m.SetSessionsActive(2)

	if !strings.Contains(scrape(t, m), "prukka_sessions_active 2") {
		t.Fatal("gauge did not reflect the latest value")
	}
}

func TestDispatchQueueGaugesComputeAtScrape(t *testing.T) {
	t.Parallel()

	m := observability.NewMetrics()

	depth := 0
	m.RegisterDispatchQueue(
		func() float64 { return float64(depth) },
		func() float64 { return 256 },
	)

	// The depth gauge is evaluated at scrape time, so a later change shows.
	depth = 7
	out := scrape(t, m)

	if !strings.Contains(out, "prukka_dispatch_queue_depth 7") {
		t.Fatalf("queue depth gauge missing or stale:\n%s", out)
	}

	if !strings.Contains(out, "prukka_dispatch_queue_capacity 256") {
		t.Fatalf("queue capacity gauge missing:\n%s", out)
	}
}
