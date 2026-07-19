// Package enginebundle owns the on-disk layout of a downloaded engine bundle:
// the helper executable names and the model directory structure. The installer
// (internal/speech) writes this layout, the engine helpers (internal/speechengine)
// read it, and doctor verifies it — so the names live in exactly one place and
// the three sides can never disagree on where a model or binary sits.
//
// Executable names here carry no platform suffix; each caller applies the
// ".exe" it needs (the installer stages one set of files, doctor probes with
// and without the suffix). The model directory names are the whole contract.
package enginebundle

import "path/filepath"

// Helper executable base names, relative to the bundle root. Piper lives in
// its own directory beside the library it links.
const (
	WhisperServer = "whisper-server"
	MT            = "mt"
	PiperDir      = "piper"
	PiperExe      = "piper"
)

// ModelsDir is the bundle's model root.
const ModelsDir = "models"

// Piper is the TTS engine executable path (piper/piper), relative to the
// bundle root.
func Piper() string { return filepath.Join(PiperDir, PiperExe) }

// MTPackName is the identifier of the from->to translation model: it names
// both the catalog pack and its model directory, so the two cannot drift.
func MTPackName(from, to string) string { return MT + "-" + from + "-" + to }

// MTModelDir is the directory holding the from->to translation model, relative
// to the bundle root.
func MTModelDir(from, to string) string {
	return filepath.Join(ModelsDir, MTPackName(from, to))
}
