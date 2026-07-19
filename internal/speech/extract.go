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
// symlinks whose target reads as bundle-internal are admitted. Containment is
// the kernel's job: an *os.Root resolves every entry with openat-relative
// syscalls, so no chain of archive-created symlinks can walk out of the bundle
// the way a per-entry textual check allows. That also means a path component
// that IS a symlink is never traversed, so an archive listing entries beneath a
// directory link fails — a shape the deterministic archiver never produces.
// The on-disk tree is the single authority on what was materialized: the
// runtime path probes it (nativeHelpersPresent) and the pack path walks it
// (stagedModelFiles).
func extractArchive(src io.Reader, dir string) (err error) {
	gz, err := gzip.NewReader(src)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer func() { err = errors.Join(err, gz.Close()) }()

	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open extraction root: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()

	reader := tar.NewReader(gz)
	var entries int
	var total int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		entries++
		if entries > maxArchiveEntries {
			return fmt.Errorf("archive exceeds %d entries", maxArchiveEntries)
		}
		if total += header.Size; total > maxArchiveTotal {
			return fmt.Errorf("archive expands past %d bytes", int64(maxArchiveTotal))
		}

		name, err := cleanEntryPath(header.Name)
		if err != nil {
			return err
		}
		if err := extractEntry(reader, header, root, name); err != nil {
			return err
		}
	}
}

// extractEntry materializes one entry inside root.
func extractEntry(reader *tar.Reader, header *tar.Header, root *os.Root, name string) error {
	dest := filepath.FromSlash(name)
	switch header.Typeflag {
	case tar.TypeDir:
		return root.MkdirAll(dest, 0o700)
	case tar.TypeSymlink:
		if err := checkLinkTarget(name, header.Linkname); err != nil {
			return err
		}
		if err := root.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return err
		}

		return root.Symlink(header.Linkname, dest)
	case tar.TypeReg:
		return writeEntryFile(reader, header, root, dest)
	default:
		return fmt.Errorf("archive entry %q has unsupported type %d", name, header.Typeflag)
	}
}

func writeEntryFile(reader *tar.Reader, header *tar.Header, root *os.Root, dest string) error {
	if header.Size > maxArchiveFile {
		return fmt.Errorf("archive entry %q exceeds %d bytes", header.Name, int64(maxArchiveFile))
	}
	if err := root.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}

	f, err := root.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, entryMode(header))
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
	if escapesRoot(clean) || clean == "." {
		return "", fmt.Errorf("archive entry %q escapes the bundle", name)
	}

	return clean, nil
}

// checkLinkTarget is a policy check on the target STRING, not a containment
// guarantee: it rejects absolute and lexically escaping targets so an archive
// that means to point outside is refused by name. It cannot bound where the
// link RESOLVES, and must never be read as if it could — filepath.Clean cancels
// "x/.." textually, while the kernel resolves it against whatever x itself
// points at, so a chain of individually admitted links walks one directory
// further out per hop. Containment is the kernel's, on both sides: the *os.Root
// here creates the link inside the bundle, and the other side is a rule, not a
// roster — EVERY consumer that later resolves a bundle path does so through an
// *os.Root over the engine root, whether it publishes, carries, deletes or
// merely probes. A path joined as a string and handed to the OS reopens this
// hole wherever it is written.
func checkLinkTarget(name, target string) error {
	if target == "" || strings.HasPrefix(target, "/") || strings.ContainsRune(target, 0) {
		return fmt.Errorf("archive symlink %q has an invalid target %q", name, target)
	}
	joined := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(filepath.FromSlash(name)), target)))
	if escapesRoot(joined) {
		return fmt.Errorf("archive symlink %q escapes the bundle via %q", name, target)
	}

	return nil
}

// escapesRoot reports whether a cleaned, slash-separated relative path leads
// out of its root. One spelling for both the entry path and the link target,
// so the two containment rules cannot drift apart.
func escapesRoot(clean string) bool {
	return clean == ".." || strings.HasPrefix(clean, "../")
}
