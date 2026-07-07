package meter

// mtOverBudget is how far past the cap translation keeps running; dubbing
// is already cut at the cap.
const mtOverBudget = 1.5

// Guard gates provider stages by spend: dubbing pauses first, translation
// next, captions last.
type Guard struct {
	book     *Book
	capEUR   float64
	hardStop bool
}

// NewGuard wires a spend guard: zero cap = unlimited; hardStop pauses
// captions too, otherwise only the paid stages degrade.
func NewGuard(book *Book, capEURPerHour float64, hardStop bool) *Guard {
	return &Guard{book: book, capEUR: capEURPerHour, hardStop: hardStop}
}

// AllowSTT reports whether transcription may spend for the session.
// Captions are last to go: without hard stop they never pause.
func (g *Guard) AllowSTT(session string) bool {
	if g.capEUR == 0 {
		return true
	}

	if g.hardStop {
		return g.book.SessionRate(session) < g.capEUR
	}

	return true
}

// AllowMT reports whether translation may spend. It pauses once spend runs
// well past the cap (or immediately, under hard stop).
func (g *Guard) AllowMT(session string) bool {
	if g.capEUR == 0 {
		return true
	}

	if g.hardStop {
		return g.book.SessionRate(session) < g.capEUR
	}

	return g.book.SessionRate(session) < g.capEUR*mtOverBudget
}

// AllowTTS reports whether dubbing may spend. Dubbing is the most expensive
// stage and the first to pause at the cap.
func (g *Guard) AllowTTS(session string) bool {
	if g.capEUR == 0 {
		return true
	}

	return g.book.SessionRate(session) < g.capEUR
}
