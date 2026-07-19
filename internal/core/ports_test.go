package core

import "testing"

func TestVoiceSupportsLanguageFamily(t *testing.T) {
	t.Parallel()

	english := Voice{ID: "lessac", Lang: "en-US"}
	if !english.Supports("en-GB") {
		t.Fatal("English voice rejected an English regional target")
	}
	if english.Supports("it") {
		t.Fatal("English voice accepted an Italian target")
	}
	if !(Voice{ID: "multilingual"}).Supports("it") {
		t.Fatal("multilingual voice rejected a target")
	}
}
