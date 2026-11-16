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

// Modes rewrites the old spellings of the keep mode in one Ergo source
// into the mode word own (audit CSX §4.3 and §4.10, unit EXM1; q168). It
// is syntactic, so it reads a file that does not check, and it changes no
// checked fact:
//
//   - `takes a, b` after a signature becomes `own` before the type of a
//     and of b. A group whose names are not all taken is split, keeping
//     the order: `a, b File takes a` becomes `a own File, b File`.
//   - A Go binding's `takes r`, naming its receiver, becomes
//     `(r own *T)`.
//   - A bare `takes` on an operation becomes `own` before the operation.
//   - `escaping func(…)` as a parameter's type becomes `own func(…)`.
//   - `escaping` anywhere else (a field's or a result's function type,
//     already escaping) is deleted.
//
// A name in a takes clause that names no parameter is dropped and counted
// in dropped: the clause gave it no fact. Modes is idempotent: its output
// has no takes clause and no parameter's escaping.
func Modes(src string) (out string, n, dropped int, err error) {
	f, err := syntax.Parse(src)
	if err != nil {
		return src, 0, 0, err
	}
	var eds []edit
	seen := map[*syntax.FuncType]bool{}
	asParam := map[*syntax.FuncType]bool{} // a parameter's escaping, rewritten to own
	// params rewrites a parameter list's escaping: a function's, a
	// function type's, a law's or a test's.
	params := func(l *syntax.FieldList) {
		if l != nil {
			for _, field := range l.List {
				x, ok := unparen(field.Type).(*syntax.FuncType)
				if !ok || !x.Escaping.IsValid() {
					continue
				}
				asParam[x] = true
				if field.Own == nil {
					eds = append(eds, edit{from: int(x.Escaping), to: int(x.Escaping) + len("escaping"), text: "own"})
				} else {
					eds = append(eds, edit{from: int(x.Escaping), to: int(x.Func), text: ""})
				}
				n++
			}
		}
	}
	sig := func(ft *syntax.FuncType, recv *syntax.FieldList, member *syntax.Field) {
		if seen[ft] {
			return
		}
		seen[ft] = true
		params(ft.Params)
		u := ft.Takes
		if u == nil {
			return
		}
		n++
		eds = append(eds, edit{from: int(clauseStart(ft)), to: int(u.End()), text: ""})
		if len(u.Names) == 0 {
			if member != nil && member.Own == nil {
				eds = append(eds, edit{from: int(member.Names[0].Pos()), to: int(member.Names[0].Pos()), text: "own "})
			}
			return
		}
		taken := map[string]bool{}
		for _, id := range u.Names {
			taken[id.Name] = true
		}
		found := map[string]bool{}
		lists := []*syntax.FieldList{recv, ft.Params}
		for _, l := range lists {
			if l == nil {
				continue
			}
			for _, field := range l.List {
				if e, ok := ownField(src, field, taken, found); ok {
					eds = append(eds, e)
				}
			}
		}
		for _, id := range u.Names {
			if !found[id.Name] {
				dropped++
			}
		}
	}
	syntax.Inspect(f, func(node syntax.Node) bool {
		switch x := node.(type) {
		case *syntax.FuncDecl:
			sig(x.Type, x.Recv, nil)
		case *syntax.InterfaceType:
			for _, m := range x.Methods {
				if ft, ok := m.Type.(*syntax.FuncType); ok {
					sig(ft, nil, m)
				}
			}
		case *syntax.FuncType:
			sig(x, nil, nil)
		case *syntax.LawDecl:
			params(x.Params)
		case *syntax.TestDecl:
			params(x.Params)
		}
		return true
	})
	syntax.Inspect(f, func(node syntax.Node) bool {
		if x, ok := node.(*syntax.FuncType); ok && x.Escaping.IsValid() && !asParam[x] {
			eds = append(eds, edit{from: int(x.Escaping), to: int(x.Func), text: ""})
			n++
		}
		return true
	})
	if len(eds) == 0 {
		return src, 0, dropped, nil
	}
	sort.SliceStable(eds, func(i, j int) bool { return eds[i].from > eds[j].from })
	out = src
	for _, e := range eds {
		out = out[:e.from] + e.text + out[e.to:]
	}
	if _, err := syntax.Parse(out); err != nil {
		return src, 0, dropped, fmt.Errorf("the rewritten source does not parse: %w", err)
	}
	return out, n, dropped, nil
}

// clauseStart is where ft's takes clause begins, blanks before it
// included: the end of what precedes it.
func clauseStart(ft *syntax.FuncType) syntax.Pos {
	switch {
	case ft.Needs != nil:
		return ft.Needs.End()
	case ft.Results != nil:
		return ft.Results.End()
	}
	return ft.Params.End()
}

// ownField is the edit that writes own on the taken names of field, and
// records each in found. A group whose names are all taken gets the
// word before its type; a group taken in part is respelled name by name
// runs, keeping the order.
func ownField(src string, field *syntax.Field, taken, found map[string]bool) (edit, bool) {
	if field.Type == nil || len(field.Names) == 0 || field.Own != nil {
		return edit{}, false
	}
	all, any := true, false
	for _, id := range field.Names {
		if taken[id.Name] {
			found[id.Name] = true
			any = true
		} else {
			all = false
		}
	}
	if !any {
		return edit{}, false
	}
	at := int(field.Type.Pos())
	if all {
		return edit{from: at, to: at, text: "own "}, true
	}
	typ := src[field.Type.Pos():field.Type.End()]
	var parts []string
	var run []string
	runOwn := taken[field.Names[0].Name]
	flush := func() {
		mode := ""
		if runOwn {
			mode = "own "
		}
		parts = append(parts, strings.Join(run, ", ")+" "+mode+typ)
		run = nil
	}
	for _, id := range field.Names {
		if taken[id.Name] != runOwn {
			flush()
			runOwn = taken[id.Name]
		}
		run = append(run, id.Name)
	}
	flush()
	return edit{from: int(field.Names[0].Pos()), to: int(field.Type.End()), text: strings.Join(parts, ", ")}, true
}

// ModeReport is what RunModes did.
type ModeReport struct {
	Files   map[string]string // the edited files' new texts
	Edits   map[string]int    // per edited file, the rewrites
	Dropped int               // takes names that named no parameter
	Errs    []string          // files that did not parse after the rewrite
}

// Text is the report's summary: the rewrites, the files, the dropped
// names and each file the rewrite would break.
func (r ModeReport) Text() string {
	n := 0
	for _, k := range r.Edits {
		n += k
	}
	var b strings.Builder
	fmt.Fprintf(&b, "own: %d rewrites in %d files", n, len(r.Edits))
	if r.Dropped > 0 {
		fmt.Fprintf(&b, "; %d takes names named no parameter and were dropped", r.Dropped)
	}
	b.WriteString("\n")
	for _, e := range r.Errs {
		b.WriteString("not rewritten: " + e + "\n")
	}
	return b.String()
}

// RunModes applies Modes to every .ergo file under opts.Paths, generated
// bindings (gostd) included, and with opts.Go to the Go string literals
// that parse as an Ergo source and spell an old mode. It writes the files
// when opts.Write.
func RunModes(opts Options) (ModeReport, error) {
	rep := ModeReport{Files: map[string]string{}, Edits: map[string]int{}}
	visit := func(file string) error {
		switch {
		case strings.HasSuffix(file, ".ergo"):
			src, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			out, n, d, err := Modes(string(src))
			rep.Dropped += d
			if err != nil {
				if n > 0 || strings.Contains(err.Error(), "rewritten") {
					rep.Errs = append(rep.Errs, file+": "+err.Error())
				}
				return nil
			}
			if n > 0 {
				rep.Files[file], rep.Edits[file] = out, n
			}
		case opts.Go && strings.HasSuffix(file, ".go"):
			out, n, d, errs, err := modesGo(file)
			if err != nil {
				return err
			}
			rep.Dropped += d
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

// modesGo applies Modes to each string literal of a Go file that parses
// as an Ergo source and spells an old mode.
func modesGo(file string) (string, int, int, []string, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return "", 0, 0, nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.SkipObjectResolution)
	if err != nil {
		return "", 0, 0, nil, nil
	}
	type rewrite struct {
		from, to int
		text     string
	}
	var rws []rewrite
	n, dropped := 0, 0
	var errs []string
	ast.Inspect(f, func(node ast.Node) bool {
		bl, ok := node.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		text, err := strconv.Unquote(bl.Value)
		if err != nil || !strings.Contains(text, "takes") && !strings.Contains(text, "escaping") {
			return true
		}
		out, k, d, err := Modes(text)
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
		dropped += d
		return true
	})
	out := string(src)
	sort.Slice(rws, func(i, j int) bool { return rws[i].from > rws[j].from })
	for _, r := range rws {
		out = out[:r.from] + r.text + out[r.to:]
	}
	return out, n, dropped, errs, nil
}

func unparen(x syntax.Expr) syntax.Expr {
	for {
		p, ok := x.(*syntax.ParenExpr)
		if !ok {
			return x
		}
		x = p.X
	}
}
