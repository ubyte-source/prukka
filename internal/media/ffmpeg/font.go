package ffmpeg

import (
	"os"
	"runtime"
	"strings"
)

// DefaultFontFile locates a system font for the burn-in filter (the static
// ffmpeg has no fontconfig); empty means burn-in is unavailable.
func DefaultFontFile() string {
	for _, path := range fontCandidates() {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	return ""
}

// BurnFilter renders the live-overlay drawtext filter; reload=1 re-reads
// the cue file every frame.
func BurnFilter(cueFile, fontFile string) string {
	return "drawtext=textfile=" + escapeFilterArg(cueFile) +
		":reload=1:fontfile=" + escapeFilterArg(fontFile) +
		":fontsize=28:fontcolor=white:borderw=2:bordercolor=black" +
		":x=(w-text_w)/2:y=h-text_h-24"
}

// escapeFilterArg quotes a path for use as a filter option value, where
// backslash, colon and quote are structural — and so are comma, semicolon
// and brackets one level up, in the filtergraph parser.
func escapeFilterArg(arg string) string {
	return strings.NewReplacer(
		`\`, `\\`, ":", `\:`, "'", `\'`, ",", `\,`, ";", `\;`, "[", `\[`, "]", `\]`,
	).Replace(arg)
}

// fontCandidates lists well-known system fonts per platform, most common
// first (mirrors builds.go's runtime.GOOS dispatch).
func fontCandidates() []string {
	switch runtime.GOOS {
	case osDarwin:
		return []string{
			"/System/Library/Fonts/Helvetica.ttc",
			"/System/Library/Fonts/Supplemental/Arial.ttf",
		}
	case osWindows:
		return []string{
			`C:\Windows\Fonts\arial.ttf`,
			`C:\Windows\Fonts\segoeui.ttf`,
		}
	default: // linux and the BSDs
		return []string{
			"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
			"/usr/share/fonts/dejavu/DejaVuSans.ttf",
			"/usr/share/fonts/TTF/DejaVuSans.ttf",
			"/usr/share/fonts/truetype/liberation/LiberationSans-Regular.ttf",
		}
	}
}
