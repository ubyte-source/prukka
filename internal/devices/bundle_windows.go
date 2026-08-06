//go:build windows && bundleddrivers

package devices

import _ "embed"

// webcamPayload is the CI-built user-mode Media Foundation camera.
//
//go:embed assets/windows/webcam.tar.gz
var webcamPayload []byte

func payloads() (map[string][]byte, error) {
	return map[string][]byte{string(Webcam): webcamPayload}, nil
}
