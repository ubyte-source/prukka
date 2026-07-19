// Shared fixtures for the speech installer tests. This file defines no Test
// functions by mapping-gate contract.

package speech

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// bundleManifestJSON is the engine bundle declaration fixtures ship.
const bundleManifestJSON = `{"schema":"prukka.engine.bundle","version":2,"kind":"native"}`

// tarEntry describes one member of an in-memory test archive.
type tarEntry struct {
	name string
	link string
	body []byte
	mode int64
	dir  bool
}

// tarGz renders a gzip'd tar with the given entries.
func tarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		header := &tar.Header{Name: e.name, Mode: e.mode}
		switch {
		case e.dir:
			header.Typeflag = tar.TypeDir
		case e.link != "":
			header.Typeflag = tar.TypeSymlink
			header.Linkname = e.link
		default:
			header.Typeflag = tar.TypeReg
			header.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatalf("write header %s: %v", e.name, err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := tw.Write(e.body); err != nil {
				t.Fatalf("write body %s: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}

	return buf.Bytes()
}

// nativeHelperEntries lists the compiled tools a runtime archive must carry so
// nativeHelpersPresent accepts the bundle on every OS (the production
// nativeHelpers list, .exe-suffixed on Windows). No orchestrator binary: the
// daemon is the orchestrator.
func nativeHelperEntries() []tarEntry {
	entries := make([]tarEntry, 0, len(nativeHelpers()))
	for _, helper := range nativeHelpers() {
		entries = append(entries, tarEntry{name: filepath.ToSlash(helper), body: []byte("tool"), mode: 0o755})
	}

	return entries
}

// runtimeArchive is a minimal valid runtime bundle archive.
func runtimeArchive(t *testing.T) []byte {
	t.Helper()

	entries := append([]tarEntry{
		{name: "prukka-engine-manifest.json", body: []byte(bundleManifestJSON), mode: 0o644},
		{name: "lib", dir: true, mode: 0o755},
		{name: "lib/libwhisper.dylib", body: []byte("lib"), mode: 0o644},
	}, nativeHelperEntries()...)

	return tarGz(t, entries)
}

// packArchive is a minimal valid model pack archive.
func packArchive(t *testing.T, paths ...string) []byte {
	t.Helper()

	entries := make([]tarEntry, 0, len(paths))
	for _, p := range paths {
		entries = append(entries, tarEntry{name: p, body: []byte("model-bytes"), mode: 0o644})
	}

	return tarGz(t, entries)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

// artifactServer serves named blobs over loopback HTTP and counts requests.
type artifactServer struct {
	server *httptest.Server
	blobs  map[string][]byte
	hits   map[string]int
}

func newArtifactServer(t *testing.T) *artifactServer {
	t.Helper()

	s := &artifactServer{blobs: map[string][]byte{}, hits: map[string]int{}}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		blob, ok := s.blobs[r.URL.Path]
		if !ok {
			http.NotFound(w, r)

			return
		}
		s.hits[r.URL.Path]++
		if _, err := w.Write(blob); err != nil {
			return
		}
	}))
	t.Cleanup(s.server.Close)

	return s
}

func (s *artifactServer) add(path string, blob []byte) (url, sha string, size int64) {
	s.blobs[path] = blob

	return s.server.URL + path, sha256Hex(blob), int64(len(blob))
}

// catalogDoc renders a valid catalog document for the current platform with
// the given runtime and pack blobs already registered on the server.
func catalogDoc(t *testing.T, s *artifactServer, goos, goarch string, runtime []byte, packs map[string][]byte) []byte {
	t.Helper()

	rtURL, rtSHA, rtSize := s.add("/runtime.tar.gz", runtime)
	doc := map[string]any{
		"schema":   "prukka.engine.catalog",
		"version":  1,
		"protocol": 2,
		"runtimes": []map[string]any{{"os": goos, "arch": goarch, "url": rtURL, "sha256": rtSHA, "size": rtSize}},
	}
	packList := make([]map[string]any, 0, len(packs))
	for id, blob := range packs {
		url, sha, size := s.add("/"+id+".tar.gz", blob)
		entry := map[string]any{"id": id, "url": url, "sha256": sha, "size": size}
		decoratePackEntry(entry, id)
		packList = append(packList, entry)
	}
	doc["packs"] = packList

	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("render catalog: %v", err)
	}

	return raw
}

// decoratePackEntry fills the kind-specific fields the canonical ids imply.
func decoratePackEntry(entry map[string]any, id string) {
	switch {
	case id == PackIDSTTCore:
		entry["kind"] = PackSTT
	case len(id) > 3 && id[:3] == "mt-":
		entry["kind"] = PackMT
		entry["from"] = id[3:5]
		entry["to"] = id[6:8]
	default:
		entry["kind"] = PackVoice
		lang := id[len("voice-"):]
		entry["lang"] = lang
		entry["voice"] = fmt.Sprintf("models/tts/%s_test-voice.onnx", lang)
	}
}
