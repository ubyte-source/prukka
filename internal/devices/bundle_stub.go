//go:build !bundleddrivers

package devices

// payloads reports that this build embeds no driver archives; release
// builds compile the bundleddrivers variant instead.
func payloads() (map[string][]byte, error) {
	return nil, ErrNotBundled
}
