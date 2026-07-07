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

type packageLockEntry struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Resolved  string `json:"resolved"`
	Integrity string `json:"integrity"`
	License   string `json:"license"`
	Optional  bool   `json:"optional"`
}

type packageLock struct {
	Packages        map[string]packageLockEntry `json:"packages"`
	Name            string                      `json:"name"`
	LockfileVersion int                         `json:"lockfileVersion"`
}

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

func readReleaseBinary(dist string, target *releaseTarget) (binary []byte, err error) {
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
	if strings.HasSuffix(target.archive, ".zip") {
		info, statErr := file.Stat()
		if statErr != nil {
			return nil, fmt.Errorf("stat release archive: %w", statErr)
		}
		return readZIPBinary(file, info.Size(), target.goos)
	}
	return readTarBinary(file, target.goos)
}

func verifyArchiveMembers(dist string, target *releaseTarget, epoch int64) (err error) {
	root, err := os.OpenRoot(dist)
	if err != nil {
		return fmt.Errorf("open release directory: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	file, err := root.Open(target.archive)
	if err != nil {
		return fmt.Errorf("open release archive: %w", err)
	}
	defer func() { err = errors.Join(err, file.Close()) }()

	var members map[string]struct{}
	if strings.HasSuffix(target.archive, ".zip") {
		info, statErr := file.Stat()
		if statErr != nil {
			return fmt.Errorf("stat release archive: %w", statErr)
		}
		members, err = zipMembers(file, info.Size(), epoch)
	} else {
		members, err = tarMembers(file, epoch)
	}
	if err != nil {
		return err
	}
	want := make(map[string]struct{}, len(archiveFiles)+1)
	for _, name := range archiveFiles {
		want[name] = struct{}{}
	}
	want[binaryName(target.goos)] = struct{}{}
	if !maps.Equal(members, want) {
		return fmt.Errorf("release archive members are %v, want %v",
			slices.Sorted(maps.Keys(members)), slices.Sorted(maps.Keys(want)))
	}

	return nil
}

func tarMembers(input io.Reader, epoch int64) (_ map[string]struct{}, err error) {
	gz, err := gzip.NewReader(input)
	if err != nil {
		return nil, fmt.Errorf("open release gzip: %w", err)
	}
	defer func() { err = errors.Join(err, gz.Close()) }()

	members := make(map[string]struct{}, len(archiveFiles)+1)
	reader := tar.NewReader(gz)
	for {
		header, nextErr := reader.Next()
		if nextErr == io.EOF {
			return members, nil
		}
		if nextErr != nil {
			return nil, fmt.Errorf("read release tar: %w", nextErr)
		}
		if header.FileInfo().IsDir() {
			continue
		}
		if err := validateTarHeader(header, epoch); err != nil {
			return nil, err
		}
		if err := addArchiveMember(members, header.Name); err != nil {
			return nil, err
		}
	}
}

func zipMembers(input io.ReaderAt, size, epoch int64) (map[string]struct{}, error) {
	reader, err := zip.NewReader(input, size)
	if err != nil {
		return nil, fmt.Errorf("open release zip: %w", err)
	}
	members := make(map[string]struct{}, len(archiveFiles)+1)
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		if err := validateZIPHeader(file, epoch); err != nil {
			return nil, err
		}
		if err := addArchiveMember(members, file.Name); err != nil {
			return nil, err
		}
	}

	return members, nil
}

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

func archiveMode(name string) fs.FileMode {
	if name == projectName || name == projectName+".exe" || name == "deploy/uninstall.sh" {
		return 0o755
	}
	return 0o644
}

func addArchiveMember(members map[string]struct{}, name string) error {
	if name == "" || path.IsAbs(name) || path.Clean(name) != name || strings.Contains(name, `\`) {
		return fmt.Errorf("unsafe release archive member %q", name)
	}
	if _, exists := members[name]; exists {
		return fmt.Errorf("duplicate release archive member %q", name)
	}
	members[name] = struct{}{}

	return nil
}

func readTarBinary(input io.Reader, goos string) (binary []byte, err error) {
	gz, err := gzip.NewReader(input)
	if err != nil {
		return nil, fmt.Errorf("open release gzip: %w", err)
	}
	defer func() { err = errors.Join(err, gz.Close()) }()
	reader := tar.NewReader(gz)
	for {
		header, nextErr := reader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return nil, fmt.Errorf("read release tar: %w", nextErr)
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != binaryName(goos) {
			continue
		}
		if binary != nil {
			return nil, errors.New("release archive contains duplicate runtimes")
		}
		binary, err = readBounded(reader)
		if err != nil {
			return nil, err
		}
	}
	if binary == nil {
		return nil, errors.New("release archive does not contain the runtime")
	}
	return binary, nil
}

func readZIPBinary(input io.ReaderAt, size int64, goos string) ([]byte, error) {
	reader, err := zip.NewReader(input, size)
	if err != nil {
		return nil, fmt.Errorf("open release zip: %w", err)
	}
	var binary []byte
	for _, file := range reader.File {
		if !file.FileInfo().Mode().IsRegular() || filepath.Base(file.Name) != binaryName(goos) {
			continue
		}
		if binary != nil {
			return nil, errors.New("release archive contains duplicate runtimes")
		}
		opened, openErr := file.Open()
		if openErr != nil {
			return nil, fmt.Errorf("open runtime in zip: %w", openErr)
		}
		binary, err = readBounded(opened)
		closeErr := opened.Close()
		if err != nil || closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
	}
	if binary == nil {
		return nil, errors.New("release archive does not contain the runtime")
	}
	return binary, nil
}

func readBounded(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxBinaryBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read release runtime: %w", err)
	}
	if len(data) > maxBinaryBytes {
		return nil, errors.New("release runtime exceeds the size limit")
	}
	return data, nil
}

func binaryName(goos string) string {
	if goos == "windows" {
		return "prukka.exe"
	}
	return "prukka"
}

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

func loadArchiveFiles(dist, repo string, target *releaseTarget) ([]embeddedFile, error) {
	contents, err := readArchiveFiles(filepath.Join(dist, target.archive))
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(repo)
	if err != nil {
		return nil, fmt.Errorf("open repository: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()

	files := make([]embeddedFile, 0, len(archiveFiles))
	for _, name := range archiveFiles {
		archiveData, ok := contents[name]
		if !ok {
			return nil, fmt.Errorf("release archive member is missing: %s", name)
		}
		repoData, readErr := root.ReadFile(filepath.FromSlash(name))
		if readErr != nil {
			return nil, fmt.Errorf("read archived repository file %s: %w", name, readErr)
		}
		if !bytes.Equal(archiveData, repoData) {
			return nil, fmt.Errorf("release archive member differs from repository: %s", name)
		}
		files = append(files, embeddedFile{name: name, data: archiveData})
	}
	return files, nil
}

func readArchiveFiles(filename string) (files map[string][]byte, err error) {
	root, err := os.OpenRoot(filepath.Dir(filename))
	if err != nil {
		return nil, fmt.Errorf("open release directory: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	file, err := root.Open(filepath.Base(filename))
	if err != nil {
		return nil, fmt.Errorf("open release archive: %w", err)
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	if strings.HasSuffix(filename, ".zip") {
		info, statErr := file.Stat()
		if statErr != nil {
			return nil, fmt.Errorf("stat release archive: %w", statErr)
		}
		return readZIPFiles(file, info.Size())
	}
	return readTarFiles(file)
}

func readTarFiles(input io.Reader) (_ map[string][]byte, err error) {
	gz, err := gzip.NewReader(input)
	if err != nil {
		return nil, fmt.Errorf("open release gzip: %w", err)
	}
	defer func() { err = errors.Join(err, gz.Close()) }()
	reader := tar.NewReader(gz)
	files := make(map[string][]byte, len(archiveFiles))
	for {
		header, nextErr := reader.Next()
		if nextErr == io.EOF {
			return files, nil
		}
		if nextErr != nil {
			return nil, fmt.Errorf("read release tar: %w", nextErr)
		}
		if !slices.Contains(archiveFiles, header.Name) {
			continue
		}
		data, readErr := readArchiveFile(reader)
		if readErr != nil {
			return nil, fmt.Errorf("read release archive member %s: %w", header.Name, readErr)
		}
		files[header.Name] = data
	}
}

func readZIPFiles(input io.ReaderAt, size int64) (map[string][]byte, error) {
	reader, err := zip.NewReader(input, size)
	if err != nil {
		return nil, fmt.Errorf("open release zip: %w", err)
	}
	files := make(map[string][]byte, len(archiveFiles))
	for _, file := range reader.File {
		if !slices.Contains(archiveFiles, file.Name) {
			continue
		}
		opened, openErr := file.Open()
		if openErr != nil {
			return nil, fmt.Errorf("open release archive member %s: %w", file.Name, openErr)
		}
		data, readErr := readArchiveFile(opened)
		closeErr := opened.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return nil, fmt.Errorf("read release archive member %s: %w", file.Name, err)
		}
		files[file.Name] = data
	}
	return files, nil
}

func readArchiveFile(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxArchiveFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxArchiveFileBytes {
		return nil, errors.New("file exceeds the size limit")
	}
	return data, nil
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
		path: packagePath, name: name, version: entry.Version, resolved: entry.Resolved,
		integrity: entry.Integrity, license: entry.License, sha512: digest,
	}, nil
}

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
