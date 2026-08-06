//go:build !darwin

package discover

// OutputStamp has no portable fingerprint on this platform; device outputs
// run unwatched and rely on their own write errors.
func OutputStamp(string) (string, bool) {
	return "", false
}

// OutputIndex is a darwin concern: pulse sinks and WASAPI endpoints are
// addressed by stable names, never positional indexes.
func OutputIndex(string) (int, bool) {
	return 0, false
}
