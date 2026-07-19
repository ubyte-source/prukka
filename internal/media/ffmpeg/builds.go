// Package ffmpeg locates, installs and supervises ffmpeg — the only
// package authorized to exec with computed arguments.
package ffmpeg

import (
	"fmt"
	"runtime"
)

const (
	kindZip   = "zip"
	kindTarXz = "tar.xz"

	ffmpegLicense = "GPL-3.0-or-later"

	btbnRelease = "https://github.com/BtbN/FFmpeg-Builds/releases/download/" +
		"autobuild-2026-07-18-13-13/"
	btbnRevision = "928c1147c234f29a0479806e19c41d7d62ebce7e"
	btbnRecipe   = "https://github.com/BtbN/FFmpeg-Builds/tree/" + btbnRevision
	btbnRaw      = "https://raw.githubusercontent.com/BtbN/FFmpeg-Builds/" + btbnRevision + "/"
	btbnCommit   = "94138f6973dd1ac6208ace92148ac0d172455d65"
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

// builds contains immutable, checksum-verified archives. Prukka downloads
// these executables on explicit setup; it does not link or ship them.
var builds = map[string]build{
	"linux/amd64": btbnBuild(
		"linux64",
		"ffmpeg-n8.1.2-22-g94138f6973-linux64-gpl-8.1.tar.xz",
		"e6e10d339d72618674ce085f2faf4f9490173750adafe246845125febda2f376",
		kindTarXz,
	),
	"linux/arm64": btbnBuild(
		"linuxarm64",
		"ffmpeg-n8.1.2-22-g94138f6973-linuxarm64-gpl-8.1.tar.xz",
		"762632ff901ca869d6ec96fb5906cba29e04809e3b1329557e6226f699e917dd",
		kindTarXz,
	),
	"windows/amd64": btbnBuild(
		"win64",
		"ffmpeg-n8.1.2-22-g94138f6973-win64-gpl-8.1.zip",
		"764716fa5cff555431791945f15db61c0aa85cc61023bfe36668f32f2b256b62",
		kindZip,
	),
	"windows/arm64": btbnBuild(
		"winarm64",
		"ffmpeg-n8.1.2-22-g94138f6973-winarm64-gpl-8.1.zip",
		"5d54d9b65b9e289f76f059b8bf4873a6786d8733e8b8c20f9dcf6c30f32c6cb7",
		kindZip,
	),
	"darwin/amd64": martinBuild(
		"amd64/1783018342_8.1.2",
		"a52ef43883f44c219766d4b3bdde4e635b35465d0b704c01c3a0566b59775df9",
	),
	"darwin/arm64": martinBuild(
		"arm64/1783011502_8.1.2",
		"ef1aa60006c7b77ce170c1608c08d8e4ba1c30c5746f2ac986ded932d0ac2c3c",
	),
}

func btbnBuild(target, archive, checksum, kind string) build {
	return build{
		vendor:         "BtbN/FFmpeg-Builds",
		version:        "n8.1.2-22-g94138f6973",
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
		return ffmpegName + ".exe"
	}

	return ffmpegName
}
