// Command engine-catalog scans a directory of built engine release artifacts
// (per-platform runtime archives, model-pack archives and their metadata) and
// emits the validated prukka-engine-catalog.json the daemon consumes to
// discover, download and checksum-verify engine assets.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ubyte-source/prukka/internal/speech"
	"github.com/ubyte-source/prukka/internal/strictjson"
)

// Artifact names are a fixed contract with the publishing workflow: anything
// in the scanned directory matching none of these patterns is a mistake.
var (
	runtimeNamePattern = regexp.MustCompile(`^prukka-engine-runtime_([a-z0-9]+)_([a-z0-9]+)\.tar\.gz$`)
	packNamePattern    = regexp.MustCompile(`^prukka-engine-pack_([a-z][a-z0-9-]*)\.tar\.gz$`)
	metaNamePattern    = regexp.MustCompile(`^([a-z][a-z0-9-]*)\.meta\.json$`)
)

type options struct {
	dir      string
	baseURL  string
	output   string
	protocol int
}

type packMeta struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	Lang    string `json:"lang,omitempty"`
	Voice   string `json:"voice,omitempty"`
	License string `json:"license,omitempty"`
}

// inventory is the classified content of the artifact directory, keyed by
// pack id where applicable.
type inventory struct {
	packTars map[string]string
	metas    map[string]string
	runtimes []string
}

func main() {
	var opts options
	flag.StringVar(&opts.dir, "dir", "", "directory holding the built release artifacts")
	flag.StringVar(&opts.baseURL, "base-url", "", "URL prefix the published assets are downloaded from")
	flag.IntVar(&opts.protocol, "protocol", 0, "engine protocol version the artifacts implement")
	flag.StringVar(&opts.output, "output", "", "output catalog path")
	flag.Parse()

	if err := run(opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(opts options) error {
	if opts.dir == "" || opts.baseURL == "" || opts.output == "" || opts.protocol <= 0 {
		return errors.New("dir, base-url, output, and a positive protocol are required")
	}
	root, err := os.OpenRoot(opts.dir)
	if err != nil {
		return fmt.Errorf("open artifact directory: %w", err)
	}
	doc, buildErr := buildCatalog(root, opts)
	if joined := errors.Join(buildErr, root.Close()); joined != nil {
		return joined
	}
	return writeCatalog(opts.output, doc)
}

func buildCatalog(root *os.Root, opts options) (*speech.Catalog, error) {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("read artifact directory: %w", err)
	}
	found, err := classify(entries)
	if err != nil {
		return nil, err
	}
	runtimes, err := runtimeEntries(root, found.runtimes, opts.baseURL)
	if err != nil {
		return nil, err
	}
	packs, err := packEntries(root, found, opts.baseURL)
	if err != nil {
		return nil, err
	}
	return &speech.Catalog{
		Schema:   speech.CatalogSchema,
		Version:  speech.CatalogVersion,
		Protocol: opts.protocol,
		Runtimes: runtimes,
		Packs:    packs,
	}, nil
}

func classify(entries []fs.DirEntry) (inventory, error) {
	found := inventory{packTars: map[string]string{}, metas: map[string]string{}}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			return inventory{}, fmt.Errorf("unexpected directory %q in the artifact directory", name)
		}
		switch {
		case runtimeNamePattern.MatchString(name):
			found.runtimes = append(found.runtimes, name)
		case packNamePattern.MatchString(name):
			found.packTars[packNamePattern.FindStringSubmatch(name)[1]] = name
		case metaNamePattern.MatchString(name):
			found.metas[metaNamePattern.FindStringSubmatch(name)[1]] = name
		default:
			return inventory{}, fmt.Errorf("unexpected file %q in the artifact directory", name)
		}
	}
	return found, checkComplete(found)
}

// checkComplete refuses partial artifact sets: a pack archive without its
// metadata (or the reverse) means an upload or a spelling went wrong, and an
// empty side means a whole build job's output is missing.
func checkComplete(found inventory) error {
	if len(found.runtimes) == 0 {
		return errors.New("no prukka-engine-runtime_<os>_<arch>.tar.gz archives found")
	}
	if len(found.packTars) == 0 {
		return errors.New("no prukka-engine-pack_<id>.tar.gz archives found")
	}
	for _, id := range slices.Sorted(maps.Keys(found.packTars)) {
		if _, ok := found.metas[id]; !ok {
			return fmt.Errorf("pack archive %q has no %s.meta.json", found.packTars[id], id)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(found.metas)) {
		if _, ok := found.packTars[id]; !ok {
			return fmt.Errorf("metadata %q has no prukka-engine-pack_%s.tar.gz archive", found.metas[id], id)
		}
	}
	return nil
}

func runtimeEntries(root *os.Root, names []string, baseURL string) ([]speech.Runtime, error) {
	entries := make([]speech.Runtime, 0, len(names))
	for _, name := range names {
		match := runtimeNamePattern.FindStringSubmatch(name)
		sum, size, err := digest(root, name)
		if err != nil {
			return nil, err
		}
		entries = append(entries, speech.Runtime{
			OS:     match[1],
			Arch:   match[2],
			URL:    assetURL(baseURL, name),
			SHA256: sum,
			Size:   size,
		})
	}
	slices.SortFunc(entries, func(a, b speech.Runtime) int {
		if byOS := strings.Compare(a.OS, b.OS); byOS != 0 {
			return byOS
		}
		return strings.Compare(a.Arch, b.Arch)
	})
	return entries, nil
}

func packEntries(root *os.Root, found inventory, baseURL string) ([]speech.Pack, error) {
	ids := slices.Sorted(maps.Keys(found.packTars))
	entries := make([]speech.Pack, 0, len(ids))
	for _, id := range ids {
		meta, err := readMeta(root, found.metas[id], id)
		if err != nil {
			return nil, err
		}
		name := found.packTars[id]
		sum, size, err := digest(root, name)
		if err != nil {
			return nil, err
		}
		entries = append(entries, speech.Pack{
			ID:      id,
			Kind:    meta.Kind,
			From:    meta.From,
			To:      meta.To,
			Lang:    meta.Lang,
			Voice:   meta.Voice,
			URL:     assetURL(baseURL, name),
			SHA256:  sum,
			Size:    size,
			License: meta.License,
		})
	}
	return entries, nil
}

func readMeta(root *os.Root, name, id string) (*packMeta, error) {
	data, err := root.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", name, err)
	}
	meta := &packMeta{}
	if decodeErr := strictjson.Decode(data, meta); decodeErr != nil {
		return nil, fmt.Errorf("parse %q: %w", name, decodeErr)
	}
	return meta, validateMeta(meta, name, id)
}

// requiredFields maps a pack kind to the descriptor fields it must carry;
// the second result reports whether the kind exists at all.
func requiredFields(kind string) ([]string, bool) {
	switch kind {
	case "stt":
		return nil, true
	case "mt":
		return []string{"from", "to"}, true
	case "voice":
		return []string{"lang", "voice"}, true
	default:
		return nil, false
	}
}

func validateMeta(meta *packMeta, name, id string) error {
	if meta.ID != id {
		return fmt.Errorf("%s: id %q does not match the file name", name, meta.ID)
	}
	required, known := requiredFields(meta.Kind)
	if !known {
		return fmt.Errorf("%s: unknown pack kind %q", name, meta.Kind)
	}
	fields := map[string]string{"from": meta.From, "to": meta.To, "lang": meta.Lang, "voice": meta.Voice}
	for _, field := range required {
		if fields[field] == "" {
			return fmt.Errorf("%s: %s packs need a %s value", name, meta.Kind, field)
		}
		delete(fields, field)
	}
	for _, field := range slices.Sorted(maps.Keys(fields)) {
		if fields[field] != "" {
			return fmt.Errorf("%s: %s packs must not set %s", name, meta.Kind, field)
		}
	}
	return nil
}

func digest(root *os.Root, name string) (sum string, size int64, err error) {
	file, err := root.Open(name)
	if err != nil {
		return "", 0, fmt.Errorf("open %q: %w", name, err)
	}
	hasher := sha256.New()
	size, copyErr := io.Copy(hasher, file)
	closeErr := file.Close()
	if hashErr := errors.Join(copyErr, closeErr); hashErr != nil {
		return "", 0, fmt.Errorf("hash %q: %w", name, hashErr)
	}
	return hex.EncodeToString(hasher.Sum(nil)), size, nil
}

func assetURL(baseURL, name string) string {
	return strings.TrimSuffix(baseURL, "/") + "/" + name
}

func writeCatalog(output string, doc *speech.Catalog) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode catalog: %w", err)
	}
	data = append(data, '\n')

	// The daemon's strict parser is the contract for its own protocol; a
	// catalog built for another protocol version is legitimately rejected by
	// THIS daemon and is validated structurally by construction instead.
	if doc.Protocol == speech.SupportedProtocol {
		if _, parseErr := speech.ParseCatalog(bytes.NewReader(data)); parseErr != nil {
			return fmt.Errorf("built catalog fails the daemon's parser: %w", parseErr)
		}
	}
	if writeErr := os.WriteFile(filepath.Clean(output), data, 0o600); writeErr != nil {
		return fmt.Errorf("write catalog: %w", writeErr)
	}
	return nil
}
