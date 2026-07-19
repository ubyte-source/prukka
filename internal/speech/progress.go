package speech

import (
	"fmt"
	"io"
)

// Progress phases, in the order one artifact moves through them.
const (
	PhaseDownload = "download"
	PhaseVerify   = "verify"
	PhaseInstall  = "install"
	PhaseDone     = "done"
)

// Progress is one advance of an engine operation. TotalBytes is the catalog
// size during download and zero for the phases without a byte dimension.
type Progress struct {
	Phase      string
	Item       string
	DoneBytes  int64
	TotalBytes int64
}

// Reporter receives progress updates; nil disables reporting. Implementations
// must not block: the installer calls them on the download path.
type Reporter func(Progress)

// WriterReporter renders progress as human lines: one line per phase change
// and one per ten percent downloaded, matching the setup command's tone.
func WriterReporter(w io.Writer) Reporter {
	lastTenth := int64(-1)
	lastItem := ""

	return func(p Progress) {
		if p.Phase != PhaseDownload {
			sayf(w, "%s: %s", p.Item, p.Phase)

			return
		}
		if p.Item != lastItem {
			lastItem = p.Item
			lastTenth = -1
			sayf(w, "%s: downloading %s", p.Item, formatBytes(p.TotalBytes))
		}
		if tenth := p.DoneBytes * 10 / max(p.TotalBytes, 1); tenth != lastTenth {
			lastTenth = tenth
			sayf(w, "%s: %d%%", p.Item, min(tenth*10, 100))
		}
	}
}

// sayf writes one progress line; progress is best-effort by contract.
func sayf(w io.Writer, format string, args ...any) {
	if w == nil {
		return
	}

	if _, err := fmt.Fprintf(w, format+"\n", args...); err != nil {
		return
	}
}

// formatBytes renders a size for humans without importing a formatting
// dependency: engine artifacts range from tens to hundreds of MiB.
func formatBytes(n int64) string {
	const mib = 1 << 20
	if n >= mib {
		return fmt.Sprintf("%.0f MiB", float64(n)/mib)
	}

	return fmt.Sprintf("%d B", n)
}
