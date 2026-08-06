package main

import (
	"bytes"
	"go/format"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// stage puts one Go source in a fresh directory of its own.
func stage(t *testing.T, src string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "a.go")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	return path
}

func TestRunRejectsPackedLiterals(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"two fields sharing a line": "package p\n\ntype T struct{ A, B int }\n\n" +
			"var v = T{\n\tA: 1, B: 2,\n}\n",
		"a tail packed onto the closing brace": "package p\n\ntype T struct{ A, B, C int }\n\n" +
			"var v = T{\n\tA: 1,\n\tB: 2, C: 3}\n",
		"a nested literal packed inside a clean one": "package p\n\ntype I struct{ X, Y int }\n\n" +
			"type T struct {\n\tA int\n\tB I\n}\n\nvar v = T{\n\tA: 1,\n\tB: I{\n\t\tX: 1, Y: 2,\n\t},\n}\n",
	}

	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			if err := run([]string{stage(t, src)}, &out); err == nil {
				t.Fatalf("run accepted %s:\n%s", name, src)
			}
			if !strings.Contains(out.String(), "give every field its own line") {
				t.Fatalf("report = %q, want the rule's own words", out.String())
			}
		})
	}
}

func TestRunAcceptsTheShapesTheRuleDoesNotBind(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"the whole literal on one line":  "package p\n\ntype T struct{ A, B int }\n\nvar v = T{A: 1, B: 2}\n",
		"one field per line":             "package p\n\ntype T struct{ A, B int }\n\nvar v = T{\n\tA: 1,\n\tB: 2,\n}\n",
		"a positional literal":           "package p\n\nvar v = []int{\n\t1, 2,\n\t3,\n}\n",
		"a table keyed by string":        "package p\n\nvar v = map[string]int{\n\t\"a\": 1, \"b\": 2,\n}\n",
		"a single field over two lines":  "package p\n\ntype T struct{ A string }\n\nvar v = T{\n\tA: \"x\",\n}\n",
		"a first field beside its brace": "package p\n\ntype T struct{ A, B int }\n\nvar v = T{A: 1,\n\tB: 2,\n}\n",
	}

	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			if err := run([]string{stage(t, src)}, &out); err != nil {
				t.Fatalf("run rejected %s: %v\n%s", name, err, out.String())
			}
		})
	}
}

func TestRepairMakesTheTreePassAndStayGofmt(t *testing.T) {
	t.Parallel()

	const src = "package p\n\ntype T struct {\n\tA int\n\tB int\n\tC int\n}\n\n" +
		"var v = T{\n\tA: 1, B: 2,\n\tC: 3}\n"

	path := stage(t, src)

	var out bytes.Buffer
	if err := run([]string{path}, &out); err == nil {
		t.Fatal("the fixture was already accepted; it proves nothing")
	}

	out.Reset()
	if err := run([]string{writeFlag, path}, &out); err != nil {
		t.Fatalf("repair failed: %v\n%s", err, out.String())
	}

	repaired, err := source(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	formatted, err := format.Source(repaired)
	if err != nil {
		t.Fatalf("the repair did not produce parseable Go: %v\n%s", err, repaired)
	}
	if err := os.WriteFile(path, formatted, 0o600); err != nil {
		t.Fatalf("write formatted: %v", err)
	}

	out.Reset()
	if err := run([]string{path}, &out); err != nil {
		t.Fatalf("the repaired file still fails the gate: %v\n%s\n%s", err, out.String(), formatted)
	}
	if strings.Count(string(formatted), "\n\tA: 1") != 1 || strings.Contains(string(formatted), "A: 1, B: 2") {
		t.Fatalf("repair did not separate the fields:\n%s", formatted)
	}
}

func TestRepairKeepsTheFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows carries no permission bits: Chmod there toggles only the read-only flag")
	}
	t.Parallel()

	path := stage(t, "package p\n\ntype T struct{ A, B int }\n\nvar v = T{\n\tA: 1, B: 2,\n}\n")
	const mode = os.FileMode(0o640)
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	var out bytes.Buffer
	if err := run([]string{writeFlag, path}, &out); err != nil {
		t.Fatalf("repair failed: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != mode {
		t.Fatalf("mode after repair = %v, want %v", info.Mode().Perm(), mode)
	}
}

func TestRunFailsClosed(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := run(nil, &out); err == nil {
		t.Fatal("run reported success having inspected no files")
	}

	out.Reset()
	if err := run([]string{writeFlag}, &out); err == nil {
		t.Fatal("a repair run with no files reported success")
	}

	out.Reset()
	if err := run([]string{stage(t, "package p\n\nfunc (\n")}, &out); err == nil {
		t.Fatal("run reported success on a file it could not parse")
	}

	out.Reset()
	if err := run([]string{filepath.Join(t.TempDir(), "absent.go")}, &out); err == nil {
		t.Fatal("run reported success on a file that does not exist")
	}
}
