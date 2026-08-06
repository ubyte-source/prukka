//go:build darwin && bundleddrivers

package devices

import _ "embed"

var (
	//go:embed assets/darwin/microphone.tar.gz
	microphonePayload []byte
	//go:embed assets/darwin/speaker.tar.gz
	speakerPayload []byte
	//go:embed assets/darwin/webcam.tar.gz
	webcamPayload []byte
)

func payloads() (map[string][]byte, error) {
	return map[string][]byte{
		string(Microphone): microphonePayload,
		string(Speaker):    speakerPayload,
		string(Webcam):     webcamPayload,
	}, nil
}
