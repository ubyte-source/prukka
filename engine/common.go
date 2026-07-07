package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	goosWindows   = "windows"
	minSampleRate = 8000
	maxSampleRate = 192000
	// languageAuto asks whisper to detect the spoken language instead of pinning
	// it, and marks a hint as unset for firstNonAuto.
	languageAuto = "auto"
)

func validSampleRate(rate int) bool {
	return rate >= minSampleRate && rate <= maxSampleRate
}

// engineDir is the directory holding this binary and its bundled tools/models.
func engineDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}

	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		resolved = exe
	}

	return filepath.Dir(resolved)
}

// libDir holds the shared libraries the compiled helpers dlopen at runtime
// (whisper, CTranslate2, SentencePiece); it feeds DYLD_LIBRARY_PATH.
func libDir(dir string) string {
	return filepath.Join(dir, "lib")
}

// baseLanguageTag reduces a BCP-47 tag to its base subtag: whisper's -l accepts
// only "auto" or a bare ISO-639-1 code, so "it-CH" must reach it as "it". MT
// pair directories are named from base tags too, so this is safe for both.
func baseLanguageTag(value string) string {
	if value == languageAuto {
		return value
	}

	base, _, _ := strings.Cut(value, "-")

	return base
}

func validLanguageArg(value string, allowAuto bool) bool {
	if allowAuto && value == languageAuto {
		return true
	}

	parts := strings.Split(value, "-")
	if len(parts[0]) < 2 || len(parts[0]) > 3 || !asciiLetters(parts[0]) {
		return false
	}
	for _, part := range parts[1:] {
		if part == "" || len(part) > 8 || !asciiAlphaNumeric(part) {
			return false
		}
	}

	return true
}

func asciiLetters(value string) bool {
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') {
			return false
		}
	}

	return true
}

func asciiAlphaNumeric(value string) bool {
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') {
			return false
		}
	}

	return true
}

func bundlePath(dir, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}

	return filepath.Join(dir, filepath.Clean(path))
}

func libraryEnv(env []string, dir string) []string {
	key := "LD_LIBRARY_PATH"
	switch runtime.GOOS {
	case "darwin":
		key = "DYLD_LIBRARY_PATH"
	case goosWindows:
		key = "PATH"
	}

	value := dir
	if current := os.Getenv(key); current != "" {
		value += string(os.PathListSeparator) + current
	}

	return append(env, key+"="+value)
}
