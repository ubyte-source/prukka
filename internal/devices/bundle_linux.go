//go:build linux && bundleddrivers

package devices

import _ "embed"

// srcPayload is the kernel-module source tree; modules are version-coupled to
// the running kernel, so they compile on install.
//
//go:embed assets/linux/src.tar.gz
var srcPayload []byte

func payloads() (map[string][]byte, error) {
	return map[string][]byte{payloadSrc: srcPayload}, nil
}
