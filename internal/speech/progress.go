package speech

import (
	"fmt"
	"io"

	"github.com/ubyte-source/prukka/internal/besteffort"
)

// Progress phases, in the order one artifact moves through them.
const (
	PhaseDownload = "download"
	PhaseVerify   = "verify"
	PhaseInstall  = "install"
	PhaseDone     = "done"
)

// Progress is one advance of an engine operation; TotalBytes is zero in the
// phases without a byte dimension.
type Progress struct {
	Phase      string
	Item       string
	DoneBytes  int64
	TotalBytes int64
}

// Reporter receives progress updates (nil disables reporting) and must not
// block: the installer calls it on the download path.
type Reporter func(Progress)

// report forwards one update; a nil Reporter drops it.
func (r Reporter) report(p Progress) {
	if r != nil {
		r(p)
	}
}

// WriterReporter renders progress as human lines: one per phase change, one per
// ten percent downloaded.
func WriterReporter(w io.Writer) Reporter {
	lastTenth := int64(-1)
	lastItem := ""

	return func(p Progress) {
		if p.Phase != PhaseDownload {
			besteffort.Linef(w, "%s: %s", p.Item, p.Phase)

			return
		}
		if p.Item != lastItem {
			lastItem = p.Item
			lastTenth = -1
			besteffort.Linef(w, "%s: downloading %s", p.Item, formatBytes(p.TotalBytes))
		}
		if tenth := p.DoneBytes * 10 / max(p.TotalBytes, 1); tenth != lastTenth {
			lastTenth = tenth
			besteffort.Linef(w, "%s: %d%%", p.Item, min(tenth*10, 100))
		}
	}
}

// formatBytes renders a size for humans.
func formatBytes(n int64) string {
	const mib = 1 << 20
	if n >= mib {
		return fmt.Sprintf("%.0f MiB", float64(n)/mib)
	}

	return fmt.Sprintf("%d B", n)
}
