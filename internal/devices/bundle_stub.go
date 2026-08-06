//go:build !bundleddrivers

package devices

// payloads embeds nothing; release builds compile the bundleddrivers variant.
func payloads() (map[string][]byte, error) {
	return nil, ErrNotBundled
}
