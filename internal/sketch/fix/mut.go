package fix

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Mut rewrites the borrow `*T` written in a type outside a Go binding
// into the write-in-place mode `mut T` (q169; SIMPLIFY U2, MODES-1). The
// two spellings are one borrow type, so the rewrite changes no checked
// fact and no emitted Go. It is syntactic, so it reads a file that does
// not check, and it is idempotent: its output has no such `*T` but those
// that bind a type parameter (typeArgBorrows), which it leaves for the
// author, since a type parameter binds no borrow. n is the number of
// borrows rewritten.
//
// It also deletes `lent` (MODES-4): `[T lent C]` becomes `[T C]`, and
// each parameter of the declaration whose type is that T, in its
// signature or in a function type or literal inside it, is written
// `x mut T`: a type parameter binds no borrow, so the borrow the caller
// lent (`&t`) is the parameter's mode, and C's operations may write it.
// `[T lent]` becomes `[T]` with its parameters read: T has no operation
// to write through. n counts these rewrites too.
func Mut(src string) (out string, n int, err error) {
	f, err := syntax.Parse(src)
	if err != nil {
		return src, 0, err
	}
	var eds []mutEdit
	for _, at := range borrowStars(f) {
		eds = append(eds, mutEdit{at: int(at), del: 1, ins: "mut "})
	}
	eds = append(eds, lentEdits(f)...)
	if len(eds) == 0 {
		return src, 0, nil
	}
	sort.Slice(eds, func(i, j int) bool { return eds[i].at < eds[j].at })
	out = src
	for i := len(eds) - 1; i >= 0; i-- {
		e := eds[i]
		out = out[:e.at] + e.ins + out[e.at+e.del:]
	}
	if _, err := syntax.Parse(out); err != nil {
		return src, 0, fmt.Errorf("the rewritten source does not parse: %w", err)
	}
	return out, len(eds), nil
}

// mutEdit replaces del bytes at the offset at with ins.
type mutEdit struct {
	at, del int
	ins     string
}

// lentEdits are the edits that delete `lent` from each declaration's type
// parameters and write `mut` before each parameter of such a type
// parameter (Mut).
func lentEdits(f *syntax.File) []mutEdit {
	var eds []mutEdit
	for _, d := range f.Decls {
		fd, ok := d.(*syntax.FuncDecl)
		if !ok || fd.TypeParams == nil || fd.Extern != nil {
			continue
		}
		lent := map[string]bool{}
		for _, field := range fd.TypeParams.List {
			switch id, bare := field.Type.(*syntax.Ident); {
			case field.Lent > 0:
				// [T lent C]: delete `lent ` up to the constraint.
				eds = append(eds, mutEdit{at: int(field.Lent), del: int(field.Type.Pos() - field.Lent)})
			case bare && id.Name == "lent":
				// [T lent]: delete ` lent` after the last name. T has no
				// operation, so the body writes nothing through it: its
				// parameters stay read, and a caller that lent a borrow
				// as T is told to pass the value or write mut there.
				end := field.Names[len(field.Names)-1].End()
				eds = append(eds, mutEdit{at: int(end), del: int(id.End() - end)})
				continue
			default:
				continue
			}
			for _, name := range field.Names {
				lent[name.Name] = true
			}
		}
		if len(lent) == 0 {
			continue
		}
		syntax.Inspect(fd, func(node syntax.Node) bool {
			ft, ok := node.(*syntax.FuncType)
			if !ok || ft.Params == nil {
				return true
			}
			for _, p := range ft.Params.List {
				if id, ok := p.Type.(*syntax.Ident); ok && lent[id.Name] {
					eds = append(eds, mutEdit{at: int(id.Pos()), ins: "mut "})
				}
			}
			return true
		})
	}
	return eds
}

// borrowStars are the positions, in source order, of the `*` of every
// borrow type `*T` written in a type of f outside a Go binding. A type is
// a field's type (a parameter, a result, a record field, an operation) at
// any depth, so a function type's parameter counts and an indirection
// `*p` in a body does not. A Go binding (`extern go …`) keeps `*T`: there
// it is Go's pointer (interop.md).
func borrowStars(f *syntax.File) []syntax.Pos {
	var stars []syntax.Pos
	for _, s := range borrows(f) {
		stars = append(stars, s.Star)
	}
	sort.Slice(stars, func(i, j int) bool { return stars[i] < stars[j] })
	return stars
}

// borrowElems are the types the borrows of borrowStars write after `*`.
func borrowElems(f *syntax.File) []syntax.Expr {
	var xs []syntax.Expr
	for _, s := range borrows(f) {
		xs = append(xs, s.X)
	}
	return xs
}

// borrows are the borrow types `*T` written in a type of f outside a Go
// binding (borrowStars), but for those that bind a type parameter
// (typeArgBorrows).
func borrows(f *syntax.File) []*syntax.StarExpr {
	var stars []*syntax.StarExpr
	for _, s := range typedStars(f) {
		if !s.arg {
			stars = append(stars, s.star)
		}
	}
	return stars
}

// typeArgBorrows are the `*T` written as a type argument, or inside one
// (`Render[*Counter]`, `Option[[]*P]`), outside a function type's
// parameters there. A type parameter binds no borrow, so `mut T` would
// not be the same type: Mut leaves each one and RunMut prints its line.
func typeArgBorrows(f *syntax.File) []*syntax.StarExpr {
	var stars []*syntax.StarExpr
	for _, s := range typedStars(f) {
		if s.arg {
			stars = append(stars, s.star)
		}
	}
	return stars
}

// A typedStar is a `*T` written in a type of a file outside a Go
// binding, and whether it is in a type argument (typeArgBorrows).
type typedStar struct {
	star *syntax.StarExpr
	arg  bool
}

// typedStars are the `*T` written in a type of f outside a Go binding: a
// field's type (a parameter, a result, a record field, an operation) at
// any depth. An indirection `*p` in a body is no type, and is not one.
func typedStars(f *syntax.File) []typedStar {
	var stars []typedStar
	inArg := map[*syntax.StarExpr]bool{}
	args := func(x *syntax.IndexExpr) {
		for _, a := range x.Indices {
			syntax.Inspect(a, func(node syntax.Node) bool {
				switch n := node.(type) {
				case *syntax.FuncType:
					return false // its parameters take modes again
				case *syntax.StarExpr:
					inArg[n] = true
				}
				return true
			})
		}
	}
	inType := func(t syntax.Expr) {
		syntax.Inspect(t, func(node syntax.Node) bool {
			if x, ok := node.(*syntax.IndexExpr); ok {
				args(x)
			}
			if s, ok := node.(*syntax.StarExpr); ok && !s.Mut {
				stars = append(stars, typedStar{s, inArg[s]})
			}
			return true
		})
	}
	syntax.Inspect(f, func(node syntax.Node) bool {
		switch x := node.(type) {
		case *syntax.FuncDecl:
			return x.Extern == nil
		case *syntax.TypeDecl:
			return x.Extern == nil
		case *syntax.VarDecl:
			return x.Extern == nil
		case *syntax.Field:
			if x.Type != nil {
				inType(x.Type)
			}
			return false
		}
		return true
	})
	return stars
}

// keptLines are the lines of src (1-based) where a `*T` binds a type
// parameter (typeArgBorrows), which Mut leaves.
func keptLines(src string) []int {
	f, err := syntax.Parse(src)
	if err != nil {
		return nil
	}
	var lines []int
	for _, s := range typeArgBorrows(f) {
		lines = append(lines, 1+strings.Count(src[:min(int(s.Star), len(src))], "\n"))
	}
	return lines
}

// keptNote is the line RunMut prints for a `*T` it leaves.
const keptNote = "*T binds a type parameter, and a type parameter binds no borrow: not rewritten; pass the value, or take the parameter as mut T"

// MutReport is what RunMut did.
type MutReport struct {
	Files map[string]string // the edited files' new texts
	Edits map[string]int    // per edited file, the borrows rewritten
	Errs  []string          // files the rewrite would break
	Kept  []string          // FILE:LINE of each *T left (typeArgBorrows)
}

// Text is the report's summary: the rewrites, the files and each file
// the rewrite would break.
func (r MutReport) Text() string {
	n := 0
	for _, k := range r.Edits {
		n += k
	}
	if n == 0 && len(r.Errs) == 0 && len(r.Kept) == 0 {
		return ""
	}
	var b strings.Builder
	if n > 0 || len(r.Errs) > 0 {
		fmt.Fprintf(&b, "mut: %d borrows rewritten in %d files\n", n, len(r.Edits))
	}
	for _, e := range r.Errs {
		b.WriteString("not rewritten: " + e + "\n")
	}
	for _, k := range r.Kept {
		b.WriteString("left: " + k + ": " + keptNote + "\n")
	}
	return b.String()
}

// RunMut applies Mut to every .ergo file under opts.Paths, and with
// opts.Go to the Go string literals that parse as an Ergo source and
// write a borrow. It writes the files when opts.Write.
func RunMut(opts Options) (MutReport, error) {
	rep := MutReport{Files: map[string]string{}, Edits: map[string]int{}}
	visit := func(file string) error {
		switch {
		case strings.HasSuffix(file, ".ergo"):
			src, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			for _, l := range keptLines(string(src)) {
				rep.Kept = append(rep.Kept, fmt.Sprintf("%s:%d", file, l))
			}
			out, n, err := Mut(string(src))
			if err != nil {
				if strings.Contains(err.Error(), "rewritten") {
					rep.Errs = append(rep.Errs, file+": "+err.Error())
				}
				return nil
			}
			if n > 0 {
				rep.Files[file], rep.Edits[file] = out, n
			}
		case opts.Go && strings.HasSuffix(file, ".go"):
			out, n, errs, kept, err := mutGo(file)
			if err != nil {
				return err
			}
			rep.Errs = append(rep.Errs, errs...)
			rep.Kept = append(rep.Kept, kept...)
			if n > 0 {
				rep.Files[file], rep.Edits[file] = out, n
			}
		}
		return nil
	}
	for _, p := range opts.Paths {
		err := filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			return visit(path)
		})
		if err != nil {
			return rep, err
		}
	}
	if opts.Write {
		for file, text := range rep.Files {
			info, err := os.Stat(file)
			if err != nil {
				return rep, err
			}
			if err := os.WriteFile(file, []byte(text), info.Mode().Perm()); err != nil {
				return rep, err
			}
		}
	}
	return rep, nil
}

// goSource reports a string that parses as Ergo but is Go source: a borrow
// it would rewrite names a type through a qualifier the text imports no
// package for (`func TestA(t *testing.T) { … }`), or a function returns a
// `*T`, which no Ergo function does. An Ergo source names a
// package's type only after importing it, so no such text checks as Ergo,
// and its `*T` is Go's pointer; mutGo leaves it as it is (MODES-2).
// fileImports are the packages the rest of a generator imports
// (generatorImports), or nil.
func goSource(text string, fileImports map[string]bool) bool {
	f, err := syntax.Parse(text)
	if err != nil {
		return false
	}
	imported := maps.Clone(fileImports)
	if imported == nil {
		imported = map[string]bool{}
	}
	for name := range importsOf(f) {
		imported[name] = true
	}
	foreign := false
	syntax.Inspect(f, func(node syntax.Node) bool {
		// A borrow is only a parameter's or a receiver's type: one
		// written as a result is Go's pointer (`func HostAlarms()
		// *AlarmClock`, a Go source a test edits).
		if ft, ok := node.(*syntax.FuncType); ok && ft.Results != nil {
			for _, r := range ft.Results.List {
				if s, ok := r.Type.(*syntax.StarExpr); ok && !s.Mut {
					foreign = true
				}
			}
		}
		return !foreign
	})
	for _, x := range borrowElems(f) {
		for {
			if ix, ok := x.(*syntax.IndexExpr); ok {
				x = ix.X
				continue
			}
			break
		}
		if sel, ok := x.(*syntax.SelectorExpr); ok {
			if q, ok := sel.X.(*syntax.Ident); ok && q.Name != "go" && !imported[q.Name] {
				foreign = true
			}
		}
	}
	return foreign
}

// importsOf are the names the imports of f bind.
func importsOf(f *syntax.File) map[string]bool {
	imported := map[string]bool{}
	for _, d := range f.Decls {
		id, ok := d.(*syntax.ImportDecl)
		if !ok {
			continue
		}
		for _, sp := range id.Specs {
			switch {
			case sp.Name != nil:
				imported[sp.Name.Name] = true
			case sp.Path != nil:
				p := strings.Trim(sp.Path.Value, "\"`")
				imported[p[strings.LastIndex(p, "/")+1:]] = true
			}
		}
	}
	return imported
}

// ergoImportRE matches an Ergo import line in a generator's piece: Go
// quotes its paths, Ergo does not (`import arena`, `import go net/url`).
var ergoImportRE = regexp.MustCompile(`(?m)^import ((go )?[a-z][a-z0-9_/.]*)\s*$`)

// generatorImports are the packages the Ergo pieces of a generator's
// literals import, by the name they bind. A generator writes its import
// in one piece and a signature naming the package in another
// (`import arena`, then `func take[T](ar *arena.Of[Slot[T]], …)`; irspike
// gen_irinline.go, TestIRInlineMatchesSource): the signature's qualifier
// is imported, so its borrow is Ergo's.
func generatorImports(lits []string) map[string]bool {
	imported := map[string]bool{}
	for _, text := range lits {
		for _, m := range ergoImportRE.FindAllStringSubmatch(text, -1) {
			p := strings.TrimPrefix(m[1], "go ")
			imported[p[strings.LastIndex(p, "/")+1:]] = true
		}
	}
	return imported
}

// mutGo applies Mut to each string literal of a Go file that parses as
// an Ergo source and writes a borrow.
func mutGo(file string) (string, int, []string, []string, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return "", 0, nil, nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.SkipObjectResolution)
	if err != nil {
		return "", 0, nil, nil, nil
	}
	var kept []string
	type rewrite struct {
		from, to int
		text     string
	}
	var rws []rewrite
	n := 0
	var errs []string
	read, closes := fragmentFile(file)
	var fileImports map[string]bool
	if closes {
		// A generator: its pieces import for each other.
		var texts []string
		ast.Inspect(f, func(node ast.Node) bool {
			if bl, ok := node.(*ast.BasicLit); ok && bl.Kind == token.STRING {
				if text, err := strconv.Unquote(bl.Value); err == nil {
					texts = append(texts, text)
				}
			}
			return true
		})
		fileImports = generatorImports(texts)
	}
	ast.Inspect(f, func(node ast.Node) bool {
		bl, ok := node.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		text, err := strconv.Unquote(bl.Value)
		if err != nil || !strings.Contains(text, "*") || goSource(text, fileImports) {
			return true
		}
		for _, l := range keptLines(text) {
			kept = append(kept, fmt.Sprintf("%s:%d", file, fset.Position(bl.ValuePos).Line+l-1))
		}
		out, k, err := Mut(text)
		if err != nil && !strings.Contains(err.Error(), "rewritten") && read {
			// A piece of a source a generator joins, or a fmt format
			// (MutFragment).
			out, k = mutFragment(text, closes || ergoMarker(text), fileImports)
			err = nil
		}
		if err != nil || k == 0 {
			if err != nil && strings.Contains(err.Error(), "rewritten") {
				errs = append(errs, fmt.Sprintf("%s#%d: %v", file, fset.Position(bl.ValuePos).Offset, err))
			}
			return true
		}
		from := fset.Position(bl.ValuePos).Offset
		l := goLit{file: file, from: from, to: from + len(bl.Value), raw: bl.Value[0] == '`'}
		rws = append(rws, rewrite{from, l.to, l.quote(out)})
		n += k
		return true
	})
	out := string(src)
	sort.Slice(rws, func(i, j int) bool { return rws[i].from > rws[j].from })
	for _, r := range rws {
		out = out[:r.from] + r.text + out[r.to:]
	}
	return out, n, errs, kept, nil
}
