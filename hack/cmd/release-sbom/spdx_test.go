package main

import (
	"bytes"
	"crypto/sha512"
	"encoding/json"
	"runtime/debug"
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
