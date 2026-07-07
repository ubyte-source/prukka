//go:build windows && bundleddrivers

package devices

import _ "embed"

// webcamPayload is the CI-built user-mode Media Foundation camera:
// PrukkaWebcam.dll plus its PrukkaWebcamCtl.exe controller.
//
//go:embed assets/windows/webcam.tar.gz
var webcamPayload []byte

// payloads exposes the embedded driver archives by device name.
func payloads() (map[string][]byte, error) {
	return map[string][]byte{string(Webcam): webcamPayload}, nil
}
