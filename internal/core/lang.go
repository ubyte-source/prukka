package core

import "strings"

// Lang is a validated BCP-47 tag ("it", "de-CH"); construct from user
// input with core/lang, code past that boundary trusts it.
type Lang string

// LangAuto asks providers to detect the source language instead of pinning it.
const LangAuto Lang = ""

// Base returns the ISO 639-1 base of a language tag: "en" for "en-US".
func (l Lang) Base() Lang {
	base, _, _ := strings.Cut(string(l), "-")

	return Lang(base)
}

// SameLang reports whether two tags share a base language, case-insensitively.
func SameLang(a, b Lang) bool {
	return strings.EqualFold(string(a.Base()), string(b.Base()))
}
