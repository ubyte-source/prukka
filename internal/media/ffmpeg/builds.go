// Package ffmpeg locates, installs and supervises ffmpeg; it owns external
// media-process execution.
package ffmpeg

import (
	"fmt"
	"runtime"

	"github.com/ubyte-source/prukka/internal/hostos"
)

const (
	kindZip   = "zip"
	kindTarXz = "tar.xz"

	archAMD64 = "amd64"
	archARM64 = "arm64"

	ffmpegLicense = "GPL-3.0-or-later"

	btbnRelease = "https://github.com/BtbN/FFmpeg-Builds/releases/download/" +
		"autobuild-2026-08-02-13-17/"
	btbnRevision = "a99e8230eae00d1cee38f23076a7a1f55cd984e2"
	btbnRecipe   = "https://github.com/BtbN/FFmpeg-Builds/tree/" + btbnRevision
	btbnRaw      = "https://raw.githubusercontent.com/BtbN/FFmpeg-Builds/" + btbnRevision + "/"
	btbnCommit   = "9b6c8969e05b4f0b29f0f85cd501be6b3e582e6b"
	btbnSource   = "https://github.com/FFmpeg/FFmpeg/archive/" + btbnCommit + ".tar.gz"

	martinRelease  = "https://ffmpeg.martin-riedl.de/download/macos/"
	martinRevision = "bb1d6db29cee948f9685bcd69e6caf17d960662b"
	martinRecipe   = "https://git.martin-riedl.de/ffmpeg/build-script/src/commit/" + martinRevision
	martinCommit   = "38b88335f99e76ed89ff3c93f877fdefce736c13"
	martinSource   = "https://ffmpeg.org/releases/ffmpeg-8.1.2.tar.xz"
)

// build identifies one downloadable FFmpeg executable and the upstream
// material required to audit how it was produced.
type build struct {
	vendor         string
	version        string
	commit         string
	license        string
	binaryURL      string
	archiveSHA256  string
	kind           string
	sourceURL      string
	sourceSHA256   string
	recipeURL      string
	recipeRevision string
	buildInfoURL   string
	buildConfig    string
}

// buildTarget is the comparable identity of one pinned archive: the GOOS and
// GOARCH it was published for.
type buildTarget struct {
	os   string
	arch string
}

func (p buildTarget) String() string { return p.os + "/" + p.arch }

// hostTarget is the build target of the machine this binary runs on.
func hostTarget() buildTarget {
	return buildTarget{os: runtime.GOOS, arch: runtime.GOARCH}
}

// builds contains immutable, checksum-verified archives; Prukka downloads them
// on explicit setup and neither links nor ships them.
var builds = map[buildTarget]build{
	{os: hostos.Linux, arch: archAMD64}: btbnBuild(
		"linux64",
		"ffmpeg-n8.1.2-34-g9b6c8969e0-linux64-gpl-8.1.tar.xz",
		"f34f7ed9b02f54d96f5622393f3a36fc65b3c5b3bb503bb04a3225fde68d71e0",
		kindTarXz,
	),
	{os: hostos.Linux, arch: archARM64}: btbnBuild(
		"linuxarm64",
		"ffmpeg-n8.1.2-34-g9b6c8969e0-linuxarm64-gpl-8.1.tar.xz",
		"f804780ef2813763a329d68334fa9c99065ec58347768732ce7d3350a3048f52",
		kindTarXz,
	),
	{os: hostos.Windows, arch: archAMD64}: btbnBuild(
		"win64",
		"ffmpeg-n8.1.2-34-g9b6c8969e0-win64-gpl-8.1.zip",
		"5815b40b78161636bfc93fe7105242eb9a8fc3233dd8c15b28344b36f58f7548",
		kindZip,
	),
	{os: hostos.Windows, arch: archARM64}: btbnBuild(
		"winarm64",
		"ffmpeg-n8.1.2-34-g9b6c8969e0-winarm64-gpl-8.1.zip",
		"d9f73d0f9ceeb8d22003d604489e9ab316a298c92584f951a2a61f7278795806",
		kindZip,
	),
	{os: hostos.Darwin, arch: archAMD64}: martinBuild(
		"amd64/1783018342_8.1.2",
		"a52ef43883f44c219766d4b3bdde4e635b35465d0b704c01c3a0566b59775df9",
	),
	{os: hostos.Darwin, arch: archARM64}: martinBuild(
		"arm64/1783011502_8.1.2",
		"ef1aa60006c7b77ce170c1608c08d8e4ba1c30c5746f2ac986ded932d0ac2c3c",
	),
}

func btbnBuild(target, archive, checksum, kind string) build {
	return build{
		vendor:         "BtbN/FFmpeg-Builds",
		version:        "n8.1.2-34-g9b6c8969e0",
		commit:         btbnCommit,
		license:        ffmpegLicense,
		binaryURL:      btbnRelease + archive,
		archiveSHA256:  checksum,
		kind:           kind,
		sourceURL:      btbnSource,
		sourceSHA256:   "41cc834ca4c8b63733b5ea9d215aed7b6cf8e29c4f96c8c61aff187d47e02937",
		recipeURL:      btbnRecipe,
		recipeRevision: btbnRevision,
		buildInfoURL:   btbnRaw + "variants/" + target + "-gpl.sh",
		buildConfig:    "./makeimage.sh " + target + " gpl 8.1 && ./build.sh " + target + " gpl 8.1",
	}
}

func martinBuild(path, checksum string) build {
	base := martinRelease + path + "/"

	return build{
		vendor:         "Martin Riedl FFmpeg Build Server",
		version:        "8.1.2",
		commit:         martinCommit,
		license:        ffmpegLicense,
		binaryURL:      base + "ffmpeg.zip",
		archiveSHA256:  checksum,
		kind:           kindZip,
		sourceURL:      martinSource,
		sourceSHA256:   "464beb5e7bf0c311e68b45ae2f04e9cc2af88851abb4082231742a74d97b524c",
		recipeURL:      martinRecipe,
		recipeRevision: martinRevision,
		buildInfoURL:   base + "versions.txt",
		buildConfig:    "./build.sh -FFMPEG_SNAPSHOT=NO",
	}
}

// platformBuild returns this platform's pinned build.
func platformBuild() (build, error) {
	host := hostTarget()

	b, ok := builds[host]
	if !ok {
		return build{}, fmt.Errorf("no pinned ffmpeg build for %s — install ffmpeg with your package manager", host)
	}

	return b, nil
}

// binaryName is the platform's executable name.
func binaryName() string {
	if runtime.GOOS == hostos.Windows {
		return ffmpegName + ".exe"
	}

	return ffmpegName
}
