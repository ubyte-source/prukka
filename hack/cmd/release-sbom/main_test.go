package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateOptions(t *testing.T) {
	t.Parallel()
	valid := testOptions()
	if err := validateOptions(&valid); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.namespaceBase = "http://example.com"
	if err := validateOptions(&invalid); err == nil {
		t.Fatal("validateOptions accepted a non-HTTPS namespace")
	}
}

func testOptions() options {
	return options{
		syftVersion: "v1.44.0", dist: "dist", repo: ".", version: "1.2.3",
		commit: strings.Repeat("a", 40), goVersion: "1.26.5",
		namespaceBase: "https://github.com/ubyte-source/prukka", createdEpoch: 1_700_000_000,
	}
}

func testSPDX(t *testing.T, binary []byte, volatile string) []byte {
	t.Helper()
	digest := sha256.Sum256(binary)
	document := map[string]any{
		"spdxVersion": "SPDX-2.3", "dataLicense": "CC0-1.0", "SPDXID": "SPDXRef-DOCUMENT",
		"name": "volatile", "documentNamespace": "urn:" + volatile,
		"creationInfo": map[string]any{"created": volatile, "creators": []string{"Tool: syft-1.44.0"}},
		"packages":     []any{},
		"files": []spdxFile{{
			Name: "prukka", ID: "SPDXRef-File-prukka",
			Checksums: []spdxChecksum{{Algorithm: checksumSHA256, Value: hex.EncodeToString(digest[:])}},
			License:   "NOASSERTION", LicenseInFile: []string{"NOASSERTION"}, Copyright: "NOASSERTION",
		}},
		"relationships": []any{},
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeTestFile(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, root, name string) string {
	t.Helper()
	directory, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := directory.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	data, err := directory.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
