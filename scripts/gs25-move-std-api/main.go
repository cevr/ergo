// Command gs25-move-std-api moves the standard library's Go API out of the
// package that embeds its Ergo sources (gate-speed GS25).
//
// Go recompiles every package that imports a package whose embedded files
// changed. internal/sketch/std embedded the std sources and held their API,
// so a std edit recompiled every importer: check, lower, eval, sketchfront,
// tsproj, rpcgen and bindc, and each of their test variants (37 CPU-s for
// the lower test binary alone after a std comment edit). Afterwards
// internal/sketch/std holds only the embed (embed.go, std.Sources), and
// its API is internal/sketch/stdlib. A std edit then recompiles std and
// stdlib, whose compiled form does not change, and stops there.
//
// The move is a program so that it can be applied again: after a merge
// brings in a lane that still edits the old layout, run it from the module
// root and it moves what remains.
//
//   - every Go file of internal/sketch/std other than embed.go, and its
//     testdata, moves to internal/sketch/stdlib (a file present in both is
//     an error to merge by hand);
//   - each //go:embed line in stdlib, with the embed.FS variable under it,
//     moves to std's embed.go, and stdlib reads std.Sources instead;
//   - stdlib's package clauses become stdlib and stdlib_test;
//   - every other file that imports internal/sketch/std imports
//     internal/sketch/stdlib, and its std.X are stdlib.X. Only stdlib may
//     read std.Sources.
//
// Usage: go run ./scripts/gs25-move-std-api [-check]. With -check it
// changes nothing, prints what it would do, and exits 1 if anything is left.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

const (
	module    = "ergo.local/prototype"
	leafRel   = "internal/sketch/std"
	apiRel    = "internal/sketch/stdlib"
	leafPath  = module + "/" + leafRel
	apiPath   = module + "/" + apiRel
	embedFile = "embed.go"
	sources   = "Sources"
	// ledgerText is the leaf's other embed, the std law ledger: stdlib reads
	// it as it reads Sources.
	ledgerText = "LedgerText"
)

// mover applies the move, or with check only reports it.
type mover struct {
	root    string
	check   bool
	changes []string
}

func main() {
	check := flag.Bool("check", false, "change nothing; exit 1 if anything is left to move")
	flag.Parse()
	m := &mover{root: ".", check: *check}
	if err := m.run(); err != nil {
		fmt.Fprintln(os.Stderr, "gs25-move-std-api:", err)
		os.Exit(2)
	}
	for _, c := range m.changes {
		fmt.Println(c)
	}
	if m.check && len(m.changes) > 0 {
		os.Exit(1)
	}
}

func (m *mover) run() error {
	data, err := os.ReadFile(filepath.Join(m.root, "go.mod"))
	if err != nil || !bytes.Contains(data, []byte("module "+module+"\n")) {
		return fmt.Errorf("run it from the root of module %s", module)
	}
	steps := []func() error{m.moveFiles, m.splitEmbeds, m.renamePackages, m.rewriteImporters}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

func (m *mover) note(format string, args ...any) {
	m.changes = append(m.changes, fmt.Sprintf(format, args...))
}

func (m *mover) write(rel string, data []byte) error {
	if m.check {
		return nil
	}
	return os.WriteFile(filepath.Join(m.root, rel), data, 0o644)
}

// moveFiles moves std's Go files but embed.go, and its testdata, to stdlib.
func (m *mover) moveFiles() error {
	entries, err := os.ReadDir(filepath.Join(m.root, leafRel))
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		switch {
		case e.IsDir() && name == "testdata":
			if err := m.moveTree(filepath.Join(leafRel, name), filepath.Join(apiRel, name)); err != nil {
				return err
			}
		case !e.IsDir() && strings.HasSuffix(name, ".go") && name != embedFile:
			if err := m.move(filepath.Join(leafRel, name), filepath.Join(apiRel, name)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *mover) moveTree(from, to string) error {
	return filepath.WalkDir(filepath.Join(m.root, from), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(filepath.Join(m.root, from), p)
		if err != nil {
			return err
		}
		return m.move(filepath.Join(from, rel), filepath.Join(to, rel))
	})
}

func (m *mover) move(from, to string) error {
	if _, err := os.Stat(filepath.Join(m.root, to)); err == nil {
		return fmt.Errorf("%s and %s both exist: merge them by hand, then remove %s", from, to, from)
	}
	m.note("move %s to %s", from, to)
	if m.check {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(m.root, to)), 0o755); err != nil {
		return err
	}
	return os.Rename(filepath.Join(m.root, from), filepath.Join(m.root, to))
}

// apiFiles are stdlib's Go files, or std's that moveFiles would move under
// -check.
func (m *mover) apiFiles() ([]string, error) {
	var out []string
	for _, dir := range []string{apiRel, leafRel} {
		entries, err := os.ReadDir(filepath.Join(m.root, dir))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") && (dir == apiRel || e.Name() != embedFile) {
				out = append(out, filepath.Join(dir, e.Name()))
			}
		}
	}
	return out, nil
}

// splitEmbeds moves each //go:embed line of stdlib, and the embed.FS
// variable it embeds into, to std's embed.go; stdlib reads std.Sources.
func (m *mover) splitEmbeds() error {
	files, err := m.apiFiles()
	if err != nil {
		return err
	}
	var patterns []string
	for _, rel := range files {
		found, err := m.splitEmbed(rel)
		if err != nil {
			return err
		}
		patterns = append(patterns, found...)
	}
	if len(patterns) == 0 {
		return nil
	}
	leaf := filepath.Join(leafRel, embedFile)
	existing, err := embedPatterns(filepath.Join(m.root, leaf))
	if err != nil {
		return err
	}
	for _, p := range patterns {
		if !slices.Contains(existing, p) {
			existing = append(existing, p)
		}
	}
	m.note("write %s with %d embed lines", leaf, len(existing))
	return m.write(leaf, leafSource(existing))
}

// embedPatterns are the //go:embed lines of an embed.go, or none.
func embedPatterns(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "//go:embed "); ok {
			out = append(out, rest)
		}
	}
	return out, nil
}

func leafSource(patterns []string) []byte {
	var b strings.Builder
	b.WriteString(`// Package std holds the Ergo source of the language's prelude and
// standard library, embedded, and nothing else: its API is
// internal/sketch/stdlib. Go recompiles every importer of a package whose
// embedded files changed, so the importer is only stdlib, whose compiled
// form a std edit does not change; check, lower and the rest import stdlib
// and are not recompiled (gate-speed GS25). scripts/gs25-move-std-api keeps
// it so.
package std

import "embed"

// Sources are the std packages' .ergo files, by path under this directory.
// Only stdlib reads them.
//
`)
	for _, p := range patterns {
		b.WriteString("//go:embed " + p + "\n")
	}
	b.WriteString("var " + sources + " embed.FS\n")
	// The std law ledger lives beside the sources, so the leaf embeds it
	// too; stdlib's Ledger reads it (scripts/stdlaws writes it).
	b.WriteString(`
// LedgerText is the std law ledger (runtime.md, Tests 10), as
// ` + "`go run ./scripts/stdlaws`" + ` writes it. Only stdlib reads it.
//
//go:embed ergo.laws
var LedgerText string
`)
	return []byte(b.String())
}

// splitEmbed removes rel's //go:embed lines and their embed.FS variable,
// makes its uses std.Sources, and returns the lines' patterns.
func (m *mover) splitEmbed(rel string) ([]string, error) {
	path := filepath.Join(m.root, rel)
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !bytes.Contains(src, []byte("//go:embed ")) {
		return nil, nil
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	// The embed variable: a var spec whose doc holds //go:embed lines.
	var spec *ast.ValueSpec
	var decl *ast.GenDecl
	for _, d := range file.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.VAR || g.Doc == nil {
			continue
		}
		for _, c := range g.Doc.List {
			if strings.HasPrefix(c.Text, "//go:embed ") {
				if decl != nil && decl != g {
					return nil, fmt.Errorf("%s: more than one embed variable", rel)
				}
				decl = g
			}
		}
	}
	if decl == nil || len(decl.Specs) != 1 {
		return nil, fmt.Errorf("%s: a //go:embed line that is not on one var declaration", rel)
	}
	spec = decl.Specs[0].(*ast.ValueSpec)
	if len(spec.Names) != 1 {
		return nil, fmt.Errorf("%s: the embed variable declares %d names", rel, len(spec.Names))
	}
	name := spec.Names[0]
	var patterns []string
	for _, c := range decl.Doc.List {
		if rest, ok := strings.CutPrefix(c.Text, "//go:embed "); ok {
			patterns = append(patterns, rest)
		}
	}
	// The edits, as byte ranges replaced: the uses of the variable, then
	// the declaration with its embed lines (from the first embed line).
	type edit struct {
		from, to int
		text     string
	}
	var edits []edit
	ast.Inspect(file, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if ok && id != name && id.Obj != nil && id.Obj == name.Obj {
			edits = append(edits, edit{fset.Position(id.Pos()).Offset, fset.Position(id.End()).Offset, "std." + sources})
		}
		return true
	})
	first := -1
	for _, c := range decl.Doc.List {
		if strings.HasPrefix(c.Text, "//go:embed ") {
			first = fset.Position(c.Pos()).Offset
			break
		}
	}
	end := fset.Position(decl.End()).Offset
	if end < len(src) && src[end] == '\n' {
		end++
	}
	edits = append(edits, edit{first, end, ""})
	// The import: "embed" goes when nothing else uses it; std comes in.
	otherEmbed := false
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if ok && sel.Pos() != spec.Type.Pos() {
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == "embed" {
				otherEmbed = true
			}
		}
		return true
	})
	for _, imp := range file.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == "embed" {
			text := strconv.Quote(leafPath)
			if otherEmbed {
				text = imp.Path.Value + "\n\t" + text
			}
			edits = append(edits, edit{fset.Position(imp.Path.Pos()).Offset, fset.Position(imp.Path.End()).Offset, text})
		}
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].from > edits[j].from })
	out := slices.Clone(src)
	for _, e := range edits {
		out = append(out[:e.from:e.from], append([]byte(e.text), out[e.to:]...)...)
	}
	formatted, err := format.Source(out)
	if err != nil {
		return nil, fmt.Errorf("%s after the split: %v", rel, err)
	}
	m.note("move %d embed lines of %s to %s/%s", len(patterns), rel, leafRel, embedFile)
	return patterns, m.write(rel, formatted)
}

// renamePackages makes stdlib's package clauses stdlib and stdlib_test.
func (m *mover) renamePackages() error {
	files, err := m.apiFiles()
	if err != nil {
		return err
	}
	for _, rel := range files {
		src, err := os.ReadFile(filepath.Join(m.root, rel))
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, rel, src, parser.PackageClauseOnly|parser.ParseComments)
		if err != nil {
			return err
		}
		want := map[string]string{"std": "stdlib", "std_test": "stdlib_test"}[file.Name.Name]
		if want == "" {
			continue
		}
		from, to := fset.Position(file.Name.Pos()).Offset, fset.Position(file.Name.End()).Offset
		out := append(append(slices.Clone(src[:from]), want...), src[to:]...)
		// The package's doc names it.
		out = bytes.Replace(out, []byte("// Package std "), []byte("// Package stdlib "), 1)
		m.note("rename package %s in %s to %s", file.Name.Name, rel, want)
		if err := m.write(rel, out); err != nil {
			return err
		}
	}
	return nil
}

// rewriteImporters makes every importer of std but stdlib's own reader of
// std.Sources import stdlib.
func (m *mover) rewriteImporters() error {
	return filepath.WalkDir(m.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if p != m.root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "node_modules" || name == "bin") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(m.root, p)
		if err != nil {
			return err
		}
		if !strings.HasSuffix(rel, ".go") || filepath.ToSlash(filepath.Dir(rel)) == leafRel {
			return nil
		}
		return m.rewriteImporter(filepath.ToSlash(rel))
	})
}

func (m *mover) rewriteImporter(rel string) error {
	src, err := os.ReadFile(filepath.Join(m.root, rel))
	if err != nil {
		return err
	}
	if !bytes.Contains(src, []byte(strconv.Quote(leafPath))) {
		return nil
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, rel, src, parser.ParseComments)
	if err != nil {
		return err
	}
	var spec *ast.ImportSpec
	for _, imp := range file.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == leafPath {
			spec = imp
		}
	}
	if spec == nil {
		return nil
	}
	name := "std"
	if spec.Name != nil {
		name = spec.Name.Name
	}
	var uses, reads []*ast.Ident
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); ok && x.Name == name && x.Obj == nil {
			if sel.Sel.Name == sources || sel.Sel.Name == ledgerText {
				reads = append(reads, x)
			} else {
				uses = append(uses, x)
			}
		}
		return true
	})
	inAPI := filepath.ToSlash(filepath.Dir(rel)) == apiRel
	switch {
	case len(reads) > 0 && !inAPI:
		return fmt.Errorf("%s reads std.%s: only %s may", rel, sources, apiRel)
	case len(reads) > 0 && len(uses) > 0:
		return fmt.Errorf("%s reads std.%s and uses std's API: split it", rel, sources)
	case len(reads) > 0:
		return nil
	}
	out := slices.Clone(src)
	// Back to front, so each offset holds.
	type edit struct {
		from, to int
		text     string
	}
	edits := []edit{{fset.Position(spec.Path.Pos()).Offset, fset.Position(spec.Path.End()).Offset, strconv.Quote(apiPath)}}
	if spec.Name == nil {
		for _, x := range uses {
			edits = append(edits, edit{fset.Position(x.Pos()).Offset, fset.Position(x.End()).Offset, "stdlib"})
		}
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].from > edits[j].from })
	for _, e := range edits {
		out = append(out[:e.from:e.from], append([]byte(e.text), out[e.to:]...)...)
	}
	formatted, err := format.Source(out)
	if err != nil {
		return fmt.Errorf("%s after the rewrite: %v", rel, err)
	}
	m.note("import %s in %s (%d uses)", apiRel, rel, len(uses))
	return m.write(rel, formatted)
}
