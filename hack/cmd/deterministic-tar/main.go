// Package main writes normalized tar.gz payloads for release builds.
package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

var errInvalidPath = errors.New("archive path must be relative and contained by the root")

type options struct {
	output string
	root   string
	paths  []string
}

func main() {
	var opts options
	flag.StringVar(&opts.output, "output", "", "output .tar.gz path")
	flag.StringVar(&opts.root, "root", "", "directory used as the archive root")
	flag.Parse()
	opts.paths = flag.Args()

	if err := createArchive(opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func createArchive(opts options) error {
	if opts.output == "" || opts.root == "" || len(opts.paths) == 0 {
		return errors.New("output, root, and at least one archive path are required")
	}

	root, err := os.OpenRoot(opts.root)
	if err != nil {
		return fmt.Errorf("open archive root: %w", err)
	}
	entries, err := collect(root.FS(), opts.paths)
	if err != nil {
		return errors.Join(err, root.Close())
	}
	return errors.Join(writeOutput(opts.output, root, entries), root.Close())
}

func writeOutput(output string, root *os.Root, entries []string) error {
	tmp, err := os.CreateTemp(filepath.Dir(output), ".prukka-archive-*")
	if err != nil {
		return fmt.Errorf("create archive: %w", err)
	}
	tmpName := tmp.Name()

	writeErr := writeArchive(tmp, root, entries)
	syncErr := syncAfterWrite(tmp, writeErr == nil)
	closeErr := tmp.Close()
	if archiveErr := errors.Join(writeErr, syncErr, closeErr); archiveErr != nil {
		return errors.Join(archiveErr, removeFile(tmpName))
	}
	if err := os.Rename(tmpName, output); err != nil {
		return errors.Join(fmt.Errorf("activate archive: %w", err), removeFile(tmpName))
	}
	return nil
}

func syncAfterWrite(file *os.File, writeOK bool) error {
	if !writeOK {
		return nil
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync archive: %w", err)
	}
	return nil
}

func removeFile(name string) error {
	err := os.Remove(name)
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	return err
}

func collect(root fs.FS, paths []string) ([]string, error) {
	seen := make(map[string]struct{})
	for _, name := range paths {
		clean, err := cleanArchivePath(name)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", name, err)
		}
		if err := fs.WalkDir(root, clean, func(path string, _ fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if _, ok := seen[path]; ok {
				return fmt.Errorf("duplicate archive entry %q", path)
			}
			seen[path] = struct{}{}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("walk %q: %w", name, err)
		}
	}

	entries := make([]string, 0, len(seen))
	for name := range seen {
		entries = append(entries, name)
	}
	slices.Sort(entries)
	return entries, nil
}

func cleanArchivePath(name string) (string, error) {
	if name == "" || filepath.IsAbs(name) || strings.ContainsRune(name, '\x00') {
		return "", errInvalidPath
	}
	clean := filepath.ToSlash(filepath.Clean(name))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errInvalidPath
	}
	return clean, nil
}

func writeArchive(dst io.Writer, root *os.Root, entries []string) error {
	gz, err := gzip.NewWriterLevel(dst, gzip.BestCompression)
	if err != nil {
		return fmt.Errorf("create gzip stream: %w", err)
	}
	gz.ModTime = time.Unix(0, 0).UTC()
	gz.OS = 255

	tw := tar.NewWriter(gz)
	writeErr := writeEntries(tw, root, entries)
	return errors.Join(writeErr, tw.Close(), gz.Close())
}

func writeEntries(tw *tar.Writer, root *os.Root, entries []string) error {
	for _, name := range entries {
		if err := writeEntry(tw, root, name); err != nil {
			return err
		}
	}
	return nil
}

func writeEntry(tw *tar.Writer, root *os.Root, name string) error {
	header, regular, err := makeHeader(root, name)
	if err != nil {
		return err
	}
	if writeErr := tw.WriteHeader(header); writeErr != nil {
		return fmt.Errorf("write header %q: %w", name, writeErr)
	}
	if !regular {
		return nil
	}
	return writeContents(tw, root, name, header.Size)
}

func makeHeader(root *os.Root, name string) (*tar.Header, bool, error) {
	info, err := root.Lstat(filepath.FromSlash(name))
	if err != nil {
		return nil, false, fmt.Errorf("stat %q: %w", name, err)
	}
	if info.Mode()&(os.ModeDevice|os.ModeNamedPipe|os.ModeSocket) != 0 {
		return nil, false, fmt.Errorf("unsupported archive entry %q", name)
	}
	link := ""
	if info.Mode()&os.ModeSymlink != 0 {
		link, err = root.Readlink(filepath.FromSlash(name))
		if err != nil {
			return nil, false, fmt.Errorf("read link %q: %w", name, err)
		}
	}
	header, err := tar.FileInfoHeader(info, link)
	if err != nil {
		return nil, false, fmt.Errorf("header %q: %w", name, err)
	}
	header.Name = name
	if info.IsDir() {
		header.Name += "/"
	}
	header.ModTime = time.Unix(0, 0).UTC()
	header.AccessTime = time.Time{}
	header.ChangeTime = time.Time{}
	header.Uid = 0
	header.Gid = 0
	header.Uname = ""
	header.Gname = ""
	header.PAXRecords = nil
	header.Format = tar.FormatPAX
	header.Mode = canonicalMode(info)
	return header, info.Mode().IsRegular(), nil
}

// canonicalMode collapses on-disk permissions so archives do not remember
// the producing machine's umask, keeping only the execute intent.
func canonicalMode(info os.FileInfo) int64 {
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return 0o777
	case info.IsDir() || info.Mode()&0o100 != 0:
		return 0o755
	default:
		return 0o644
	}
}

func writeContents(dst io.Writer, root *os.Root, name string, size int64) error {
	file, err := root.Open(filepath.FromSlash(name))
	if err != nil {
		return fmt.Errorf("open %q: %w", name, err)
	}
	_, copyErr := io.CopyN(dst, file, size)
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("write %q: %w", name, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close %q: %w", name, closeErr)
	}
	return nil
}
