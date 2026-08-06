// Command literal-gate enforces CONTRIBUTING.md's composite-literal rule over
// the Go files named on the command line, test files included: a keyed struct
// literal fits on one line, or gives every field its own line.
//
// -w rewrites the literals it finds instead of reporting them. Its output is
// gofmt INPUT, not gofmt output: it inserts line breaks and leaves the
// indentation to gofmt, which must run after it.
package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const writeFlag = "-w"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "literal-gate: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	repair := len(args) > 0 && args[0] == writeFlag
	paths := args
	if repair {
		paths = paths[1:]
	}
	if len(paths) == 0 {
		return errors.New("no Go files to check")
	}

	var sites []string
	for _, path := range paths {
		found, err := unpack(path, repair)
		if err != nil {
			return err
		}
		sites = append(sites, found...)
	}

	return report(out, len(paths), sites, repair)
}

// report writes the verdict; a repair run lists the same sites without
// failing, having fixed them.
func report(out io.Writer, checked int, sites []string, repair bool) error {
	format := "::error::%s: keyed struct literal packed across lines; give every field its own line"
	if repair {
		format = "%s: unpacked"
	}

	lines := make([]string, 0, len(sites)+1)
	for _, site := range sites {
		lines = append(lines, fmt.Sprintf(format, site))
	}
	if len(sites) == 0 {
		lines = append(lines, fmt.Sprintf(
			"literal gate: %d Go files, every keyed struct literal on one line or one field per line", checked))
	}
	if _, err := io.WriteString(out, strings.Join(lines, "\n")+"\n"); err != nil {
		return fmt.Errorf("write report: %w", err)
	}

	if len(sites) != 0 && !repair {
		return fmt.Errorf("%d packed composite literal(s)", len(sites))
	}

	return nil
}

// unpack names every packed literal in one file and, when repair is set,
// writes the file back with the breaks the rule asks for.
func unpack(path string, repair bool) ([]string, error) {
	src, err := source(path)
	if err != nil {
		return nil, err
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	sites, breaks := packed(fset, file, src)
	if len(sites) == 0 || !repair {
		return sites, nil
	}

	return sites, write(path, insert(src, breaks))
}

// lineBreak is one newline the repair inserts, at a byte offset in the file.
type lineBreak struct {
	text   string
	offset int
}

func packed(fset *token.FileSet, file *ast.File, src []byte) (sites []string, breaks []lineBreak) {
	ast.Inspect(file, func(node ast.Node) bool {
		lit, isLit := node.(*ast.CompositeLit)
		if !isLit {
			return true
		}

		found := litBreaks(fset, lit, src)
		if len(found) == 0 {
			return true
		}

		sites = append(sites, fset.Position(lit.Lbrace).String())
		breaks = append(breaks, found...)

		return true
	})

	return sites, breaks
}

func litBreaks(fset *token.FileSet, lit *ast.CompositeLit, src []byte) []lineBreak {
	if !keyedStruct(lit) || fset.Position(lit.Lbrace).Line == fset.Position(lit.Rbrace).Line {
		return nil
	}

	var breaks []lineBreak
	previous := 0
	for index, element := range lit.Elts {
		at := fset.Position(element.Pos())
		if index > 0 && at.Line == previous {
			breaks = append(breaks, lineBreak{offset: at.Offset, text: "\n"})
		}
		previous = at.Line
	}
	if len(breaks) == 0 {
		return nil
	}

	return append(breaks, closingBreak(fset, lit, src)...)
}

// closingBreak reads the source gap before the brace because the AST records
// no comma, and Go rejects both a missing separator and a doubled one.
func closingBreak(fset *token.FileSet, lit *ast.CompositeLit, src []byte) []lineBreak {
	end := fset.Position(lit.Elts[len(lit.Elts)-1].End())
	brace := fset.Position(lit.Rbrace)
	if end.Line != brace.Line {
		return nil
	}

	text := "\n"
	if !strings.Contains(string(src[end.Offset:brace.Offset]), ",") {
		text = ",\n"
	}

	return []lineBreak{{offset: brace.Offset, text: text}}
}

// keyedStruct binds the rule to two or more keyed fields: a single field
// cannot be packed across lines at all, and an unkeyed literal is positional,
// which is not the shape CONTRIBUTING.md names.
func keyedStruct(lit *ast.CompositeLit) bool {
	if len(lit.Elts) < 2 {
		return false
	}
	for _, element := range lit.Elts {
		pair, isPair := element.(*ast.KeyValueExpr)
		if !isPair {
			return false
		}
		if _, named := pair.Key.(*ast.Ident); !named {
			return false
		}
	}

	return true
}

// insert applies the breaks highest offset first, so every offset still to be
// applied stays valid.
func insert(src []byte, breaks []lineBreak) []byte {
	slices.SortFunc(breaks, func(a, b lineBreak) int { return b.offset - a.offset })

	for _, br := range breaks {
		out := make([]byte, 0, len(src)+len(br.text))
		out = append(out, src[:br.offset]...)
		out = append(out, br.text...)
		src = append(out, src[br.offset:]...)
	}

	return src
}

func source(path string) (src []byte, err error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()

	return root.ReadFile(filepath.Base(path))
}

// write truncates an existing file, keeping its mode; 0o600 applies only to a
// file that vanished between the read and this write.
func write(path string, src []byte) (err error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()

	return root.WriteFile(filepath.Base(path), src, 0o600)
}
