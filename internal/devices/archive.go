package devices

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxArchiveEntries = 4096
	maxArchiveFile    = 64 << 20
	maxArchiveSize    = 256 << 20
)

// extract unpacks a gzip'd tar into dir, preserving file modes and
// refusing entries that would escape dir. The caller's umask is held
// open: drivers are read by system daemons (coreaudiod runs as
// _coreaudiod), so a umask-077 sudo shell must not produce 0700 bundles.
func extract(data []byte, dir string) error {
	return withOpenUmask(func() error { return extractAll(data, dir) })
}

// extractAll walks the archive entries.
func extractAll(data []byte, dir string) (err error) {
	gz, gzErr := gzip.NewReader(bytes.NewReader(data))
	if gzErr != nil {
		return fmt.Errorf("open driver archive: %w", gzErr)
	}
	defer func() { err = errors.Join(err, gz.Close()) }()

	archive := tar.NewReader(gz)
	seen := make(map[string]struct{})
	var total int64

	for entries := 0; ; entries++ {
		header, nextErr := archive.Next()
		if errors.Is(nextErr, io.EOF) {
			return nil
		}

		if nextErr != nil {
			return fmt.Errorf("read driver archive: %w", nextErr)
		}
		if entries >= maxArchiveEntries {
			return fmt.Errorf("driver archive exceeds %d entries", maxArchiveEntries)
		}

		var sizeErr error
		total, sizeErr = validateEntry(header, total)
		if sizeErr != nil {
			return sizeErr
		}

		target, pathErr := securePath(dir, header.Name)
		if pathErr != nil {
			return pathErr
		}
		if _, duplicate := seen[target]; duplicate {
			return fmt.Errorf("duplicate entry %s in driver archive", header.Name)
		}
		seen[target] = struct{}{}

		if err := extractEntry(archive, header, dir, target); err != nil {
			return err
		}
	}
}

// extractEntry writes one archive entry under dir.
func extractEntry(archive *tar.Reader, header *tar.Header, dir, target string) error {
	if err := rejectSymlinkParent(dir, target); err != nil {
		return err
	}

	switch header.Typeflag {
	case tar.TypeDir:
		if err := os.MkdirAll(target, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", target, err)
		}

		return nil
	case tar.TypeSymlink:
		if _, err := secureLinkTarget(dir, target, header.Linkname); err != nil {
			return fmt.Errorf("unsafe link %s in driver archive", header.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(target), err)
		}
		if err := os.Symlink(filepath.FromSlash(header.Linkname), target); err != nil {
			return fmt.Errorf("link %s: %w", target, err)
		}

		return nil
	case tar.TypeReg:
		return writeEntry(archive, header, target)
	default:
		return fmt.Errorf("unsupported entry %s in driver archive", header.Name)
	}
}

func validateEntry(header *tar.Header, total int64) (int64, error) {
	if header.Mode < 0 || header.Mode&^0o777 != 0 {
		return total, fmt.Errorf("unsafe mode %#o for %s in driver archive", header.Mode, header.Name)
	}
	if header.Size < 0 {
		return total, fmt.Errorf("negative size for %s in driver archive", header.Name)
	}

	switch header.Typeflag {
	case tar.TypeReg:
		return validateRegularEntry(header, total)
	case tar.TypeDir:
		if header.Mode&0o100 == 0 {
			return total, fmt.Errorf("untraversable mode %#o for %s in driver archive", header.Mode, header.Name)
		}

		return validateNonFileEntry(header, total)
	case tar.TypeSymlink:
		return validateNonFileEntry(header, total)
	default:
		return total, fmt.Errorf("unsupported entry %s in driver archive", header.Name)
	}
}

func validateRegularEntry(header *tar.Header, total int64) (int64, error) {
	if header.Mode&0o400 == 0 {
		return total, fmt.Errorf("unreadable mode %#o for %s in driver archive", header.Mode, header.Name)
	}
	if header.Size > maxArchiveFile {
		return total, fmt.Errorf("driver archive entry %s exceeds %d bytes", header.Name, maxArchiveFile)
	}
	if header.Size > maxArchiveSize-total {
		return total, fmt.Errorf("driver archive exceeds %d decompressed bytes", maxArchiveSize)
	}

	return total + header.Size, nil
}

func validateNonFileEntry(header *tar.Header, total int64) (int64, error) {
	if header.Size != 0 {
		return total, fmt.Errorf("non-file entry %s has size %d", header.Name, header.Size)
	}

	return total, nil
}

// writeEntry streams one file out of the archive with its recorded mode.
func writeEntry(archive *tar.Reader, header *tar.Header, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(target), err)
	}

	f, openErr := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, header.FileInfo().Mode().Perm())
	if openErr != nil {
		return fmt.Errorf("create %s: %w", target, openErr)
	}

	_, copyErr := io.CopyN(f, archive, header.Size)
	if err := errors.Join(copyErr, f.Close()); err != nil {
		return fmt.Errorf("write %s: %w", target, errors.Join(err, os.Remove(target)))
	}

	return nil
}

// securePath joins name under dir, rejecting escapes.
func securePath(dir, name string) (string, error) {
	name = filepath.FromSlash(name)
	if name == "" || filepath.IsAbs(name) || filepath.VolumeName(name) != "" {
		return "", fmt.Errorf("entry %s escapes %s", name, dir)
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve driver destination: %w", err)
	}
	target := filepath.Join(root, name)
	if !pathWithin(root, target) {
		return "", fmt.Errorf("entry %s escapes %s", name, dir)
	}

	return target, nil
}

func secureLinkTarget(dir, target, link string) (string, error) {
	link = filepath.FromSlash(link)
	if link == "" || filepath.IsAbs(link) || filepath.VolumeName(link) != "" {
		return "", fmt.Errorf("unsafe link target %q", link)
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(target), link))
	if !pathWithin(root, resolved) {
		return "", fmt.Errorf("link target %q escapes %s", link, dir)
	}

	return resolved, nil
}

func pathWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}

	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func rejectSymlinkParent(root, target string) error {
	parent := filepath.Dir(target)
	rel, err := filepath.Rel(root, parent)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("entry parent %s escapes %s", parent, root)
	}

	current := root
	if rel == "." {
		return nil
	}
	for part := range strings.SplitSeq(rel, string(os.PathSeparator)) {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if os.IsNotExist(statErr) {
			return nil
		}
		if statErr != nil {
			return fmt.Errorf("inspect archive path %s: %w", current, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("entry %s follows symlink %s", target, current)
		}
		if !info.IsDir() {
			return fmt.Errorf("entry parent %s is not a directory", current)
		}
	}

	return nil
}
