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

	m.E2ELatency("caption", 2*time.Second)
	m.SetSessionCounts(observability.SessionCounts{
		Registered: 10,
		Starting:   1,
		Running:    2,
		Finished:   3,
		Failed:     4,
	})

	out := scrape(t, m)

	for _, want := range []string{
		`prukka_e2e_latency_seconds_count{kind="caption"} 1`,
		`prukka_sessions_registered 10`,
		`prukka_sessions_by_state{state="starting"} 1`,
		`prukka_sessions_by_state{state="running"} 2`,
		`prukka_sessions_by_state{state="finished"} 3`,
		`prukka_sessions_by_state{state="failed"} 4`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics output missing %q", want)
		}
	}

	for _, stale := range []string{"prukka_stage_latency", "prukka_fallback_active"} {
		if strings.Contains(out, stale) {
			t.Errorf("metrics output contains retired collector %q", stale)
		}
	}
}

func TestSessionGaugesReflectLatestSnapshot(t *testing.T) {
	t.Parallel()

	m := observability.NewMetrics()
	m.SetSessionCounts(observability.SessionCounts{Registered: 5, Running: 5})
	m.SetSessionCounts(observability.SessionCounts{Registered: 2, Finished: 1, Failed: 1})

	out := scrape(t, m)
	for _, want := range []string{
		"prukka_sessions_registered 2",
		`prukka_sessions_by_state{state="running"} 0`,
		`prukka_sessions_by_state{state="finished"} 1`,
		`prukka_sessions_by_state{state="failed"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("latest session snapshot missing %q:\n%s", want, out)
		}
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
