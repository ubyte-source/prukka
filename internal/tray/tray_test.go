package tray

import (
	"bytes"
	"image/png"
	"testing"
)

// TestEmbeddedIconIsAValidPNG: a broken embed would render an invisible
// tray with no error anywhere — pin the asset at build time instead.
func TestEmbeddedIconIsAValidPNG(t *testing.T) {
	t.Parallel()

	img, err := png.Decode(bytes.NewReader(iconPNG))
	if err != nil {
		t.Fatalf("embedded icon does not decode as PNG: %v", err)
	}

	bounds := img.Bounds()
	if bounds.Dx() == 0 || bounds.Dy() == 0 {
		t.Fatalf("embedded icon is empty: %v", bounds)
	}
}
