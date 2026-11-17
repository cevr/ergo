package fix

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Mut rewrites the borrow `*T` written in a type outside a Go binding
// into the write-in-place mode `mut T` (q169; SIMPLIFY U2, MODES-1). The
// two spellings are one borrow type, so the rewrite changes no checked
// fact and no emitted Go. It is syntactic, so it reads a file that does
// not check, and it is idempotent: its output has no such `*T`. n is the
// number of borrows rewritten.
func Mut(src string) (out string, n int, err error) {
	f, err := syntax.Parse(src)
	if err != nil {
		return src, 0, err
	}
	stars := borrowStars(f)
	if len(stars) == 0 {
		return src, 0, nil
	}
	out = src
	for i := len(stars) - 1; i >= 0; i-- {
		at := int(stars[i])
		out = out[:at] + "mut " + out[at+1:]
	}
	if _, err := syntax.Parse(out); err != nil {
		return src, 0, fmt.Errorf("the rewritten source does not parse: %w", err)
	}
	return out, len(stars), nil
}

// borrowStars are the positions, in source order, of the `*` of every
// borrow type `*T` written in a type of f outside a Go binding. A type is
// a field's type (a parameter, a result, a record field, an operation) at
// any depth, so a function type's parameter counts and an indirection
// `*p` in a body does not. A Go binding (`extern go …`) keeps `*T`: there
// it is Go's pointer (interop.md).
func borrowStars(f *syntax.File) []syntax.Pos {
	var stars []syntax.Pos
	inType := func(t syntax.Expr) {
		syntax.Inspect(t, func(node syntax.Node) bool {
			if s, ok := node.(*syntax.StarExpr); ok && !s.Mut {
				stars = append(stars, s.Star)
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
	sort.Slice(stars, func(i, j int) bool { return stars[i] < stars[j] })
	return stars
}

// MutReport is what RunMut did.
type MutReport struct {
	Files map[string]string // the edited files' new texts
	Edits map[string]int    // per edited file, the borrows rewritten
	Errs  []string          // files the rewrite would break
}

// Text is the report's summary: the rewrites, the files and each file
// the rewrite would break.
func (r MutReport) Text() string {
	n := 0
	for _, k := range r.Edits {
		n += k
	}
	if n == 0 && len(r.Errs) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "mut: %d borrows rewritten in %d files\n", n, len(r.Edits))
	for _, e := range r.Errs {
		b.WriteString("not rewritten: " + e + "\n")
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
			out, n, errs, err := mutGo(file)
			if err != nil {
				return err
			}
			rep.Errs = append(rep.Errs, errs...)
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

// mutGo applies Mut to each string literal of a Go file that parses as
// an Ergo source and writes a borrow.
func mutGo(file string) (string, int, []string, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return "", 0, nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.SkipObjectResolution)
	if err != nil {
		return "", 0, nil, nil
	}
	type rewrite struct {
		from, to int
		text     string
	}
	var rws []rewrite
	n := 0
	var errs []string
	ast.Inspect(f, func(node ast.Node) bool {
		bl, ok := node.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		text, err := strconv.Unquote(bl.Value)
		if err != nil || !strings.Contains(text, "*") {
			return true
		}
		out, k, err := Mut(text)
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
	return out, n, errs, nil
}
