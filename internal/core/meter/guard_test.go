package meter_test

import (
	"testing"

	"github.com/ubyte-source/prukka/internal/core/meter"
)

// spend adds a large one-shot charge so the sliding-hour rate exceeds the
// cap on the same minute.
func spend(b *meter.Book, eur float64) {
	b.Add("demo", "tts", 0, eur)
}

func TestGuardDegradesInOrder(t *testing.T) {
	t.Parallel()

	c := &clock{}
	book := meter.NewBook(c.now)
	guard := meter.NewGuard(book, 3.0, false)

	// Under budget: every stage runs.
	spend(book, 1.0)

	if !guard.AllowSTT("demo") || !guard.AllowMT("demo") || !guard.AllowTTS("demo") {
		t.Fatal("under budget, all stages must run")
	}

	// At the cap: dubbing pauses first, captions and translation continue.
	spend(book, 2.5) // total 3.5/h > 3

	if guard.AllowTTS("demo") {
		t.Fatal("over cap: TTS must pause first")
	}

	if !guard.AllowMT("demo") || !guard.AllowSTT("demo") {
		t.Fatal("over cap without hard stop: captions and translation continue")
	}

	// Far over the cap (past 1.5×): translation pauses too, captions last.
	spend(book, 2.0) // total 5.5/h > 4.5

	if guard.AllowMT("demo") {
		t.Fatal("well over cap: MT must pause")
	}

	if !guard.AllowSTT("demo") {
		t.Fatal("captions are last: STT continues without hard stop")
	}
}

func TestGuardHardStopHaltsEverything(t *testing.T) {
	t.Parallel()

	c := &clock{}
	book := meter.NewBook(c.now)
	guard := meter.NewGuard(book, 3.0, true)

	spend(book, 3.5)

	if guard.AllowSTT("demo") || guard.AllowMT("demo") || guard.AllowTTS("demo") {
		t.Fatal("hard stop over cap: every stage must halt")
	}
}

func TestGuardUnlimitedWhenNoCap(t *testing.T) {
	t.Parallel()

	c := &clock{}
	book := meter.NewBook(c.now)
	guard := meter.NewGuard(book, 0, true)

	spend(book, 1000)

	if !guard.AllowSTT("demo") || !guard.AllowMT("demo") || !guard.AllowTTS("demo") {
		t.Fatal("zero cap means unlimited")
	}
}
