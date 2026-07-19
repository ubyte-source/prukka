//go:build darwin && bundleddrivers

package devices

import _ "embed"

// The CI-built driver archives: HAL bundles for the audio devices and
// the camera-extension app for the webcam.
var (
	//go:embed assets/darwin/microphone.tar.gz
	microphonePayload []byte
	//go:embed assets/darwin/speaker.tar.gz
	speakerPayload []byte
	//go:embed assets/darwin/webcam.tar.gz
	webcamPayload []byte
)

// payloads exposes the embedded driver archives by device name.
func payloads() (map[string][]byte, error) {
	return map[string][]byte{
		string(Microphone): microphonePayload,
		string(Speaker):    speakerPayload,
		string(Webcam):     webcamPayload,
	}, nil
}
