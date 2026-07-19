// Package main generates and verifies the release SBOM set.
package main

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	mainModule          = "github.com/ubyte-source/prukka"
	mainPackage         = mainModule + "/cmd/prukka"
	projectName         = "prukka"
	syftExecutable      = ".tools/bin/syft"
	maxBinaryBytes      = 1 << 30
	maxArchiveFileBytes = 32 << 20
	archAMD64           = "amd64"
	osLinux             = "linux"
	noAssertion         = "NOASSERTION"
	rootLicenseFile     = "LICENSE"
	linuxLicenseFile    = "drivers/linux/LICENSE"
	licenseApache       = "Apache-2.0"
	licenseGPL2         = "GPL-2.0-only"
	licenseEvidence     = "License evidence."
	checksumSHA256      = "SHA256"
	relationContains    = "CONTAINS"
	relationDescribes   = "DESCRIBES"
	relationOther       = "OTHER"
	packageManager      = "PACKAGE-MANAGER"
	spdxDocumentID      = "SPDXRef-DOCUMENT"
	externalPURL        = "purl"
	dashboardPackage    = "prukka-dashboard"
	archiveOwner        = "root"
)

type releaseTarget struct {
	name, archive, goos, goarch string
	drivers                     []string
}

var releaseTargets = []releaseTarget{
	{name: "prukka_darwin_amd64", archive: "prukka_darwin_amd64.tar.gz", goos: "darwin", goarch: archAMD64,
		drivers: []string{"darwin/microphone.tar.gz", "darwin/speaker.tar.gz", "darwin/webcam.tar.gz"}},
	{name: "prukka_darwin_arm64", archive: "prukka_darwin_arm64.tar.gz", goos: "darwin", goarch: "arm64",
		drivers: []string{"darwin/microphone.tar.gz", "darwin/speaker.tar.gz", "darwin/webcam.tar.gz"}},
	{name: "prukka_linux_amd64", archive: "prukka_linux_amd64.tar.gz", goos: osLinux, goarch: archAMD64,
		drivers: []string{"linux/src.tar.gz"}},
	{name: "prukka_linux_arm64", archive: "prukka_linux_arm64.tar.gz", goos: osLinux, goarch: "arm64",
		drivers: []string{"linux/src.tar.gz"}},
	{name: "prukka_windows_amd64", archive: "prukka_windows_amd64.zip", goos: "windows", goarch: archAMD64,
		drivers: []string{"windows/webcam.tar.gz"}},
}

type options struct {
	syftVersion, dist, repo, version string
	commit, goVersion, namespaceBase string
	createdEpoch                     int64
}

type embeddedFile struct {
	name string
	data []byte
}

type npmPackage struct {
	path, name, version, resolved, integrity, license string
	sha512                                            [sha512.Size]byte
}

type componentInventory struct {
	embedded []embeddedFile
	archive  []embeddedFile
	npm      []npmPackage
}

type spdxFile struct {
	Name          string         `json:"fileName"`
	ID            string         `json:"SPDXID"`
	License       string         `json:"licenseConcluded"`
	Copyright     string         `json:"copyrightText"`
	Types         []string       `json:"fileTypes,omitempty"`
	LicenseInFile []string       `json:"licenseInfoInFiles"`
	Checksums     []spdxChecksum `json:"checksums"`
}

type spdxChecksum struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"checksumValue"`
}

type spdxPackage struct {
	Name             string            `json:"name"`
	ID               string            `json:"SPDXID"`
	Version          string            `json:"versionInfo"`
	FileName         string            `json:"packageFileName,omitempty"`
	Download         string            `json:"downloadLocation"`
	LicenseConcluded string            `json:"licenseConcluded"`
	LicenseDeclared  string            `json:"licenseDeclared"`
	Copyright        string            `json:"copyrightText"`
	Comment          string            `json:"comment,omitempty"`
	Checksums        []spdxChecksum    `json:"checksums,omitempty"`
	External         []spdxExternalRef `json:"externalRefs"`
	FilesAnalyzed    bool              `json:"filesAnalyzed"`
}

type spdxExternalRef struct {
	Category string `json:"referenceCategory"`
	Type     string `json:"referenceType"`
	Locator  string `json:"referenceLocator"`
}

type spdxRelationship struct {
	From    string `json:"spdxElementId"`
	To      string `json:"relatedSpdxElement"`
	Type    string `json:"relationshipType"`
	Comment string `json:"comment,omitempty"`
}

func main() {
	os.Exit(realMain())
}

func realMain() int {
	opts := parseFlags()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, &opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func parseFlags() options {
	var opts options
	flag.StringVar(&opts.syftVersion, "syft-version", "", "required Syft version")
	flag.StringVar(&opts.dist, "dist", "dist", "release archive directory")
	flag.StringVar(&opts.repo, "repo", ".", "repository root")
	flag.StringVar(&opts.version, "version", "", "release version")
	flag.StringVar(&opts.commit, "commit", "", "release commit")
	flag.StringVar(&opts.goVersion, "go-version", "", "release Go version")
	flag.StringVar(&opts.namespaceBase, "namespace-base", "", "SPDX namespace base URL")
	flag.Int64Var(&opts.createdEpoch, "created-epoch", 0, "SPDX creation time as a Unix epoch")
	flag.Parse()
	return opts
}

func run(ctx context.Context, opts *options) error {
	if err := validateOptions(opts); err != nil {
		return err
	}
	if err := verifySyft(ctx, opts.syftVersion); err != nil {
		return err
	}
	if err := verifyReleaseMetadata(opts.dist, opts); err != nil {
		return err
	}
	if err := verifyArchiveSet(opts.dist); err != nil {
		return err
	}
	digests := make(map[string]string, len(releaseTargets))
	for index := range releaseTargets {
		target := &releaseTargets[index]
		digest, err := generateTarget(ctx, opts, target)
		if err != nil {
			return fmt.Errorf("%s: %w", target.name, err)
		}
		digests[target.archive] = digest
	}
	if err := verifySBOMSet(opts.dist); err != nil {
		return err
	}
	return writeChecksums(opts.dist, digests)
}

func validateOptions(opts *options) error {
	values := []string{
		opts.syftVersion, opts.dist, opts.repo, opts.version, opts.commit, opts.goVersion, opts.namespaceBase,
	}
	if slices.Contains(values, "") || opts.createdEpoch <= 0 {
		return errors.New("all release metadata flags are required")
	}
	if decoded, err := hex.DecodeString(opts.commit); err != nil || len(decoded) != 20 {
		return errors.New("commit must be a full object ID")
	}
	namespace, err := url.Parse(opts.namespaceBase)
	if err != nil || namespace.Scheme != "https" || namespace.Host == "" {
		return errors.New("namespace base must be an absolute HTTPS URL")
	}
	if _, err := time.Parse(time.RFC3339, time.Unix(opts.createdEpoch, 0).UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("invalid creation time: %w", err)
	}
	return nil
}

func verifySyft(ctx context.Context, want string) error {
	cmd := exec.CommandContext(ctx, syftExecutable, "version", "-o", "json")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("run Syft version: %w", err)
	}
	var version struct {
		Application string `json:"application"`
		Version     string `json:"version"`
	}
	if err := json.Unmarshal(output, &version); err != nil {
		return fmt.Errorf("decode Syft version: %w", err)
	}
	if version.Application != "syft" || "v"+version.Version != want {
		return fmt.Errorf("unexpected Syft version %q", version.Version)
	}
	return nil
}

// generateTarget certifies one release archive and writes its SBOM. The
// returned digest is the SHA-256 of the archive bytes the SBOM certifies;
// no later step rewrites an archive, so the digest stays valid for the
// checksum manifest.
func generateTarget(ctx context.Context, opts *options, target *releaseTarget) (string, error) {
	contents, err := readArchive(opts.dist, target, opts.createdEpoch)
	if err != nil {
		return "", err
	}
	inventory, err := loadComponentInventory(opts, target, contents)
	if err != nil {
		return "", err
	}
	archiveDigest, err := digestFile(filepath.Join(opts.dist, target.archive))
	if err != nil {
		return "", err
	}
	first, err := scanAndNormalize(ctx, opts, target, contents.binary, inventory, archiveDigest)
	if err != nil {
		return "", err
	}
	second, err := scanAndNormalize(ctx, opts, target, contents.binary, inventory, archiveDigest)
	if err != nil {
		return "", err
	}
	if !bytes.Equal(first, second) {
		return "", errors.New("normalized Syft output is not reproducible")
	}
	if writeErr := writeAtomic(filepath.Join(opts.dist, target.name+".spdx.json"), first); writeErr != nil {
		return "", writeErr
	}
	return archiveDigest, nil
}

func loadComponentInventory(
	opts *options,
	target *releaseTarget,
	contents *archiveContents,
) (*componentInventory, error) {
	embedded, err := loadEmbeddedFiles(opts.repo, target)
	if err != nil {
		return nil, err
	}
	if verifyErr := verifyEmbeddedBytes(contents.binary, embedded); verifyErr != nil {
		return nil, verifyErr
	}
	archived, err := loadArchiveFiles(opts.repo, contents)
	if err != nil {
		return nil, err
	}
	npm, err := loadNPMPackages(opts.repo)
	if err != nil {
		return nil, err
	}
	return &componentInventory{embedded: embedded, archive: archived, npm: npm}, nil
}

func scanAndNormalize(
	ctx context.Context,
	opts *options,
	target *releaseTarget,
	binary []byte,
	inventory *componentInventory,
	archiveDigest string,
) ([]byte, error) {
	raw, err := scanArchive(ctx, opts, target)
	if err != nil {
		return nil, err
	}
	normalized, err := normalizeSPDX(raw, opts, target, binary, inventory, archiveDigest)
	if err != nil {
		return nil, err
	}
	if err := verifySPDX(normalized, opts, target, binary, inventory, archiveDigest); err != nil {
		return nil, err
	}
	return normalized, nil
}

func scanArchive(ctx context.Context, opts *options, target *releaseTarget) (raw []byte, err error) {
	temp, err := os.MkdirTemp("", "prukka-sbom-*")
	if err != nil {
		return nil, fmt.Errorf("create Syft output directory: %w", err)
	}
	defer func() { err = errors.Join(err, os.RemoveAll(temp)) }()
	output := filepath.Join(temp, "sbom.json")
	archive, err := filepath.Abs(filepath.Join(opts.dist, target.archive))
	if err != nil {
		return nil, fmt.Errorf("resolve archive: %w", err)
	}
	cmd := exec.CommandContext(ctx, syftExecutable)
	cmd.Args = []string{
		syftExecutable, "scan", "file:" + archive, "--source-name", target.name,
		"--source-version", opts.version, "--output", "spdx-json=" + output, "--quiet",
	}
	cmd.Env = append(os.Environ(), "SYFT_CHECK_FOR_APP_UPDATE=false")
	var diagnostic bytes.Buffer
	cmd.Stdout = &diagnostic
	cmd.Stderr = &diagnostic
	runErr := cmd.Run()
	if runErr != nil {
		return nil, fmt.Errorf("scan archive with Syft: %w: %s", runErr, strings.TrimSpace(diagnostic.String()))
	}
	root, openErr := os.OpenRoot(temp)
	if openErr != nil {
		return nil, fmt.Errorf("open Syft output directory: %w", openErr)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	raw, err = root.ReadFile("sbom.json")
	if err != nil {
		return nil, fmt.Errorf("read Syft output: %w", err)
	}
	return raw, nil
}
