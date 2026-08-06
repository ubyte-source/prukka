package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// releaseMetadata is GoReleaser's metadata.json.
type releaseMetadata struct {
	Project string `json:"project_name"`
	Tag     string `json:"tag"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

var archiveFiles = []string{
	rootLicenseFile,
	"README.md",
	"NOTICE.txt",
	"deploy/uninstall.ps1",
	"deploy/uninstall.sh",
	linuxLicenseFile,
}

// packageLockEntry is one dependency as npm records it.
type packageLockEntry struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Resolved  string `json:"resolved"`
	Integrity string `json:"integrity"`
	License   string `json:"license"`
	Optional  bool   `json:"optional"`
}

// packageLock is the dashboard's package-lock.json.
type packageLock struct {
	Packages        map[string]packageLockEntry `json:"packages"`
	Name            string                      `json:"name"`
	LockfileVersion int                         `json:"lockfileVersion"`
}

// verifyReleaseMetadata compares the whole struct rather than field by field,
// so a field added to releaseMetadata is checked from the moment it is added.
func verifyReleaseMetadata(dist string, opts *options) (err error) {
	root, err := os.OpenRoot(dist)
	if err != nil {
		return fmt.Errorf("open release directory: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()

	data, err := root.ReadFile("metadata.json")
	if err != nil {
		return fmt.Errorf("read GoReleaser metadata: %w", err)
	}
	var metadata releaseMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return fmt.Errorf("decode GoReleaser metadata: %w", err)
	}
	want := releaseMetadata{Project: projectName, Tag: opts.version, Version: opts.version, Commit: opts.commit}
	if metadata != want {
		return fmt.Errorf("unexpected GoReleaser metadata: got %+v, want %+v", metadata, want)
	}

	return nil
}

// archiveContents is the single-pass inventory of one release archive.
type archiveContents struct {
	files      map[string][]byte
	members    map[string]struct{}
	binaryName string
	binary     []byte
}

// readArchive reads the archive once. A tar member's bytes are only readable
// while the reader sits on it, so the members the SBOM needs are captured
// during the walk rather than by reopening the archive per member.
func readArchive(dist string, target *releaseTarget, epoch int64) (contents *archiveContents, err error) {
	root, err := os.OpenRoot(dist)
	if err != nil {
		return nil, fmt.Errorf("open release directory: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	file, err := root.Open(target.archive)
	if err != nil {
		return nil, fmt.Errorf("open release archive: %w", err)
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	contents = &archiveContents{
		files:      make(map[string][]byte, len(archiveFiles)),
		members:    make(map[string]struct{}, len(archiveFiles)+1),
		binaryName: binaryName(target.goos),
	}
	if strings.HasSuffix(target.archive, ".zip") {
		info, statErr := file.Stat()
		if statErr != nil {
			return nil, fmt.Errorf("stat release archive: %w", statErr)
		}
		err = readZIPArchive(file, info.Size(), epoch, contents)
	} else {
		err = readTarArchive(file, epoch, contents)
	}
	if err != nil {
		return nil, err
	}
	if err := contents.verifyMemberSet(); err != nil {
		return nil, err
	}

	return contents, nil
}

// readTarArchive hands add a non-closing reader: every member is read from the
// one tar.Reader, which the next call to Next repositions rather than reopens.
func readTarArchive(input io.Reader, epoch int64, contents *archiveContents) (err error) {
	gz, err := gzip.NewReader(input)
	if err != nil {
		return fmt.Errorf("open release gzip: %w", err)
	}
	defer func() { err = errors.Join(err, gz.Close()) }()
	reader := tar.NewReader(gz)
	for {
		header, nextErr := reader.Next()
		if nextErr == io.EOF {
			return nil
		}
		if nextErr != nil {
			return fmt.Errorf("read release tar: %w", nextErr)
		}
		if header.FileInfo().IsDir() {
			continue
		}
		if validateErr := validateTarHeader(header, epoch); validateErr != nil {
			return validateErr
		}
		opener := func() (io.ReadCloser, error) { return io.NopCloser(reader), nil }
		if addErr := contents.add(header.Name, opener); addErr != nil {
			return addErr
		}
	}
}

func readZIPArchive(input io.ReaderAt, size, epoch int64, contents *archiveContents) error {
	reader, err := zip.NewReader(input, size)
	if err != nil {
		return fmt.Errorf("open release zip: %w", err)
	}
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		if validateErr := validateZIPHeader(file, epoch); validateErr != nil {
			return validateErr
		}
		if addErr := contents.add(file.Name, file.Open); addErr != nil {
			return addErr
		}
	}

	return nil
}

// add closes the member even when the read succeeded, reporting a close
// failure as its own error.
func (contents *archiveContents) add(name string, open func() (io.ReadCloser, error)) error {
	limit, needed, err := contents.claim(name)
	if err != nil || !needed {
		return err
	}
	opened, err := open()
	if err != nil {
		return fmt.Errorf("open release archive member %s: %w", name, err)
	}
	data, readErr := readMember(opened, name, limit)
	if err := errors.Join(readErr, opened.Close()); err != nil {
		return err
	}
	contents.store(name, data)

	return nil
}

// claim admits one member and reports the payload byte limit for the members
// whose contents the SBOM needs.
func (contents *archiveContents) claim(name string) (limit int64, needed bool, err error) {
	if name == "" || path.IsAbs(name) || path.Clean(name) != name || strings.Contains(name, `\`) {
		return 0, false, fmt.Errorf("unsafe release archive member %q", name)
	}
	if _, exists := contents.members[name]; exists {
		return 0, false, fmt.Errorf("duplicate release archive member %q", name)
	}
	contents.members[name] = struct{}{}
	if name == contents.binaryName {
		return maxBinaryBytes, true, nil
	}
	if slices.Contains(archiveFiles, name) {
		return maxArchiveFileBytes, true, nil
	}

	return 0, false, nil
}

func (contents *archiveContents) store(name string, data []byte) {
	if name == contents.binaryName {
		contents.binary = data

		return
	}
	contents.files[name] = data
}

// verifyMemberSet requires exactly the certified repository files plus the
// runtime binary; equality also guarantees every payload was captured.
func (contents *archiveContents) verifyMemberSet() error {
	want := make(map[string]struct{}, len(archiveFiles)+1)
	for _, name := range archiveFiles {
		want[name] = struct{}{}
	}
	want[contents.binaryName] = struct{}{}
	if !maps.Equal(contents.members, want) {
		return fmt.Errorf("release archive members are %v, want %v",
			slices.Sorted(maps.Keys(contents.members)), slices.Sorted(maps.Keys(want)))
	}

	return nil
}

// readMember reads one byte past the limit so an oversized member is an error
// rather than a silently truncated payload whose digest would still be
// published.
func readMember(reader io.Reader, name string, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read release archive member %s: %w", name, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("release archive member %s exceeds the size limit", name)
	}

	return data, nil
}

// validateTarHeader holds the archive to the shape .goreleaser.yaml declares:
// regular files owned by root, one mtime for every member, and no PAX extras.
// That mtime is the release commit's timestamp, which the workflow also passes
// as -created-epoch, so a member dated anything else was not built with the
// archive it arrived in.
func validateTarHeader(header *tar.Header, epoch int64) error {
	if header.Typeflag != tar.TypeReg || header.FileInfo().Mode().Perm() != archiveMode(header.Name) {
		return fmt.Errorf("invalid type or mode for release archive member %q", header.Name)
	}
	if !validTarOwnership(header) {
		return fmt.Errorf("invalid ownership for release archive member %q", header.Name)
	}
	if !validTarTimes(header, epoch) {
		return fmt.Errorf("invalid timestamp for release archive member %q", header.Name)
	}
	if !validTarExtras(header) {
		return fmt.Errorf("unexpected metadata for release archive member %q", header.Name)
	}

	return nil
}

func validTarOwnership(header *tar.Header) bool {
	return header.Uid == 0 && header.Gid == 0 && header.Uname == archiveOwner && header.Gname == archiveOwner
}

func validTarTimes(header *tar.Header, epoch int64) bool {
	return exactArchiveTime(header.ModTime, epoch) && header.AccessTime.IsZero() && header.ChangeTime.IsZero()
}

func validTarExtras(header *tar.Header) bool {
	return header.Linkname == "" && header.Devmajor == 0 && header.Devminor == 0 && len(header.PAXRecords) == 0
}

// validateZIPHeader requires the UNIX creator byte because archive/zip reads
// permission bits out of the external attributes only when the version-made-by
// field says UNIX; anything else yields a mode translated from DOS attributes,
// which would make the mode check meaningless. Flag bit 0 marks an encrypted
// member, which no release archive has.
func validateZIPHeader(file *zip.File, epoch int64) error {
	const zipCreatorUnix = 3
	mode := file.FileInfo().Mode()
	if !mode.IsRegular() || mode.Perm() != archiveMode(file.Name) || file.CreatorVersion>>8 != zipCreatorUnix {
		return fmt.Errorf("invalid type or mode for release archive member %q", file.Name)
	}
	if !exactArchiveTime(file.Modified, epoch) || file.Method != zip.Deflate || file.Flags&1 != 0 ||
		file.NonUTF8 || file.Comment != "" {
		return fmt.Errorf("invalid ZIP metadata for release archive member %q", file.Name)
	}

	return nil
}

func exactArchiveTime(value time.Time, epoch int64) bool {
	return value.Unix() == epoch && value.Nanosecond() == 0
}

// archiveMode is the per-member mode .goreleaser.yaml sets: 0755 for the
// runtime and the uninstall shell script, 0644 for everything else.
func archiveMode(name string) fs.FileMode {
	if name == projectName || name == projectName+".exe" || name == "deploy/uninstall.sh" {
		return 0o755
	}

	return 0o644
}

func binaryName(goos string) string {
	if goos == "windows" {
		return "prukka.exe"
	}

	return "prukka"
}

// loadEmbeddedFiles re-sorts because the driver names are appended after the
// walk; fs.WalkDir on its own already yields lexical order.
func loadEmbeddedFiles(repo string, target *releaseTarget) (files []embeddedFile, err error) {
	root, err := os.OpenRoot(repo)
	if err != nil {
		return nil, fmt.Errorf("open repository: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	names, err := webBundleNames(root.FS())
	if err != nil {
		return nil, err
	}
	for _, driver := range target.drivers {
		names = append(names, "internal/devices/assets/"+driver)
	}
	slices.Sort(names)
	files = make([]embeddedFile, 0, len(names))
	for _, name := range names {
		data, readErr := root.ReadFile(filepath.FromSlash(name))
		if readErr != nil {
			return nil, fmt.Errorf("read embedded asset %s: %w", name, readErr)
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("embedded asset is empty: %s", name)
		}
		files = append(files, embeddedFile{name: name, data: data})
	}

	return files, nil
}

// loadArchiveFiles refuses an archive whose copy of a repository file differs
// from the repository's, so the one digest the SBOM publishes identifies both.
func loadArchiveFiles(repo string, contents *archiveContents) (files []embeddedFile, err error) {
	root, err := os.OpenRoot(repo)
	if err != nil {
		return nil, fmt.Errorf("open repository: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()

	files = make([]embeddedFile, 0, len(archiveFiles))
	for _, name := range archiveFiles {
		repoData, readErr := root.ReadFile(filepath.FromSlash(name))
		if readErr != nil {
			return nil, fmt.Errorf("read archived repository file %s: %w", name, readErr)
		}
		if !bytes.Equal(contents.files[name], repoData) {
			return nil, fmt.Errorf("release archive member differs from repository: %s", name)
		}
		files = append(files, embeddedFile{name: name, data: contents.files[name]})
	}

	return files, nil
}

func loadNPMPackages(repo string) (packages []npmPackage, err error) {
	root, err := os.OpenRoot(repo)
	if err != nil {
		return nil, fmt.Errorf("open repository: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	lock, err := readPackageLock(root)
	if err != nil {
		return nil, err
	}

	return collectNPMPackages(lock.Packages)
}

// readPackageLock pins lockfileVersion 3, whose "packages" map keyed by
// install path is the only shape parseNPMPackage can read; nothing here can
// interpret an older lockfile's dependency tree.
func readPackageLock(root *os.Root) (*packageLock, error) {
	data, err := root.ReadFile("web/package-lock.json")
	if err != nil {
		return nil, fmt.Errorf("read dashboard lockfile: %w", err)
	}
	var lock packageLock
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, fmt.Errorf("decode dashboard lockfile: %w", err)
	}
	if lock.Name != dashboardPackage || lock.LockfileVersion != 3 || len(lock.Packages) == 0 {
		return nil, errors.New("unexpected dashboard lockfile identity")
	}

	return &lock, nil
}

// collectNPMPackages drops the optional entries, which npm may or may not have
// installed, and sorts by install path so the declared dependency list does not
// inherit Go's randomized map iteration order.
func collectNPMPackages(entries map[string]packageLockEntry) ([]npmPackage, error) {
	packages := make([]npmPackage, 0, len(entries))
	for packagePath, entry := range entries {
		if packagePath == "" || entry.Optional {
			continue
		}
		parsed, parseErr := parseNPMPackage(packagePath, &entry)
		if parseErr != nil {
			return nil, parseErr
		}
		packages = append(packages, parsed)
	}
	if len(packages) == 0 {
		return nil, errors.New("dashboard lockfile has no required packages")
	}
	slices.SortFunc(packages, func(a, b npmPackage) int {
		return strings.Compare(a.path, b.path)
	})

	return packages, nil
}

func parseNPMPackage(packagePath string, entry *packageLockEntry) (npmPackage, error) {
	name, err := npmName(packagePath)
	if err != nil {
		return npmPackage{}, err
	}
	if entry.Name != "" && entry.Name != name {
		return npmPackage{}, fmt.Errorf("dashboard lockfile package name mismatch at %s", packagePath)
	}
	if validationErr := validateNPMResolution(entry.Resolved); validationErr != nil {
		return npmPackage{}, fmt.Errorf(
			"invalid dashboard lockfile resolution for %s: %w", packagePath, validationErr,
		)
	}
	digest, err := parseSRI(entry.Integrity)
	if err != nil {
		return npmPackage{}, fmt.Errorf("invalid dashboard lockfile integrity for %s: %w", packagePath, err)
	}
	if entry.Version == "" || entry.License == "" {
		return npmPackage{}, fmt.Errorf("incomplete dashboard lockfile package at %s", packagePath)
	}

	return npmPackage{
		path:      packagePath,
		name:      name,
		version:   entry.Version,
		resolved:  entry.Resolved,
		integrity: entry.Integrity,
		license:   entry.License,
		sha512:    digest,
	}, nil
}

// validateNPMResolution refuses a URL carrying credentials because the
// resolved URL is published verbatim as the package's downloadLocation, where
// a userinfo component would be a secret shipped inside the SBOM.
func validateNPMResolution(raw string) error {
	resolved, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if resolved.Scheme != "https" || resolved.Host == "" || resolved.User != nil {
		return errors.New("expected an absolute HTTPS URL without credentials")
	}

	return nil
}

// npmName takes the segment after the last node_modules/ and refuses a nested
// or multi-segment remainder, so "@scope/name" is the only shape left that
// still holds a slash.
func npmName(packagePath string) (string, error) {
	const marker = "node_modules/"
	index := strings.LastIndex(packagePath, marker)
	if index < 0 {
		return "", fmt.Errorf("unsupported dashboard lockfile package path %q", packagePath)
	}
	name := packagePath[index+len(marker):]
	if name == "" || strings.Contains(name, "/node_modules/") || strings.Count(name, "/") > 1 ||
		(strings.HasPrefix(name, "@") && strings.Count(name, "/") != 1) ||
		(!strings.HasPrefix(name, "@") && strings.Contains(name, "/")) {
		return "", fmt.Errorf("unsupported dashboard lockfile package path %q", packagePath)
	}

	return name, nil
}

// parseSRI accepts one sha512 hash and no option string. Subresource Integrity
// allows a space-separated list of hashes and a "?"-suffixed option string on
// each, either of which would leave the digest this package declares ambiguous.
func parseSRI(integrity string) ([sha512.Size]byte, error) {
	var digest [sha512.Size]byte
	encoded, ok := strings.CutPrefix(integrity, "sha512-")
	if !ok || encoded == "" || strings.ContainsAny(encoded, " \t\r\n?") {
		return digest, errors.New("expected one SHA-512 SRI digest")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(decoded) != len(digest) {
		return digest, errors.New("invalid SHA-512 SRI digest")
	}
	copy(digest[:], decoded)

	return digest, nil
}

// webBundleNames refuses an empty bundle: an SBOM built from an unbuilt web/
// tree would declare a dashboard package with no files behind it.
func webBundleNames(root fs.FS) ([]string, error) {
	const bundle = "internal/webui/dist"
	var names []string
	err := fs.WalkDir(root, bundle, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() {
			names = append(names, path)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk embedded web bundle: %w", err)
	}
	if len(names) == 0 {
		return nil, errors.New("embedded web bundle is empty")
	}

	return names, nil
}

// verifyEmbeddedBytes searches the shipped binary for each asset's bytes. It is
// the one check that does not take go:embed's word for it that the tree the
// SBOM reads is the tree the binary was built from.
func verifyEmbeddedBytes(binary []byte, files []embeddedFile) error {
	for _, file := range files {
		if !bytes.Contains(binary, file.data) {
			return fmt.Errorf("runtime does not contain embedded asset %s", file.name)
		}
	}

	return nil
}

func digestFile(filename string) (value string, err error) {
	root, err := os.OpenRoot(filepath.Dir(filename))
	if err != nil {
		return "", fmt.Errorf("open release subject directory: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	file, err := root.Open(filepath.Base(filename))
	if err != nil {
		return "", fmt.Errorf("open release subject: %w", err)
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", fmt.Errorf("digest release subject: %w", err)
	}

	return hex.EncodeToString(digest.Sum(nil)), nil
}
