//go:build !darwin && !windows

package ffmpeg

import "testing"

func TestNativeVideoDeviceIsUnavailableWithoutIntegration(t *testing.T) {
	t.Parallel()

	if nativeVideoAvailable(t.Context()) {
		t.Fatal("native video reported available on an unsupported platform")
	}

	s := NewSupervisor("", nil)
	if _, err := s.startNativeVideoDevice(t.Context(), "index.m3u8"); err == nil {
		t.Fatal("unsupported native feeder started")
	}
}
