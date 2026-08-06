package main

import (
	"slices"
	"strings"
	"testing"
)

func TestReleaseSetsAndChecksumsAreExact(t *testing.T) {
	t.Parallel()
	dist := t.TempDir()
	for index, target := range releaseTargets {
		writeTestFile(t, dist, target.archive, target.name)
		writeTestFile(t, dist, target.name+".spdx.json", `{"documentNamespace":"urn:test:`+string(rune('a'+index))+`"}`)
	}
	if err := verifyArchiveSet(dist); err != nil {
		t.Fatal(err)
	}
	if err := verifySBOMSet(dist); err != nil {
		t.Fatal(err)
	}
	if err := writeChecksums(dist, nil); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(readTestFile(t, dist, "checksums.txt")), "\n")
	names := make([]string, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("invalid checksum line: %q", line)
		}
		names = append(names, fields[1])
	}
	if len(lines) != len(releaseTargets)*2 || !slices.IsSorted(names) {
		t.Fatalf("unexpected checksum manifest: %v", lines)
	}
	writeTestFile(t, dist, "prukka_plan9_amd64.zip", "unexpected")
	if err := verifyArchiveSet(dist); err == nil {
		t.Fatal("verifyArchiveSet accepted an extra archive")
	}
}

func TestWriteChecksumsReusesCachedDigests(t *testing.T) {
	t.Parallel()
	dist := t.TempDir()
	for _, target := range releaseTargets {
		writeTestFile(t, dist, target.archive, target.name)
		writeTestFile(t, dist, target.name+".spdx.json", "{}")
	}
	cached := map[string]string{releaseTargets[0].archive: strings.Repeat("f", 64)}
	if err := writeChecksums(dist, cached); err != nil {
		t.Fatal(err)
	}
	manifest := readTestFile(t, dist, "checksums.txt")
	if !strings.Contains(manifest, strings.Repeat("f", 64)+"  "+releaseTargets[0].archive+"\n") {
		t.Fatal("cached archive digest was not emitted verbatim")
	}
}
