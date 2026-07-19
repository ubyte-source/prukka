package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestVerifyReleaseMetadata(t *testing.T) {
	t.Parallel()

	opts := testOptions()
	valid := releaseMetadata{Project: projectName, Tag: opts.version, Version: opts.version, Commit: opts.commit}
	tests := []struct {
		mutate func(*releaseMetadata)
		name   string
	}{
		{name: "tag", mutate: func(metadata *releaseMetadata) { metadata.Tag = "1.2.4" }},
		{name: "version", mutate: func(metadata *releaseMetadata) { metadata.Version = "1.2.4" }},
		{name: "commit", mutate: func(metadata *releaseMetadata) { metadata.Commit = strings.Repeat("b", 40) }},
		{name: "project", mutate: func(metadata *releaseMetadata) { metadata.Project = "other" }},
	}
	writeMetadata := func(t *testing.T, root string, metadata releaseMetadata) {
		t.Helper()
		data, err := json.Marshal(metadata)
		if err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, root, "metadata.json", string(data))
	}

	t.Run("valid", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeMetadata(t, root, valid)
		if err := verifyReleaseMetadata(root, &opts); err != nil {
			t.Fatalf("valid metadata rejected: %v", err)
		}
	})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			metadata := valid
			test.mutate(&metadata)
			root := t.TempDir()
			writeMetadata(t, root, metadata)
			if err := verifyReleaseMetadata(root, &opts); err == nil {
				t.Fatalf("%s mismatch accepted", test.name)
			}
		})
	}
	t.Run("malformed", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeTestFile(t, root, "metadata.json", "{} {}")
		if err := verifyReleaseMetadata(root, &opts); err == nil {
			t.Fatal("malformed metadata accepted")
		}
	})
	t.Run("missing", func(t *testing.T) {
		t.Parallel()
		if err := verifyReleaseMetadata(t.TempDir(), &opts); err == nil {
			t.Fatal("missing metadata accepted")
		}
	})
}

func TestReadArchive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		goos   string
		format string
	}{
		{name: "tar", goos: osLinux, format: "tar.gz"},
		{name: "zip", goos: "windows", format: "zip"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			target := releaseTarget{
				name: "test", archive: "release." + test.format, goos: test.goos, goarch: archAMD64,
			}
			members := slices.Clone(archiveFiles)
			members = append(members, binaryName(test.goos))
			root := t.TempDir()
			writeArchiveMembers(t, filepath.Join(root, target.archive), test.format, members)
			contents, err := readArchive(root, &target, testOptions().createdEpoch)
			if err != nil {
				t.Fatalf("valid archive rejected: %v", err)
			}
			if !bytes.Equal(contents.binary, []byte(binaryName(test.goos))) {
				t.Fatalf("unexpected runtime payload: %q", contents.binary)
			}
			for _, name := range archiveFiles {
				if !bytes.Equal(contents.files[name], []byte(name)) {
					t.Fatalf("unexpected payload for archive member %s: %q", name, contents.files[name])
				}
			}

			writeArchiveMembers(t, filepath.Join(root, target.archive), test.format, members[:len(members)-1])
			if _, err := readArchive(root, &target, testOptions().createdEpoch); err == nil {
				t.Fatal("archive with a missing member accepted")
			}
			writeArchiveMembers(t, filepath.Join(root, target.archive), test.format, append(members, "extra"))
			if _, err := readArchive(root, &target, testOptions().createdEpoch); err == nil {
				t.Fatal("archive with an unexpected member accepted")
			}
		})
	}
}

func TestLoadNPMPackages(t *testing.T) {
	t.Parallel()
	digest := sha512.Sum512([]byte("package"))
	valid := packageLock{
		Name: dashboardPackage, LockfileVersion: 3,
		Packages: map[string]packageLockEntry{
			"": {Name: dashboardPackage},
			"node_modules/example": {
				Version: "1.2.3", Resolved: "https://registry.npmjs.org/example/-/example-1.2.3.tgz",
				Integrity: "sha512-" + base64.StdEncoding.EncodeToString(digest[:]), License: "MIT",
			},
			"node_modules/optional": {Optional: true},
		},
	}
	root := t.TempDir()
	writePackageLock(t, root, &valid)
	packages, err := loadNPMPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) != 1 || packages[0].name != "example" || packages[0].sha512 != digest {
		t.Fatalf("unexpected npm inventory: %+v", packages)
	}

	entry := valid.Packages["node_modules/example"]
	entry.Integrity = "sha256-invalid"
	valid.Packages["node_modules/example"] = entry
	writePackageLock(t, root, &valid)
	if _, err := loadNPMPackages(root); err == nil {
		t.Fatal("invalid npm integrity accepted")
	}
}

func TestNPMName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		path, want string
		valid      bool
	}{
		{path: "node_modules/plain", want: "plain", valid: true},
		{path: "node_modules/@scope/name", want: "@scope/name", valid: true},
		{path: "node_modules/parent/node_modules/child", want: "child", valid: true},
		{path: "node_modules/plain/nested"},
		{path: "node_modules/@scope"},
		{path: "plain"},
	}
	for _, test := range tests {
		got, err := npmName(test.path)
		if (err == nil) != test.valid || got != test.want {
			t.Errorf("npmName(%q) = %q, %v", test.path, got, err)
		}
	}
}

func writePackageLock(t *testing.T, root string, lock *packageLock) {
	t.Helper()
	data, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "web/package-lock.json", string(data))
}

func writeArchiveMembers(t *testing.T, output, format string, members []string) {
	t.Helper()
	root, err := os.OpenRoot(filepath.Dir(output))
	if err != nil {
		t.Fatal(err)
	}
	file, err := root.Create(filepath.Base(output))
	if err != nil {
		t.Fatal(errors.Join(err, root.Close()))
	}
	var writeErr error
	if format == "zip" {
		writeErr = writeZIPMembers(file, members)
	} else {
		writeErr = writeTarMembers(file, members)
	}
	if err = errors.Join(writeErr, file.Close(), root.Close()); err != nil {
		t.Fatal(err)
	}
}

func writeZIPMembers(output io.Writer, members []string) error {
	writer := zip.NewWriter(output)
	for _, name := range members {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(archiveMode(name))
		header.Modified = time.Unix(testOptions().createdEpoch, 0).UTC()
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return errors.Join(err, writer.Close())
		}
		if _, err = entry.Write([]byte(name)); err != nil {
			return errors.Join(err, writer.Close())
		}
	}

	return writer.Close()
}

func writeTarMembers(output io.Writer, members []string) error {
	gzipWriter := gzip.NewWriter(output)
	writer := tar.NewWriter(gzipWriter)
	for _, name := range members {
		body := []byte(name)
		header := &tar.Header{
			Name: name, Mode: int64(archiveMode(name)), Size: int64(len(body)), Typeflag: tar.TypeReg,
			Uid: 0, Gid: 0, Uname: archiveOwner, Gname: archiveOwner,
			ModTime: time.Unix(testOptions().createdEpoch, 0).UTC(),
		}
		if err := writer.WriteHeader(header); err != nil {
			return errors.Join(err, writer.Close(), gzipWriter.Close())
		}
		if _, err := writer.Write(body); err != nil {
			return errors.Join(err, writer.Close(), gzipWriter.Close())
		}
	}

	return errors.Join(writer.Close(), gzipWriter.Close())
}
