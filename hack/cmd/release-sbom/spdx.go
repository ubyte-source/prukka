package main

import (
	"bytes"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"runtime/debug"
	"slices"
	"strings"
	"time"
)

// normalizeSPDX rewrites one Syft document into the bytes the release
// publishes. Every array it touches is re-sorted by SPDX identifier, and the
// top-level object relies on encoding/json emitting map keys in sorted order,
// so the two scans generateTarget runs can be compared byte for byte.
func normalizeSPDX(
	raw []byte,
	opts *options,
	target *releaseTarget,
	binary []byte,
	inventory *componentInventory,
	archiveDigest string,
) ([]byte, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("decode SPDX: %w", err)
	}
	if err := normalizeMetadata(document, opts, target, archiveDigest); err != nil {
		return nil, err
	}
	var files []spdxFile
	if err := decodeField(document, "files", &files); err != nil {
		return nil, err
	}
	binaryID, err := binaryFileID(files, binary)
	if err != nil {
		return nil, err
	}
	relationships := make([]spdxRelationship, 0, len(inventory.embedded)+len(inventory.archive)+len(inventory.npm)*2)
	if decodeErr := decodeField(document, "relationships", &relationships); decodeErr != nil {
		return nil, decodeErr
	}
	files, relationships = appendEmbeddedSPDX(files, relationships, inventory.embedded, binaryID)
	files, relationships = appendArchiveSPDX(files, relationships, inventory.archive)
	components, componentRelationships := makeComponentSPDX(opts, target, inventory)
	relationships = append(relationships, componentRelationships...)
	if appendErr := appendPackages(document, components); appendErr != nil {
		return nil, appendErr
	}
	slices.SortFunc(files, func(a, b spdxFile) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(relationships, compareRelationships)
	if encodeErr := setJSON(document, "files", files); encodeErr != nil {
		return nil, encodeErr
	}
	if encodeErr := setJSON(document, "relationships", relationships); encodeErr != nil {
		return nil, encodeErr
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode normalized SPDX: %w", err)
	}

	return append(data, '\n'), nil
}

// appendArchiveSPDX attaches the repository files that ship beside the binary.
// They are described by the document rather than contained by the binary,
// which is the distinction DESCRIBES and CONTAINS draw in SPDX 2.3.
func appendArchiveSPDX(
	files []spdxFile,
	relationships []spdxRelationship,
	archive []embeddedFile,
) ([]spdxFile, []spdxRelationship) {
	for _, file := range archive {
		entry := makeArchiveSPDXFile(file)
		files = append(files, entry)
		relationships = append(relationships, spdxRelationship{
			From: spdxDocumentID,
			To:   entry.ID,
			Type: relationDescribes,
		})
	}

	return files, relationships
}

// makeComponentSPDX declares what a file scanner cannot see: the dashboard
// bundle, one package per bundled device driver, and the npm dependencies the
// dashboard was built from. Each is tied to the LICENSE or NOTICE file that
// carries its text by an OTHER edge, which SPDX 2.3 reserves for relationships
// it does not define and asks to be named in the edge's comment.
func makeComponentSPDX(
	opts *options,
	target *releaseTarget,
	inventory *componentInventory,
) ([]spdxPackage, []spdxRelationship) {
	dashboard := dashboardSPDXPackage(opts.version)
	packages := []spdxPackage{dashboard}
	relationships := []spdxRelationship{
		{From: spdxDocumentID, To: dashboard.ID, Type: relationDescribes},
		{From: dashboard.ID, To: archiveSPDXFileID(rootLicenseFile), Type: relationOther, Comment: licenseEvidence},
		{From: archiveSPDXFileID("README.md"), To: dashboard.ID, Type: "DOCUMENTATION_OF"},
	}
	for _, file := range inventory.embedded {
		fileID := embeddedSPDXFileID(file)
		if strings.HasPrefix(file.name, "internal/webui/dist/") {
			relationships = append(relationships, spdxRelationship{
				From: dashboard.ID,
				To:   fileID,
				Type: relationContains,
			})

			continue
		}
		if !strings.HasPrefix(file.name, "internal/devices/assets/") {
			continue
		}
		pkg := driverSPDXPackage(opts.version, target, file)
		packages = append(packages, pkg)
		licenseFile := rootLicenseFile
		if pkg.LicenseDeclared == licenseGPL2 {
			licenseFile = linuxLicenseFile
		}
		relationships = append(relationships,
			spdxRelationship{From: spdxDocumentID, To: pkg.ID, Type: relationDescribes},
			spdxRelationship{From: pkg.ID, To: fileID, Type: relationContains},
			spdxRelationship{
				From:    pkg.ID,
				To:      archiveSPDXFileID(licenseFile),
				Type:    relationOther,
				Comment: licenseEvidence,
			},
		)
	}
	for index := range inventory.npm {
		pkg := npmSPDXPackage(&inventory.npm[index])
		packages = append(packages, pkg)
		relationships = append(relationships,
			spdxRelationship{From: pkg.ID, To: dashboard.ID, Type: "BUILD_DEPENDENCY_OF"},
			spdxRelationship{
				From:    pkg.ID,
				To:      archiveSPDXFileID("NOTICE.txt"),
				Type:    relationOther,
				Comment: "Notice evidence.",
			},
		)
	}

	return packages, relationships
}

// appendPackages keeps Syft's own entries as raw JSON: spdxPackage models only
// the fields this program writes, so decoding and re-encoding them would drop
// everything else the scanner found.
func appendPackages(document map[string]json.RawMessage, additions []spdxPackage) error {
	var packages []json.RawMessage
	if err := decodeField(document, "packages", &packages); err != nil {
		return err
	}
	for index := range additions {
		encoded, err := json.Marshal(&additions[index])
		if err != nil {
			return fmt.Errorf("encode SPDX package %s: %w", additions[index].Name, err)
		}
		packages = append(packages, encoded)
	}
	var sortErr error
	slices.SortStableFunc(packages, func(a, b json.RawMessage) int {
		aID, err := rawPackageID(a)
		if err != nil {
			sortErr = errors.Join(sortErr, err)
		}
		bID, err := rawPackageID(b)
		if err != nil {
			sortErr = errors.Join(sortErr, err)
		}

		return strings.Compare(aID, bID)
	})
	if sortErr != nil {
		return sortErr
	}

	return setJSON(document, "packages", packages)
}

// rawPackageID reads the sort key without modeling the rest of the object.
// SPDXID is mandatory on an SPDX package, so an absent one is a malformed
// document rather than a value to default.
func rawPackageID(data json.RawMessage) (string, error) {
	var identity struct {
		ID string `json:"SPDXID"`
	}
	if err := json.Unmarshal(data, &identity); err != nil {
		return "", fmt.Errorf("decode SPDX package identity: %w", err)
	}
	if identity.ID == "" {
		return "", errors.New("SPDX package lacks an identifier")
	}

	return identity.ID, nil
}

// normalizeMetadata replaces the three fields that must not depend on the
// machine that scanned: the creation time comes from -created-epoch, the
// document name from the target, and the namespace from the archive digest.
func normalizeMetadata(
	document map[string]json.RawMessage,
	opts *options,
	target *releaseTarget,
	archiveDigest string,
) error {
	if err := setCreationInfo(document, opts.createdEpoch); err != nil {
		return err
	}
	if err := setJSON(document, "name", target.name); err != nil {
		return err
	}

	return setJSON(document, "documentNamespace", spdxNamespace(opts, target, archiveDigest))
}

// appendEmbeddedSPDX hangs the go:embed assets off the runtime binary's own
// file entry rather than off the document, because they are bytes inside that
// file and CONTAINS is what SPDX 2.3 says about bytes inside a file.
func appendEmbeddedSPDX(
	files []spdxFile,
	relationships []spdxRelationship,
	embedded []embeddedFile,
	binaryID string,
) ([]spdxFile, []spdxRelationship) {
	for _, file := range embedded {
		entry := makeEmbeddedSPDXFile(file)
		files = append(files, entry)
		relationships = append(relationships, spdxRelationship{From: binaryID, To: entry.ID, Type: relationContains})
	}

	return files, relationships
}

// setCreationInfo writes the timestamp in the one shape SPDX 2.3 accepts:
// YYYY-MM-DDThh:mm:ssZ, which is RFC 3339 on a UTC instant with no fraction.
func setCreationInfo(document map[string]json.RawMessage, epoch int64) error {
	var creation map[string]any
	if err := decodeField(document, "creationInfo", &creation); err != nil {
		return err
	}
	creation["created"] = time.Unix(epoch, 0).UTC().Format(time.RFC3339)

	return setJSON(document, "creationInfo", creation)
}

func setJSON(document map[string]json.RawMessage, key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode SPDX field %q: %w", key, err)
	}
	document[key] = encoded

	return nil
}

// decodeField treats a missing field as an error rather than a zero value: an
// absent array here means the scan produced a shape this program was not
// written against.
func decodeField(document map[string]json.RawMessage, key string, dst any) error {
	raw, ok := document[key]
	if !ok {
		return fmt.Errorf("SPDX field %q is missing", key)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("decode SPDX field %q: %w", key, err)
	}

	return nil
}

// makeEmbeddedSPDXFile spells every license and copyright field NOASSERTION,
// the SPDX token for a claim not made: these payloads are bytes lifted out of
// the repository, and stating a license per file would be inventing one.
func makeEmbeddedSPDXFile(file embeddedFile) spdxFile {
	sha256Sum := sha256.Sum256(file.data)

	return spdxFile{
		Name: "embedded/" + file.name,
		ID:   embeddedSPDXFileID(file),
		Checksums: []spdxChecksum{
			{Algorithm: checksumSHA256, Value: hex.EncodeToString(sha256Sum[:])},
		},
		License:       noAssertion,
		LicenseInFile: []string{noAssertion},
		Copyright:     noAssertion,
	}
}

func embeddedSPDXFileID(file embeddedFile) string {
	return spdxID("Embedded", file.name, hex.EncodeToString(sha256Digest(file.data)))
}

func makeArchiveSPDXFile(file embeddedFile) spdxFile {
	license, inFile := archiveFileLicense(file.name)

	return spdxFile{
		Name: "archive/" + file.name,
		ID:   archiveSPDXFileID(file.name),
		Checksums: []spdxChecksum{
			{Algorithm: checksumSHA256, Value: hex.EncodeToString(sha256Digest(file.data))},
		},
		License:       license,
		LicenseInFile: []string{inFile},
		Copyright:     noAssertion,
	}
}

// archiveFileLicense keeps the two SPDX license fields apart:
// licenseInfoInFiles records what the file itself says, so it stays
// NOASSERTION for a file that carries no notice, while licenseConcluded
// records the project's judgment about it.
func archiveFileLicense(name string) (concluded, inFile string) {
	switch name {
	case rootLicenseFile:
		return licenseGPL3, licenseGPL3
	case linuxLicenseFile:
		return licenseGPL2, licenseGPL2
	case "README.md", "deploy/uninstall.ps1", "deploy/uninstall.sh":
		return licenseGPL3, noAssertion
	default:
		return noAssertion, noAssertion
	}
}

func archiveSPDXFileID(name string) string {
	return spdxID("Archive", name)
}

// dashboardSPDXPackage sets filesAnalyzed false, which is how SPDX 2.3 says a
// package's file list is not enumerated on the package itself; the bundle's
// files appear once, as the runtime binary's embedded contents.
func dashboardSPDXPackage(version string) spdxPackage {
	return spdxPackage{
		Name:             dashboardPackage,
		ID:               spdxID("Package-Dashboard", projectName),
		Version:          version,
		Download:         noAssertion,
		FilesAnalyzed:    false,
		LicenseConcluded: licenseGPL3,
		LicenseDeclared:  licenseGPL3,
		Copyright:        noAssertion,
		External: []spdxExternalRef{{
			Category: packageManager,
			Type:     externalPURL,
			Locator:  "pkg:generic/prukka-dashboard@" + version,
		}},
	}
}

// driverSPDXPackage declares GPL-2.0-only for the Linux payload and
// GPL-3.0-or-later for every other target, which is why makeComponentSPDX
// points a GPL-2.0 package at drivers/linux/LICENSE and not at the root one.
func driverSPDXPackage(version string, target *releaseTarget, file embeddedFile) spdxPackage {
	name := strings.TrimPrefix(file.name, "internal/devices/assets/")
	license := licenseGPL3
	if strings.HasPrefix(name, "linux/") {
		license = licenseGPL2
	}

	return spdxPackage{
		Name:             "prukka-driver-" + strings.NewReplacer("/", "-", ".tar.gz", "").Replace(name),
		ID:               spdxID("Package-Driver", target.goos, target.goarch, name),
		Version:          version,
		FileName:         "embedded/" + file.name,
		Download:         noAssertion,
		FilesAnalyzed:    false,
		LicenseConcluded: license,
		LicenseDeclared:  license,
		Copyright:        noAssertion,
		Checksums:        []spdxChecksum{{Algorithm: checksumSHA256, Value: hex.EncodeToString(sha256Digest(file.data))}},
		External: []spdxExternalRef{{
			Category: packageManager,
			Type:     externalPURL,
			Locator: "pkg:generic/prukka-driver-" + target.goos + "@" + version +
				"?arch=" + target.goarch + "&payload=" + url.QueryEscape(name),
		}},
	}
}

// npmSPDXPackage parks the lockfile path and the integrity string in the
// package comment: SPDX has no field for either, and they are what a reader
// needs to find the entry this package was derived from.
func npmSPDXPackage(input *npmPackage) spdxPackage {
	return spdxPackage{
		Name:             input.name,
		ID:               npmPackageID(input),
		Version:          input.version,
		Download:         input.resolved,
		FilesAnalyzed:    false,
		LicenseConcluded: noAssertion,
		LicenseDeclared:  input.license,
		Copyright:        noAssertion,
		Checksums:        []spdxChecksum{{Algorithm: "SHA512", Value: hex.EncodeToString(input.sha512[:])}},
		External:         []spdxExternalRef{{Category: packageManager, Type: externalPURL, Locator: npmPURL(input)}},
		Comment:          "package-lock path: " + input.path + "\nintegrity: " + input.integrity,
	}
}

// npmPackageID folds in the install path and the integrity string, so one name
// and version installed twice under different paths stays two SPDX packages.
func npmPackageID(input *npmPackage) string {
	return spdxID("Package-NPM", input.path, input.name, input.version, input.integrity)
}

// npmPURL encodes a scope's "@" as %40 and escapes the two segments apart,
// because the purl syntax gives "/" its own meaning as the separator between
// namespace and name.
func npmPURL(input *npmPackage) string {
	name := input.name
	if strings.HasPrefix(name, "@") {
		scope, unscoped, ok := strings.Cut(name[1:], "/")
		if ok {
			name = "%40" + url.PathEscape(scope) + "/" + url.PathEscape(unscoped)
		}
	} else {
		name = url.PathEscape(name)
	}

	return "pkg:npm/" + name + "@" + url.PathEscape(input.version)
}

func sha256Digest(data []byte) []byte {
	digest := sha256.Sum256(data)

	return digest[:]
}

// spdxID hashes its inputs because an SPDXID is constrained to
// SPDXRef-[0-9a-zA-Z.-]+, an alphabet that excludes the slashes and
// underscores in the paths and package names being identified. The NUL
// between values keeps two different splittings from colliding.
func spdxID(prefix string, values ...string) string {
	digest := sha256.New()
	for _, value := range values {
		digest.Write([]byte(value))
		digest.Write([]byte{0})
	}

	return "SPDXRef-" + prefix + "-" + hex.EncodeToString(digest.Sum(nil))
}

// binaryFileID locates Syft's entry for the runtime by digest rather than by
// name, so the SPDX file the embedded assets hang from is provably the binary
// whose bytes verifyEmbeddedBytes searched.
func binaryFileID(files []spdxFile, binary []byte) (string, error) {
	want := sha256.Sum256(binary)
	wantHex := hex.EncodeToString(want[:])
	for index := range files {
		if checksumValue(files[index].Checksums, checksumSHA256) == wantHex {
			return files[index].ID, nil
		}
	}

	return "", errors.New("runtime binary digest is absent from SPDX files")
}

// compareRelationships orders on all four fields, the same four
// verifyRelationshipEndpoints rejects a repeat on, so the sort is total over
// the edges the document may legitimately hold.
func compareRelationships(a, b spdxRelationship) int {
	for _, values := range [][2]string{{a.From, b.From}, {a.Type, b.Type}, {a.To, b.To}, {a.Comment, b.Comment}} {
		if comparison := strings.Compare(values[0], values[1]); comparison != 0 {
			return comparison
		}
	}

	return 0
}

// verifySPDX reads the finished bytes back and re-derives every claim from the
// inputs, so a mistake in normalization fails the release rather than shipping
// a document that certifies the wrong bytes.
func verifySPDX(
	data []byte,
	opts *options,
	target *releaseTarget,
	binary []byte,
	inventory *componentInventory,
	archiveDigest string,
) error {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("decode normalized SPDX: %w", err)
	}
	if err := verifyDocumentMetadata(document, opts, target, archiveDigest); err != nil {
		return err
	}
	var files []spdxFile
	if err := decodeField(document, "files", &files); err != nil {
		return err
	}
	binaryID, err := binaryFileID(files, binary)
	if err != nil {
		return err
	}
	var relationships []spdxRelationship
	if err := decodeField(document, "relationships", &relationships); err != nil {
		return err
	}
	if err := verifyEmbeddedSPDX(files, relationships, binaryID, inventory.embedded, target); err != nil {
		return err
	}
	var packages []spdxPackage
	if err := decodeField(document, "packages", &packages); err != nil {
		return err
	}
	if err := verifyComponentSPDX(files, packages, relationships, opts, target, inventory); err != nil {
		return err
	}
	if err := verifyRelationshipEndpoints(files, packages, relationships); err != nil {
		return err
	}

	return verifyGoInventory(binary, packages, opts, target)
}

// verifyDocumentMetadata pins the header fields the specification fixes rather
// than leaves to the producer: CC0-1.0 is the only dataLicense SPDX 2.3
// permits, and SPDXRef-DOCUMENT is the identifier it gives the document
// element itself.
func verifyDocumentMetadata(
	document map[string]json.RawMessage,
	opts *options,
	target *releaseTarget,
	archiveDigest string,
) error {
	wants := map[string]string{
		"spdxVersion": "SPDX-2.3", "dataLicense": "CC0-1.0",
		"SPDXID": spdxDocumentID, "name": target.name,
	}
	for key, want := range wants {
		var got string
		if err := decodeField(document, key, &got); err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("SPDX %s is %q, want %q", key, got, want)
		}
	}
	var creation struct {
		Created  string   `json:"created"`
		Creators []string `json:"creators"`
	}
	if err := decodeField(document, "creationInfo", &creation); err != nil {
		return err
	}
	if creation.Created != time.Unix(opts.createdEpoch, 0).UTC().Format(time.RFC3339) || len(creation.Creators) == 0 {
		return errors.New("SPDX creation metadata is incomplete")
	}
	var namespace string
	if err := decodeField(document, "documentNamespace", &namespace); err != nil {
		return err
	}
	if namespace != spdxNamespace(opts, target, archiveDigest) {
		return errors.New("SPDX document namespace does not identify its release archive")
	}

	return nil
}

// spdxNamespace binds the document to the exact archive it describes. SPDX 2.3
// requires the namespace to be unique per document, and the digest is what
// makes a rebuild with different bytes a different document rather than a
// second copy of this one.
func spdxNamespace(opts *options, target *releaseTarget, archiveDigest string) string {
	return strings.TrimRight(opts.namespaceBase, "/") + "/sbom/" + url.PathEscape(target.name) + "/sha256/" + archiveDigest
}

// verifyEmbeddedSPDX fails on an unexpected embedded/ entry as well as a
// missing one: an SBOM listing an asset the build did not embed is wrong in
// the same way as one omitting an asset it did.
func verifyEmbeddedSPDX(
	files []spdxFile,
	relationships []spdxRelationship,
	binaryID string,
	embedded []embeddedFile,
	target *releaseTarget,
) error {
	expected := make(map[string]embeddedFile, len(embedded))
	for _, file := range embedded {
		expected["embedded/"+file.name] = file
	}
	seenDrivers := make(map[string]struct{})
	for index := range files {
		file := &files[index]
		input, ok := expected[file.Name]
		if !ok {
			if strings.HasPrefix(file.Name, "embedded/") {
				return fmt.Errorf("unexpected embedded file in SPDX: %s", file.Name)
			}

			continue
		}
		if err := verifyEmbeddedFile(file, input, binaryID, relationships); err != nil {
			return err
		}
		if strings.HasPrefix(input.name, "internal/devices/assets/") {
			seenDrivers[input.name] = struct{}{}
		}
		delete(expected, file.Name)
	}
	if len(expected) != 0 || len(seenDrivers) != len(target.drivers) {
		return errors.New("SPDX embedded inventory is incomplete")
	}

	return nil
}

func verifyEmbeddedFile(file *spdxFile, input embeddedFile, binaryID string, relationships []spdxRelationship) error {
	want := makeEmbeddedSPDXFile(input)
	if !reflect.DeepEqual(file, &want) {
		return fmt.Errorf("embedded file metadata mismatch in SPDX: %s", input.name)
	}

	return requireRelationship(relationships, binaryID, file.ID, relationContains, "")
}

// verifyComponentSPDX checks the document against the same producer that
// normalization uses, so the expectation list cannot drift from the output.
func verifyComponentSPDX(
	files []spdxFile,
	packages []spdxPackage,
	relationships []spdxRelationship,
	opts *options,
	target *releaseTarget,
	inventory *componentInventory,
) error {
	fileByName, err := indexSPDXFiles(files)
	if err != nil {
		return err
	}
	packageByID, err := indexSPDXPackages(packages)
	if err != nil {
		return err
	}
	if err := verifyArchiveSPDX(fileByName, relationships, inventory.archive); err != nil {
		return err
	}
	wantPackages, wantRelationships := makeComponentSPDX(opts, target, inventory)
	for index := range wantPackages {
		if err := verifyExactPackage(packageByID, &wantPackages[index]); err != nil {
			return err
		}
	}
	for index := range wantRelationships {
		want := &wantRelationships[index]
		if err := requireRelationship(relationships, want.From, want.To, want.Type, want.Comment); err != nil {
			return err
		}
	}

	return verifyNoUnexpectedComponents(packages, wantPackages)
}

// verifyNoUnexpectedComponents closes the golden check in the other direction:
// verifyComponentSPDX proves every expected package is present, and this proves
// no additional one wearing a component prefix was added.
func verifyNoUnexpectedComponents(packages, expected []spdxPackage) error {
	componentIDs := make(map[string]struct{}, len(expected))
	for index := range expected {
		componentIDs[expected[index].ID] = struct{}{}
	}
	for index := range packages {
		id := packages[index].ID
		if !isComponentPackageID(id) {
			continue
		}
		if _, ok := componentIDs[id]; !ok {
			return fmt.Errorf("unexpected component package in SPDX: %s", id)
		}
	}

	return nil
}

// isComponentPackageID separates this program's packages from Syft's by the
// prefix spdxID gave them, so the exactness check binds only what
// makeComponentSPDX produced and leaves the scanner's own findings alone.
func isComponentPackageID(id string) bool {
	return strings.HasPrefix(id, "SPDXRef-Package-Dashboard-") ||
		strings.HasPrefix(id, "SPDXRef-Package-Driver-") ||
		strings.HasPrefix(id, "SPDXRef-Package-NPM-")
}

// indexSPDXFiles rejects a repeated file name or identifier. An SPDXID must be
// unique within its document, and a repeated name would leave the archive/ and
// embedded/ lookups picking an arbitrary one of two entries.
func indexSPDXFiles(files []spdxFile) (map[string]spdxFile, error) {
	byName := make(map[string]spdxFile, len(files))
	ids := make(map[string]struct{}, len(files))
	for index := range files {
		file := &files[index]
		if file.Name == "" || file.ID == "" {
			return nil, errors.New("SPDX file identity is incomplete")
		}
		if _, exists := byName[file.Name]; exists {
			return nil, fmt.Errorf("duplicate SPDX file name: %s", file.Name)
		}
		if _, exists := ids[file.ID]; exists {
			return nil, fmt.Errorf("duplicate SPDX file identifier: %s", file.ID)
		}
		byName[file.Name] = *file
		ids[file.ID] = struct{}{}
	}

	return byName, nil
}

func indexSPDXPackages(packages []spdxPackage) (map[string]spdxPackage, error) {
	byID := make(map[string]spdxPackage, len(packages))
	for index := range packages {
		pkg := &packages[index]
		if pkg.Name == "" || pkg.ID == "" {
			return nil, errors.New("SPDX package identity is incomplete")
		}
		if _, exists := byID[pkg.ID]; exists {
			return nil, fmt.Errorf("duplicate SPDX package identifier: %s", pkg.ID)
		}
		byID[pkg.ID] = *pkg
	}

	return byID, nil
}

func verifyArchiveSPDX(
	fileByName map[string]spdxFile,
	relationships []spdxRelationship,
	archive []embeddedFile,
) error {
	expected := make(map[string]struct{}, len(archive))
	for _, input := range archive {
		name := "archive/" + input.name
		expected[name] = struct{}{}
		file, ok := fileByName[name]
		if !ok {
			return fmt.Errorf("archived file absent from SPDX: %s", input.name)
		}
		want := makeArchiveSPDXFile(input)
		if !reflect.DeepEqual(&file, &want) {
			return fmt.Errorf("archived file metadata mismatch in SPDX: %s", input.name)
		}
		if err := requireRelationship(
			relationships, spdxDocumentID, file.ID, relationDescribes, "",
		); err != nil {
			return err
		}
	}
	if len(expected) != len(archiveFiles) {
		return errors.New("archived SPDX inventory is incomplete")
	}
	for name := range fileByName {
		if strings.HasPrefix(name, "archive/") {
			if _, ok := expected[name]; !ok {
				return fmt.Errorf("unexpected archived file in SPDX: %s", name)
			}
		}
	}

	return nil
}

func verifyExactPackage(packages map[string]spdxPackage, want *spdxPackage) error {
	got, ok := packages[want.ID]
	if !ok {
		return fmt.Errorf("component package absent from SPDX: %s", want.Name)
	}
	if !reflect.DeepEqual(&got, want) {
		return fmt.Errorf("component package metadata mismatch in SPDX: %s", want.Name)
	}

	return nil
}

// requireRelationship counts the matching edges instead of finding one,
// because a duplicate is a defect in its own right: a consumer that
// deduplicates and one that does not would read two different graphs.
func requireRelationship(
	relationships []spdxRelationship,
	from, to, relationshipType, comment string,
) error {
	count := 0
	for _, relationship := range relationships {
		if relationship.From == from && relationship.To == to && relationship.Type == relationshipType &&
			relationship.Comment == comment {
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("SPDX relationship count is %d, want 1: %s %s %s", count, from, relationshipType, to)
	}

	return nil
}

// verifyRelationshipEndpoints rejects an edge that names an identifier no
// element declares, and a second edge with the same four fields. The SPDX JSON
// schema checks neither, so this is where the graph is closed.
func verifyRelationshipEndpoints(
	files []spdxFile,
	packages []spdxPackage,
	relationships []spdxRelationship,
) error {
	identifiers := map[string]struct{}{spdxDocumentID: {}}
	for index := range files {
		file := &files[index]
		if _, exists := identifiers[file.ID]; exists {
			return fmt.Errorf("duplicate SPDX element identifier: %s", file.ID)
		}
		identifiers[file.ID] = struct{}{}
	}
	for index := range packages {
		pkg := &packages[index]
		if _, exists := identifiers[pkg.ID]; exists {
			return fmt.Errorf("duplicate SPDX element identifier: %s", pkg.ID)
		}
		identifiers[pkg.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(relationships))
	for _, relationship := range relationships {
		if _, ok := identifiers[relationship.From]; !ok {
			return fmt.Errorf("SPDX relationship source is absent: %s", relationship.From)
		}
		if _, ok := identifiers[relationship.To]; !ok {
			return fmt.Errorf("SPDX relationship target is absent: %s", relationship.To)
		}
		key := relationship.From + "\x00" + relationship.Type + "\x00" + relationship.To + "\x00" + relationship.Comment
		if _, exists := seen[key]; exists {
			return errors.New("duplicate SPDX relationship")
		}
		seen[key] = struct{}{}
	}

	return nil
}

func checksumValue(checksums []spdxChecksum, algorithm string) string {
	for _, checksum := range checksums {
		if checksum.Algorithm == algorithm {
			return checksum.Value
		}
	}

	return ""
}

// verifyGoInventory reads the module list out of the shipped binary and
// requires an SPDX package for each. Both sides derive from the same embedded
// build info, so a module missing here means the scan and the binary disagree
// about what was linked.
func verifyGoInventory(binary []byte, packages []spdxPackage, opts *options, target *releaseTarget) error {
	info, err := buildinfo.Read(bytes.NewReader(binary))
	if err != nil {
		return fmt.Errorf("read runtime Go build info: %w", err)
	}
	if info.Path != mainPackage || info.Main.Path != mainModule || info.GoVersion != "go"+opts.goVersion {
		return fmt.Errorf("unexpected runtime Go identity: %s %s %s", info.Path, info.Main.Path, info.GoVersion)
	}
	if err := verifyBuildSettings(info.Settings, opts.commit, target); err != nil {
		return err
	}
	inventory := make(map[string]spdxPackage, len(packages))
	for index := range packages {
		pkg := &packages[index]
		inventory[pkg.Name+"\x00"+pkg.Version] = *pkg
	}
	modules := append([]*debug.Module{&info.Main}, info.Deps...)
	for _, module := range modules {
		if err := verifyModulePackage(inventory, module); err != nil {
			return err
		}
	}
	if _, ok := inventory["stdlib\x00"+info.GoVersion]; !ok {
		return errors.New("go standard library is absent from SPDX packages")
	}

	return nil
}

// verifyBuildSettings requires -trimpath and the bundleddrivers tag, the two
// build inputs the document's claims rest on: without trimpath the binary
// carries the builder's absolute paths, and without the tag it carries no
// drivers for makeComponentSPDX to declare.
func verifyBuildSettings(settings []debug.BuildSetting, commit string, target *releaseTarget) error {
	values := make(map[string]string, len(settings))
	for _, setting := range settings {
		values[setting.Key] = setting.Value
	}
	wants := map[string]string{"GOOS": target.goos, "GOARCH": target.goarch, "-trimpath": "true", "vcs.revision": commit}
	for key, want := range wants {
		if values[key] != want {
			return fmt.Errorf("go build setting %s is %q, want %q", key, values[key], want)
		}
	}
	if !strings.Contains(values["-tags"], "bundleddrivers") {
		return errors.New("release binary was not built with bundleddrivers")
	}

	return nil
}

// verifyModulePackage follows a replace directive to the module that actually
// built, since the original path names code the shipped binary does not
// contain.
func verifyModulePackage(inventory map[string]spdxPackage, module *debug.Module) error {
	want := module
	if module.Replace != nil {
		want = module.Replace
	}
	pkg, ok := inventory[want.Path+"\x00"+want.Version]
	if !ok {
		return fmt.Errorf("go module absent from SPDX packages: %s@%s", want.Path, want.Version)
	}
	for _, external := range pkg.External {
		isGoPURL := external.Category == packageManager && external.Type == externalPURL &&
			strings.HasPrefix(external.Locator, "pkg:golang/")
		if isGoPURL {
			return nil
		}
	}

	return fmt.Errorf("go module lacks an SPDX package URL: %s@%s", want.Path, want.Version)
}
