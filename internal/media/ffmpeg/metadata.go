package ffmpeg

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

const (
	manifestName = "ffmpeg-build.json"
	noticeName   = "FFMPEG-NOTICE.txt"
	licenseName  = "GPL-3.0.txt"

	distributionMode = "upstream-direct-download/not-distributed-in-prukka-release"
	mirrorStatus     = "blocked-pending-complete-corresponding-source"
)

//go:embed GPL-3.0.txt
var gpl3License []byte

type installManifest struct {
	Component           string `json:"component"`
	Execution           string `json:"execution"`
	Platform            string `json:"platform"`
	Vendor              string `json:"vendor"`
	Version             string `json:"version"`
	Commit              string `json:"commit"`
	License             string `json:"license"`
	BinaryURL           string `json:"binary_url"`
	ArchiveSHA256       string `json:"archive_sha256"`
	ExecutableSHA256    string `json:"executable_sha256"`
	FFmpegSourceURL     string `json:"ffmpeg_source_url"`
	FFmpegSourceSHA256  string `json:"ffmpeg_source_sha256"`
	BuildRecipeURL      string `json:"build_recipe_url"`
	BuildRecipeRevision string `json:"build_recipe_revision"`
	BuildInfoURL        string `json:"build_info_url"`
	BuildConfig         string `json:"build_config"`
	DistributionMode    string `json:"distribution_mode"`
	MirrorStatus        string `json:"mirror_status"`
	SchemaVersion       int    `json:"schema_version"`
}

func manifestFor(platform string, b *build, executableSHA string) installManifest {
	return installManifest{
		SchemaVersion:       1,
		Component:           "FFmpeg",
		Execution:           "separate-process",
		Platform:            platform,
		Vendor:              b.vendor,
		Version:             b.version,
		Commit:              b.commit,
		License:             b.license,
		BinaryURL:           b.binaryURL,
		ArchiveSHA256:       b.archiveSHA256,
		ExecutableSHA256:    executableSHA,
		FFmpegSourceURL:     b.sourceURL,
		FFmpegSourceSHA256:  b.sourceSHA256,
		BuildRecipeURL:      b.recipeURL,
		BuildRecipeRevision: b.recipeRevision,
		BuildInfoURL:        b.buildInfoURL,
		BuildConfig:         b.buildConfig,
		DistributionMode:    distributionMode,
		MirrorStatus:        mirrorStatus,
	}
}

func marshalManifest(manifest *installManifest) ([]byte, error) {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode ffmpeg manifest: %w", err)
	}

	return append(data, '\n'), nil
}

func noticeFor(manifest *installManifest) []byte {
	return fmt.Appendf(nil, `Prukka managed FFmpeg runtime

FFmpeg runs as a separate process and is not linked into Prukka.
Platform: %s
Vendor: %s
Version: %s
Commit: %s
License: %s
Binary: %s
Archive SHA-256: %s
Executable SHA-256: %s
FFmpeg source: %s
FFmpeg source SHA-256: %s
Build recipe: %s
Build recipe revision: %s
Build information: %s
Build configuration: %s
Distribution mode: %s
Mirror status: %s

The source URL identifies FFmpeg itself. Prukka's binary-mirror gate requires
the complete corresponding source for the exact statically linked build.
`, manifest.Platform, manifest.Vendor, manifest.Version, manifest.Commit,
		manifest.License, manifest.BinaryURL, manifest.ArchiveSHA256,
		manifest.ExecutableSHA256, manifest.FFmpegSourceURL, manifest.FFmpegSourceSHA256, manifest.BuildRecipeURL,
		manifest.BuildRecipeRevision, manifest.BuildInfoURL, manifest.BuildConfig,
		manifest.DistributionMode, manifest.MirrorStatus)
}
