package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write puts one Go source in a fresh directory, so each case gets its own
// package and cannot see another's declarations.
func write(t *testing.T, name, src string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}

	return path
}

func TestRunCatchesEachRule(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		src  string
		want string
	}{{
		name: "doc left behind by a rename",
		src: "package p\n\n" +
			"// do sends one request.\n// request performs one call.\nfunc request() {}\n\n" +
			"func do() {}\n",
		want: `opens by defining "do" but is attached to "request"`,
	}, {
		name: "two docs merged onto one declaration",
		src:  "package p\n\n// waitEmpty polls.\n// waitEmpty waits.\nfunc waitEmpty() {}\n",
		want: `defines "waitEmpty" twice`,
	}, {
		name: "rationale parked on the neighboring constant",
		src: "package p\n\n// readCue polls the overlay.\n// patience is generous.\nconst patience = 1\n\n" +
			"func readCue() {}\n",
		want: `opens by defining "readCue" but is attached to "patience"`,
	}, {
		name: "one test's rationale on another test",
		src: "package p\n\nimport \"testing\"\n\n" +
			"// TestOne checks one.\n// TestTwo checks two.\nfunc TestTwo(t *testing.T) { _ = t }\n\n" +
			"func TestOne(t *testing.T) { _ = t }\n",
		want: `opens by defining "TestOne" but is attached to "TestTwo"`,
	}, {
		name: "decorative separator",
		src:  "package p\n\n// ------------------------\n// Section.\n// ------------------------\n\nfunc f() {}\n",
		want: "delete it — headings live in the package or declaration doc comment",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			err := run([]string{write(t, "a.go", tc.src)}, &out)
			if err == nil {
				t.Fatalf("run accepted %s", tc.name)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("report = %q, want it to contain %q", out.String(), tc.want)
			}
		})
	}
}

func TestRunAcceptsTheShapesThatAreNotDefects(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		src  string
	}{{
		name: "a test names the symbol it exercises",
		src: "package p\n\nimport \"testing\"\n\n" +
			"// Split keeps only the scheme and host.\nfunc TestSplitKeepsScheme(t *testing.T) { _ = t }\n\n" +
			"func Split() {}\n",
	}, {
		name: "a group doc describes the group, not its first spec",
		src:  "package p\n\n// Event kinds the store publishes.\nconst (\n\tEventCreated = 1\n\tEventDropped = 2\n)\n",
	}, {
		name: "a second sentence mentions another symbol",
		src: "package p\n\n// Closer releases a provider.\n// Close is safe to call more than once.\n" +
			"type Closer interface{ Close() error }\n",
	}, {
		name: "a wrapped line happens to begin with a name",
		src: "package p\n\n// f reports whether the queue drained before the\n// deadline elapsed.\nfunc f() {}\n\n" +
			"func deadline() {}\n",
	}, {
		name: "rationale that names no declaration at all",
		src:  "package p\n\n// Four-word cuts destroyed the context MT needs.\nconst maxClause = 8\n",
	}, {
		name: "an ellipsis is not a separator",
		src:  "package p\n\n// and so on ...\nfunc f() {}\n",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			if err := run([]string{write(t, "a.go", tc.src)}, &out); err != nil {
				t.Fatalf("run rejected legitimate prose: %v\n%s", err, out.String())
			}
		})
	}
}

// Not parallel: it changes the working directory, which the rule reads.
func TestToolingTypesBindOnlyUnderHack(t *testing.T) {
	const src = "package main\n\ntype options struct{ n int }\n"

	root := t.TempDir()
	for _, dir := range []string{filepath.Join("hack", "cmd", "tool"), filepath.Join("internal", "thing")} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "main.go"), []byte(src), 0o600); err != nil {
			t.Fatalf("write %s: %v", dir, err)
		}
	}

	t.Chdir(root)

	var out bytes.Buffer
	if err := run([]string{filepath.Join("internal", "thing", "main.go")}, &out); err != nil {
		t.Fatalf("the type rule reached outside %s: %v", toolingRoot, err)
	}

	out.Reset()
	if err := run([]string{filepath.Join("hack", "cmd", "tool", "main.go")}, &out); err == nil {
		t.Fatalf("an undocumented type under %s passed", toolingRoot)
	}
	if !strings.Contains(out.String(), "carries no doc comment") {
		t.Fatalf("report = %q, want the missing-doc message", out.String())
	}
}

func TestRunFailsClosed(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := run(nil, &out); err == nil {
		t.Fatal("run reported success having inspected no files")
	}

	out.Reset()
	if err := run([]string{write(t, "broken.go", "package p\n\nfunc (\n")}, &out); err == nil {
		t.Fatal("run reported success on a file it could not parse")
	}
}

func TestIsSeparator(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{
		"// ----------": true,
		"// ====":       true,
		"// ###":        false,
		"// ...":        false,
		"// -- see #3":  false,
		"// real prose": false,
		"//":            false,
	}

	for text, want := range cases {
		if got := isSeparator(text); got != want {
			t.Errorf("isSeparator(%q) = %v, want %v", text, got, want)
		}
	}
}
