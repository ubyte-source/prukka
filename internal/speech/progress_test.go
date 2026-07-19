package speech

import (
	"strings"
	"testing"
)

func TestWriterReporterRendersPhasesAndTenths(t *testing.T) {
	t.Parallel()

	var out strings.Builder
	report := WriterReporter(&out)

	report(Progress{Phase: PhaseDownload, Item: "voice-it", DoneBytes: 0, TotalBytes: 100})
	report(Progress{Phase: PhaseDownload, Item: "voice-it", DoneBytes: 4, TotalBytes: 100})
	report(Progress{Phase: PhaseDownload, Item: "voice-it", DoneBytes: 50, TotalBytes: 100})
	report(Progress{Phase: PhaseDownload, Item: "voice-it", DoneBytes: 55, TotalBytes: 100})
	report(Progress{Phase: PhaseVerify, Item: "voice-it"})
	report(Progress{Phase: PhaseDone, Item: "voice-it"})

	rendered := out.String()
	for _, want := range []string{"downloading", "0%", "50%", "verify", "done"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("output misses %q:\n%s", want, rendered)
		}
	}
	if strings.Count(rendered, "50%") != 1 {
		t.Fatalf("tenth reported twice:\n%s", rendered)
	}
}

func TestWriterReporterToleratesNilWriterViaSayf(t *testing.T) {
	t.Parallel()

	sayf(nil, "dropped %d", 1)
}

func TestFormatBytes(t *testing.T) {
	t.Parallel()

	if got := formatBytes(3 << 20); got != "3 MiB" {
		t.Fatalf("mib: %s", got)
	}
	if got := formatBytes(512); got != "512 B" {
		t.Fatalf("bytes: %s", got)
	}
}
