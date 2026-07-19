package speech

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Extraction caps: a runtime bundle holds a few hundred small library files
// plus model-scale binaries; anything beyond these bounds is hostile input.
const (
	maxArchiveEntries = 4096
	maxArchiveFile    = maxArtifactBytes
	maxArchiveTotal   = 3 << 30
)

// extractArchive unpacks one verified tar.gz into dir, which must exist and
// be empty of conflicting entries. Only directories, regular files and
// bundle-internal symlinks are admitted; every path is containment-checked.
// It returns the archive's regular-file and symlink paths as slash paths.
func extractArchive(src io.Reader, dir string) (files []string, err error) {
	gz, err := gzip.NewReader(src)
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	defer func() { err = errors.Join(err, gz.Close()) }()

	reader := tar.NewReader(gz)
	var entries int
	var total int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return files, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		entries++
		if entries > maxArchiveEntries {
			return nil, fmt.Errorf("archive exceeds %d entries", maxArchiveEntries)
		}
		if total += header.Size; total > maxArchiveTotal {
			return nil, fmt.Errorf("archive expands past %d bytes", int64(maxArchiveTotal))
		}

		name, err := cleanEntryPath(header.Name)
		if err != nil {
			return nil, err
		}
		owned, err := extractEntry(reader, header, dir, name)
		if err != nil {
			return nil, err
		}
		if owned {
			files = append(files, name)
		}
	}
}

// extractEntry materializes one entry; it reports whether the entry is a
// file the caller must track for later removal.
func extractEntry(reader *tar.Reader, header *tar.Header, dir, name string) (bool, error) {
	dest := filepath.Join(dir, filepath.FromSlash(name))
	switch header.Typeflag {
	case tar.TypeDir:
		return false, os.MkdirAll(dest, 0o700)
	case tar.TypeSymlink:
		if err := checkLinkTarget(name, header.Linkname); err != nil {
			return false, err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return false, err
		}

		return true, os.Symlink(header.Linkname, dest)
	case tar.TypeReg:
		return true, writeEntryFile(reader, header, dest)
	default:
		return false, fmt.Errorf("archive entry %q has unsupported type %d", name, header.Typeflag)
	}
}

func writeEntryFile(reader *tar.Reader, header *tar.Header, dest string) error {
	if header.Size > maxArchiveFile {
		return fmt.Errorf("archive entry %q exceeds %d bytes", header.Name, int64(maxArchiveFile))
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}

	f, err := os.OpenFile(filepath.Clean(dest), os.O_CREATE|os.O_EXCL|os.O_WRONLY, entryMode(header))
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, io.LimitReader(reader, header.Size)); err != nil {
		closeQuietly(f)

		return fmt.Errorf("write %q: %w", dest, err)
	}

	return f.Close()
}

// entryMode keeps only the producer's execute intent, exactly like the
// deterministic archiver that wrote the artifact.
func entryMode(header *tar.Header) os.FileMode {
	if header.FileInfo().Mode()&0o111 != 0 {
		return 0o700
	}

	return 0o600
}

// cleanEntryPath admits only forward, relative, well-formed member paths.
func cleanEntryPath(name string) (string, error) {
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("archive entry %q has an invalid path", name)
	}
	clean := filepath.ToSlash(filepath.Clean(name))
	if clean == ".." || strings.HasPrefix(clean, "../") || clean == "." {
		return "", fmt.Errorf("archive entry %q escapes the bundle", name)
	}

	return clean, nil
}

// checkLinkTarget admits only relative link targets that stay inside the
// bundle from the link's own directory.
func checkLinkTarget(name, target string) error {
	if target == "" || strings.HasPrefix(target, "/") || strings.ContainsRune(target, 0) {
		return fmt.Errorf("archive symlink %q has an invalid target %q", name, target)
	}
	joined := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(filepath.FromSlash(name)), target)))
	if joined == ".." || strings.HasPrefix(joined, "../") {
		return fmt.Errorf("archive symlink %q escapes the bundle via %q", name, target)
	}

	return nil
}
