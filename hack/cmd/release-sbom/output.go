package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func verifyArchiveSet(dist string) error {
	expected := make(map[string]struct{}, len(releaseTargets))
	for _, target := range releaseTargets {
		expected[target.archive] = struct{}{}
	}

	return verifyNamedSet(dist, expected, isReleaseArchive, "release archive")
}

// verifySBOMSet requires the namespaces to be distinct, so one document per
// target cannot silently become one document repeated across targets.
func verifySBOMSet(dist string) (err error) {
	root, err := os.OpenRoot(dist)
	if err != nil {
		return fmt.Errorf("open release directory: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	expected := make(map[string]struct{}, len(releaseTargets))
	namespaces := make(map[string]struct{}, len(releaseTargets))
	for _, target := range releaseTargets {
		name := target.name + ".spdx.json"
		expected[name] = struct{}{}
		data, err := root.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read release SBOM: %w", err)
		}
		var document struct {
			Namespace string `json:"documentNamespace"`
		}
		if err := json.Unmarshal(data, &document); err != nil || document.Namespace == "" {
			return fmt.Errorf("invalid document namespace in %s", name)
		}
		namespaces[document.Namespace] = struct{}{}
	}
	if len(namespaces) != len(releaseTargets) {
		return errors.New("release SBOMs are not distinct")
	}

	return verifyNamedSet(dist, expected, func(name string) bool { return strings.HasSuffix(name, ".spdx.json") }, "SBOM")
}

// verifyNamedSet rejects an unexpected name as well as a missing one: an
// archive left in dist/ by an earlier build would otherwise be published with
// no SBOM describing it.
func verifyNamedSet(dist string, expected map[string]struct{}, selectName func(string) bool, label string) error {
	entries, err := os.ReadDir(dist)
	if err != nil {
		return fmt.Errorf("read release directory: %w", err)
	}
	seen := make(map[string]struct{}, len(expected))
	for _, entry := range entries {
		if entry.Type().IsRegular() && selectName(entry.Name()) {
			if _, ok := expected[entry.Name()]; !ok {
				return fmt.Errorf("unexpected %s: %s", label, entry.Name())
			}
			seen[entry.Name()] = struct{}{}
		}
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("%s set is incomplete", label)
	}

	return nil
}

func isReleaseArchive(name string) bool {
	return strings.HasPrefix(name, "prukka_") && (strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".zip"))
}

// writeChecksums reuses archiveDigests, which still match the bytes on disk
// because nothing rewrites an archive after certification.
func writeChecksums(dist string, archiveDigests map[string]string) error {
	names := make([]string, 0, len(releaseTargets)*2)
	for _, target := range releaseTargets {
		names = append(names, target.archive, target.name+".spdx.json")
	}
	slices.Sort(names)
	var output strings.Builder
	for _, name := range names {
		digest, ok := archiveDigests[name]
		if !ok {
			computed, err := digestFile(filepath.Join(dist, name))
			if err != nil {
				return err
			}
			digest = computed
		}
		fmt.Fprintf(&output, "%s  %s\n", digest, name)
	}

	return writeAtomic(filepath.Join(dist, "checksums.txt"), []byte(output.String()))
}

// writeAtomic renames over the destination, so a reader never observes a
// half-written document, and syncs the bytes before the rename, so the name
// never comes to point at content the kernel has not written.
func writeAtomic(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".prukka-sbom-*")
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	tempName := temp.Name()
	if err := temp.Chmod(0o644); err != nil {
		return errors.Join(fmt.Errorf("set output permissions: %w", err), temp.Close(), os.Remove(tempName))
	}
	writeErr := writeAndSync(temp, data)
	closeErr := temp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return errors.Join(err, os.Remove(tempName))
	}
	if err := os.Rename(tempName, path); err != nil {
		return errors.Join(fmt.Errorf("activate output: %w", err), os.Remove(tempName))
	}

	return nil
}

func writeAndSync(file *os.File, data []byte) error {
	if _, err := io.Copy(file, bytes.NewReader(data)); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync output: %w", err)
	}

	return nil
}
