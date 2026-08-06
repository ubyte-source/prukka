// Command comment-gate enforces the Engineering Constitution's comment rules
// over the Go files named on the command line: a doc comment may not open by
// defining another declaration nor define its own twice, no decorative
// separators, and every type under hack/ carries a doc comment.
//
// The rules bind test files, which the pinned linter cannot reach:
// golangci-lint's godoclint integration hardcodes StartWithNameIncludeTests
// to false.
package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "comment-gate: %v\n", err)
		os.Exit(1)
	}
}

// run reports success only having inspected at least one file, so a caller
// whose file list came out empty fails here instead of passing.
func run(paths []string, out io.Writer) error {
	if len(paths) == 0 {
		return errors.New("no Go files to check")
	}

	a := &analyzer{fset: token.NewFileSet(), files: make(map[string]*ast.File, len(paths))}
	for _, path := range paths {
		if err := a.parse(path); err != nil {
			return err
		}
	}

	findings := a.check()
	lines := make([]string, 0, len(findings)+1)
	for _, f := range findings {
		lines = append(lines, fmt.Sprintf("::error::%s: %s", f.pos, f.text))
	}
	if len(findings) == 0 {
		lines = append(lines, fmt.Sprintf(
			"comment gate: %d Go files, no misplaced doc comments, no separators, every hack/ type documented",
			len(paths)))
	}

	if _, err := io.WriteString(out, strings.Join(lines, "\n")+"\n"); err != nil {
		return fmt.Errorf("write report: %w", err)
	}

	if len(findings) != 0 {
		return fmt.Errorf("%d comment-rule violation(s)", len(findings))
	}

	return nil
}

// finding is one rule violation and the position an editor can jump to.
type finding struct {
	text string
	pos  token.Position
}

// analyzer holds the parsed tree plus, per package directory, the top-level
// names that directory declares.
type analyzer struct {
	fset  *token.FileSet
	files map[string]*ast.File
	names map[string]map[string]bool
}

// parse reads one file; a parse error fails the gate rather than skipping the
// file, which would report it clean.
func (a *analyzer) parse(path string) error {
	file, err := parser.ParseFile(a.fset, path, nil, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	a.files[path] = file
	if a.names == nil {
		a.names = map[string]map[string]bool{}
	}

	dir := filepath.Dir(path)
	if a.names[dir] == nil {
		a.names[dir] = map[string]bool{}
	}
	for _, d := range declared(file) {
		a.names[dir][d.name] = true
	}

	return nil
}

func (a *analyzer) check() []finding {
	var out []finding
	for _, path := range slices.Sorted(maps.Keys(a.files)) {
		file := a.files[path]
		names := a.names[filepath.Dir(path)]
		for _, d := range declared(file) {
			if d.doc == nil {
				continue
			}
			out = append(out, a.checkDoc(d, names)...)
		}
		out = append(out, a.separators(file)...)
		out = append(out, a.toolingTypes(path, file)...)
	}

	return out
}

func (a *analyzer) checkDoc(d decl, names map[string]bool) []finding {
	var out []finding
	own := 0
	for _, o := range openers(d.doc, names) {
		switch {
		case o.word == d.name:
			own++
			if own > 1 {
				out = append(out, finding{
					pos:  a.fset.Position(d.doc.Pos()),
					text: fmt.Sprintf("doc comment defines %q twice; two docs were merged, keep one", d.name),
				})
			}
		case o.line == 0 && !d.group && !subjectUnderTest(d.name, o.word):
			out = append(out, finding{
				pos: a.fset.Position(d.doc.Pos()),
				text: fmt.Sprintf("doc comment opens by defining %q but is attached to %q; move it to %[1]s",
					o.word, d.name),
			})
		}
	}

	return out
}

func (a *analyzer) separators(file *ast.File) []finding {
	var out []finding
	for _, group := range file.Comments {
		for _, c := range group.List {
			if !isSeparator(c.Text) {
				continue
			}
			out = append(out, finding{
				pos:  a.fset.Position(c.Pos()),
				text: "decorative separator; delete it — headings live in the package or declaration doc comment",
			})
		}
	}

	return out
}

// decl is one top-level declaration and the doc comment attached to it, with
// group set when that doc describes a parenthesized group rather than a name.
type decl struct {
	doc   *ast.CommentGroup
	name  string
	group bool
}

func declared(file *ast.File) []decl {
	var out []decl
	for _, d := range file.Decls {
		switch n := d.(type) {
		case *ast.FuncDecl:
			out = append(out, decl{name: n.Name.Name, doc: n.Doc})
		case *ast.GenDecl:
			if n.Tok != token.IMPORT {
				out = append(out, genDecls(n)...)
			}
		}
	}

	return out
}

func genDecls(n *ast.GenDecl) []decl {
	var out []decl
	first := ""
	for _, s := range n.Specs {
		for _, name := range specNames(s) {
			if first == "" {
				first = name
			}
			out = append(out, decl{name: name, doc: specDoc(s, name, first)})
		}
	}
	if n.Doc != nil && first != "" {
		out = append(out, decl{name: first, doc: n.Doc, group: len(n.Specs) > 1})
	}

	return out
}

func specNames(s ast.Spec) []string {
	switch sp := s.(type) {
	case *ast.TypeSpec:
		return []string{sp.Name.Name}
	case *ast.ValueSpec:
		out := make([]string, 0, len(sp.Names))
		for _, id := range sp.Names {
			out = append(out, id.Name)
		}

		return out
	default:
		return nil
	}
}

// specDoc returns a spec's doc only for the first name it declares:
// `var a, b = ...` documents the pair once, not each half.
func specDoc(s ast.Spec, name, first string) *ast.CommentGroup {
	if name != first {
		return nil
	}
	switch sp := s.(type) {
	case *ast.TypeSpec:
		return sp.Doc
	case *ast.ValueSpec:
		return sp.Doc
	default:
		return nil
	}
}

// opener is a declared name found where a sentence begins, which is where a
// doc comment defines something rather than merely mentioning it.
type opener struct {
	word string
	line int
}

func openers(doc *ast.CommentGroup, names map[string]bool) []opener {
	var out []opener
	lines := strings.Split(strings.TrimRight(doc.Text(), "\n"), "\n")
	for i, line := range lines {
		if i > 0 && !endsSentence(lines[i-1]) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if word := strings.TrimRight(fields[0], ".,:;"); names[word] {
			out = append(out, opener{line: i, word: word})
		}
	}

	return out
}

// endsSentence decodes the last rune rather than the last byte because this
// repository's prose ends lines on em dashes, which continue a sentence.
func endsSentence(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return true
	}
	last, _ := utf8.DecodeLastRuneInString(trimmed)

	return strings.ContainsRune(".:!?", last)
}

// subjectUnderTest allows the one doc that may open with another name: a test
// naming the symbol it exercises, the way ExampleSplit opens with Split.
func subjectUnderTest(attached, word string) bool {
	return isTestName(attached) && !isTestName(word)
}

func isTestName(name string) bool {
	for _, prefix := range [...]string{"Test", "Benchmark", "Fuzz", "Example"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}

	return false
}

// isSeparator matches four or more repetitions of one punctuation mark;
// three is left alone so an ellipsis in prose is not a violation.
func isSeparator(text string) bool {
	body := strings.TrimSpace(strings.Trim(strings.TrimPrefix(text, "//"), "/*"))
	if len(body) < 4 {
		return false
	}
	if !strings.ContainsRune("-=*_~#+", rune(body[0])) {
		return false
	}

	return strings.Count(body, string(body[0])) == len(body)
}

// toolingRoot scopes the type-doc rule: everything under it is `package main`,
// so revive's `exported` rule finds no exported identifier to bind to and this
// gate is the only thing that can require a doc comment there.
const toolingRoot = "hack" + string(filepath.Separator)

// toolingTypes accepts a doc on a single-spec group as the type's own, because
// go/ast attaches the comment above an unparenthesized `type T struct` to the
// GenDecl rather than to the TypeSpec.
func (a *analyzer) toolingTypes(path string, file *ast.File) []finding {
	if !strings.HasPrefix(path, toolingRoot) {
		return nil
	}

	var out []finding
	for _, d := range file.Decls {
		n, ok := d.(*ast.GenDecl)
		if !ok || n.Tok != token.TYPE {
			continue
		}
		for _, s := range n.Specs {
			sp, isType := s.(*ast.TypeSpec)
			if !isType || sp.Doc != nil || (n.Doc != nil && len(n.Specs) == 1) {
				continue
			}
			out = append(out, finding{
				pos:  a.fset.Position(sp.Pos()),
				text: fmt.Sprintf("type %s carries no doc comment; %s* documents its types", sp.Name.Name, toolingRoot),
			})
		}
	}

	return out
}
