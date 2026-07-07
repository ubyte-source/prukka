// Package ffmpeg locates, installs and supervises ffmpeg — the only
// package authorized to exec with computed arguments.
package ffmpeg

import (
	"fmt"
	"runtime"
)

// Archive kinds of the pinned builds.
const (
	kindZip   = "zip"
	kindTarXz = "tar.xz"
)

// build pins one downloadable static ffmpeg.
type build struct {
	url    string
	sha256 string
	// kind is the archive format: kindZip or kindTarXz.
	kind string
}

// btbn is the immutable dated autobuild release the Linux and Windows pins
// come from; its checksums are published upstream alongside the assets.
const btbn = "https://github.com/BtbN/FFmpeg-Builds/releases/download/autobuild-2026-07-03-13-21/"

// builds pins checksum-verified static ffmpeg per platform; the repository
// ships only this downloader, never the GPL binaries.
var builds = map[string]build{
	"linux/amd64": {
		url:    btbn + "ffmpeg-n8.1.2-21-gce3c09c101-linux64-gpl-8.1.tar.xz",
		sha256: "7aadf7d95d94e9dc71d4283d64be209ef1ba4cab5eb09893c29037223704d0b1",
		kind:   kindTarXz,
	},
	"linux/arm64": {
		url:    btbn + "ffmpeg-n8.1.2-21-gce3c09c101-linuxarm64-gpl-8.1.tar.xz",
		sha256: "147400f5b6fd2486523f0b010191b5d5c58aaa44c56a296cbd1a3f130cb59329",
		kind:   kindTarXz,
	},
	"windows/amd64": {
		url:    btbn + "ffmpeg-n8.1.2-21-gce3c09c101-win64-gpl-8.1.zip",
		sha256: "68d17ffe72af5254c9ef3912b7ef5d7dae2c01e9006debfdc2279737d8fb0161",
		kind:   kindZip,
	},
	"windows/arm64": {
		url:    btbn + "ffmpeg-n8.1.2-21-gce3c09c101-winarm64-gpl-8.1.zip",
		sha256: "c0b38810a44d5a8af96fe43117e4263d288ab3957c3d95d7cc6e6af5dc32d5f2",
		kind:   kindZip,
	},
	"darwin/amd64": {
		url:    "https://evermeet.cx/ffmpeg/ffmpeg-8.1.2.zip",
		sha256: "e91df72a1ee7c26606f90dd2dd4dcccc6a75140ff9ea6fdd50faae828b82ba69",
		kind:   kindZip,
	},
	"darwin/arm64": {
		url:    "https://ffmpeg.martin-riedl.de/download/macos/arm64/1783164229_N-125450-gfad2e0bc50/ffmpeg.zip",
		sha256: "b5ce8b77f8c0686e4f68a46f8e4d094fe7e6f4ded50bcfacd9944ad1efdf66e9",
		kind:   kindZip,
	},
}

// platformBuild returns this platform's pinned build.
func platformBuild() (build, error) {
	key := runtime.GOOS + "/" + runtime.GOARCH

	b, ok := builds[key]
	if !ok {
		return build{}, fmt.Errorf("no pinned ffmpeg build for %s — install ffmpeg with your package manager", key)
	}

	return b, nil
}

// binaryName is the platform's executable name.
func binaryName() string {
	if runtime.GOOS == osWindows {
		return "ffmpeg.exe"
	}

	return "ffmpeg"
}
