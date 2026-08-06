// Package enginebundle owns the on-disk layout of a downloaded engine bundle:
// the helper executable names and the model directory structure. Executable
// names here carry no platform suffix; each caller applies the ".exe" it needs.
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

// Piper is the TTS engine executable path, relative to the bundle root.
func Piper() string { return filepath.Join(PiperDir, PiperExe) }

// MTPackName is the identifier of the from->to translation model, naming both
// the catalog pack and its model directory.
func MTPackName(from, to string) string { return MT + "-" + from + "-" + to }

// MTModelDir is the directory holding the from->to translation model, relative
// to the bundle root.
func MTModelDir(from, to string) string {
	return filepath.Join(ModelsDir, MTPackName(from, to))
}
