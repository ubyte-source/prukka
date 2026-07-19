package main

import (
	"bytes"
	"crypto/sha512"
	"encoding/json"
	"maps"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
)

func TestNormalizeSPDXIsDeterministicAndRepresentsEmbeds(t *testing.T) {
	t.Parallel()
	binary := []byte("prukka-runtime")
	embedded := []embeddedFile{
		{name: "internal/devices/assets/linux/src.tar.gz", data: []byte("driver")},
		{name: "internal/webui/dist/app.js", data: []byte("web")},
	}
	inventory := testComponentInventory(embedded)
	target := &releaseTargets[2]
	opts := testOptions()
	digest := strings.Repeat("a", 64)
	first, err := normalizeSPDX(testSPDX(t, binary, "first"), &opts, target, binary, &inventory, digest)
	if err != nil {
		t.Fatal(err)
	}
	second, err := normalizeSPDX(testSPDX(t, binary, "second"), &opts, target, binary, &inventory, digest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("normalized SPDX depends on volatile Syft metadata")
	}

	document := decodeTestSPDX(t, first)
	binaryID, err := binaryFileID(document.files, binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyEmbeddedSPDX(document.files, document.relationships, binaryID, embedded, target); err != nil {
		t.Fatal(err)
	}
	if err := verifyComponentSPDX(
		document.files, document.packages, document.relationships, &opts, target, &inventory,
	); err != nil {
		t.Fatal(err)
	}
	if err := verifyRelationshipEndpoints(document.files, document.packages, document.relationships); err != nil {
		t.Fatal(err)
	}
}

func TestComponentSPDXRejectsTampering(t *testing.T) {
	t.Parallel()
	binary := []byte("prukka-runtime")
	target := &releaseTargets[2]
	opts := testOptions()
	inventory := testComponentInventory([]embeddedFile{
		{name: "internal/devices/assets/linux/src.tar.gz", data: []byte("driver")},
		{name: "internal/webui/dist/app.js", data: []byte("web")},
	})
	data, err := normalizeSPDX(
		testSPDX(t, binary, "source"), &opts, target, binary, &inventory, strings.Repeat("a", 64),
	)
	if err != nil {
		t.Fatal(err)
	}
	document := decodeTestSPDX(t, data)

	for index := range document.packages {
		if document.packages[index].ID == dashboardSPDXPackage(opts.version).ID {
			document.packages[index].LicenseDeclared = "MIT"
			break
		}
	}
	if err := verifyComponentSPDX(
		document.files, document.packages, document.relationships, &opts, target, &inventory,
	); err == nil {
		t.Fatal("tampered component package accepted")
	}
}

// Golden identifiers for the fixed component fixture below. Each literal was
// computed outside the producer (sha256 over the ID inputs, NUL-terminated),
// so the expectation cannot follow a regression in the producer's scheme.
const (
	goldenDashboardID    = "SPDXRef-Package-Dashboard-a77619e9ce424671cca61a3e5d95302bb5f2c1a483bd76df63d7f689a6c249b5"
	goldenDriverID       = "SPDXRef-Package-Driver-3d200596e37a5df81e75d03ac3525e4fe91efbef812d04302c6e2e47d6ba1e6a"
	goldenNPMID          = "SPDXRef-Package-NPM-0d3d1159bd1f656080dd915aab87da2e8c4fc3a7479c72d48d65e5400421fce1"
	goldenRootLicenseID  = "SPDXRef-Archive-6db80c78cd42a7bc3ee34f936d15de82a664c706c0ca3a406237ed735659ad36"
	goldenLinuxLicenseID = "SPDXRef-Archive-2a80405a4ef12b5c2f14c9e6ace097cc40363f387f1b6fafabeb7273c439bf1a"
	goldenReadmeID       = "SPDXRef-Archive-786036777b90bca85beb523cadc94d9b2197bf45208344304080b45cb7365df2"
	goldenNoticeID       = "SPDXRef-Archive-403a235bef2efebe2dd9210b413665c41d6c45bc5a071d733eaaa4fa67f42357"
	goldenWebFileID      = "SPDXRef-Embedded-39f05d036fc0423f17dc2a2cc0dd2e91c34341175dcb9a1830c33cc0e4d48176"
	goldenDriverFileID   = "SPDXRef-Embedded-4fd4f3e2da4cc78b2f28adea9d806f6ecbe17b0f04fd5ec7d49045830000d8ab"
)

// TestMakeComponentSPDXGoldenContract pins the component contract with
// expectations spelled out independently of the producer, so a producer
// regression cannot be masked by verifiers that consult the same producer.
func TestMakeComponentSPDXGoldenContract(t *testing.T) {
	t.Parallel()
	opts := testOptions()
	inventory := testComponentInventory([]embeddedFile{
		{name: "internal/devices/assets/linux/src.tar.gz", data: []byte("driver")},
		{name: "internal/webui/dist/app.js", data: []byte("web")},
	})
	packages, relationships := makeComponentSPDX(&opts, &releaseTargets[2], &inventory)

	licenses := make(map[string]string, len(packages))
	for index := range packages {
		licenses[packages[index].ID] = packages[index].LicenseDeclared
	}
	wantLicenses := map[string]string{
		goldenDashboardID: "Apache-2.0",
		goldenDriverID:    "GPL-2.0-only",
		goldenNPMID:       "MIT",
	}
	if len(packages) != len(wantLicenses) || !maps.Equal(licenses, wantLicenses) {
		t.Fatalf("component packages are %v, want %v", licenses, wantLicenses)
	}

	want := []spdxRelationship{
		{From: "SPDXRef-DOCUMENT", To: goldenDashboardID, Type: "DESCRIBES"},
		{From: goldenDashboardID, To: goldenRootLicenseID, Type: "OTHER", Comment: "License evidence."},
		{From: goldenReadmeID, To: goldenDashboardID, Type: "DOCUMENTATION_OF"},
		{From: goldenDashboardID, To: goldenWebFileID, Type: "CONTAINS"},
		{From: "SPDXRef-DOCUMENT", To: goldenDriverID, Type: "DESCRIBES"},
		{From: goldenDriverID, To: goldenDriverFileID, Type: "CONTAINS"},
		{From: goldenDriverID, To: goldenLinuxLicenseID, Type: "OTHER", Comment: "License evidence."},
		{From: goldenNPMID, To: goldenDashboardID, Type: "BUILD_DEPENDENCY_OF"},
		{From: goldenNPMID, To: goldenNoticeID, Type: "OTHER", Comment: "Notice evidence."},
	}
	got := slices.Clone(relationships)
	slices.SortFunc(got, compareRelationships)
	slices.SortFunc(want, compareRelationships)
	if !slices.Equal(got, want) {
		t.Fatalf("component relationships are %v, want %v", got, want)
	}
}

type decodedTestSPDX struct {
	files         []spdxFile
	packages      []spdxPackage
	relationships []spdxRelationship
}

func decodeTestSPDX(t *testing.T, data []byte) decodedTestSPDX {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	var document decodedTestSPDX
	if err := decodeField(raw, "files", &document.files); err != nil {
		t.Fatal(err)
	}
	if err := decodeField(raw, "packages", &document.packages); err != nil {
		t.Fatal(err)
	}
	if err := decodeField(raw, "relationships", &document.relationships); err != nil {
		t.Fatal(err)
	}
	return document
}

func testComponentInventory(embedded []embeddedFile) componentInventory {
	archive := make([]embeddedFile, 0, len(archiveFiles))
	for _, name := range archiveFiles {
		archive = append(archive, embeddedFile{name: name, data: []byte(name)})
	}
	digest := sha512.Sum512([]byte("npm-package"))
	return componentInventory{
		embedded: embedded,
		archive:  archive,
		npm: []npmPackage{{
			path: "node_modules/example", name: "example", version: "1.2.3",
			resolved:  "https://registry.npmjs.org/example/-/example-1.2.3.tgz",
			integrity: "sha512-test", license: "MIT", sha512: digest,
		}},
	}
}

func TestGoInventoryContracts(t *testing.T) {
	t.Parallel()
	module := &debug.Module{Path: "example.com/module", Version: "v1.2.3"}
	inventory := map[string]spdxPackage{
		"example.com/module\x00v1.2.3": {
			Name: "example.com/module", Version: "v1.2.3",
			External: []spdxExternalRef{{
				Category: packageManager, Type: "purl", Locator: "pkg:golang/example.com/module@v1.2.3",
			}},
		},
	}
	if err := verifyModulePackage(inventory, module); err != nil {
		t.Fatal(err)
	}
	settings := []debug.BuildSetting{
		{Key: "GOOS", Value: "linux"}, {Key: "GOARCH", Value: "amd64"},
		{Key: "-trimpath", Value: "true"}, {Key: "-tags", Value: "bundleddrivers"},
		{Key: "vcs.revision", Value: strings.Repeat("a", 40)},
	}
	if err := verifyBuildSettings(settings, strings.Repeat("a", 40), &releaseTargets[2]); err != nil {
		t.Fatal(err)
	}
}
