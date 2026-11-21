package irspike

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRSpawnsTypeCheck type-checks, in process with go/types, the Go of
// GenerateSpawn's IR bodies under both spawn lowerings: GoWith of the
// block's captures ("") and the closure over them ("closure", the
// reference). irGo prints each IR body as Go (the kinds these bodies use),
// in place of its declaration in the lowered main.go, and the package is
// checked against the runtime's source. It asks "does the IR's Go of the
// GoWith form type-check" (its struct type argument, its literal's
// captures parameter, each field read) without a Go build; the derivative
// compiler's own decode and the run stay with TestIRSpawnsMatchSource.
//
// Mutants: gp-spawn2-field-first (every captured read reads F0: gather's
// string, let and parameter fields are not of F0's type, the *Env),
// gp-spawn2-param-type (a parameter's captured field takes the
// environment's type).
//
// It checks GenerateSpawn without sorts, whose match forms irGo does not
// print (TestIRSpawnsHaveBodies lowers sorts).
func TestIRSpawnsTypeCheck(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 2)
	for _, mode := range []string{"", "closure"} {
		prev := lower.SetSpawnLoweringForTest(mode)
		for seed := first; seed < first+count; seed++ {
			text := generateSpawn(seed, false)
			file := filepath.Join(t.TempDir(), "p.ergo")
			if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
			u, err := Load(file)
			if err != nil {
				t.Fatalf("seed %d: %v", seed, err)
			}
			files, _, err := lower.LowerIR(u)
			if err != nil {
				t.Fatalf("seed %d: %v", seed, err)
			}
			src, withs, err := irGoMain(files)
			if err != nil {
				t.Fatalf("mode %q seed %d: %v", mode, seed, err)
			}
			if want := map[string]int{"": len(spawnFuncs) - 1, "closure": 0}[mode]; withs != want {
				t.Errorf("mode %q seed %d: %d GoWith calls; want %d", mode, seed, withs, want)
			}
			if errs := typeCheckMain(t, root, src); len(errs) > 0 {
				t.Errorf("mode %q seed %d: the IR's Go does not type-check:\n%s\nGo:\n%s", mode, seed, strings.Join(errs, "\n"), numbered(src))
			}
		}
		lower.SetSpawnLoweringForTest(prev)
	}
}

// typeCheckMain type-checks src, package main of the lowered module,
// importing ergo.local/prototype/... from the repository at root and the
// standard library from GOROOT, from source.
func typeCheckMain(t *testing.T, root, src string) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", src, parser.SkipObjectResolution)
	if err != nil {
		return []string{err.Error()}
	}
	ctx := build.Default
	ctx.CgoEnabled = false
	imp := &repoImporter{fset: fset, root: root, ctx: &ctx, done: map[string]*types.Package{}}
	var errs []string
	conf := types.Config{Importer: imp, Error: func(err error) {
		if len(errs) < 20 {
			errs = append(errs, err.Error())
		}
	}}
	conf.Check("main", fset, []*ast.File{f}, nil)
	return errs
}

// repoImporter imports ergo.local/prototype/<dir> from root/<dir>'s
// non-test Go files that the build context (cgo off) matches, and any
// other path from the standard library's source in GOROOT.
type repoImporter struct {
	fset *token.FileSet
	root string
	ctx  *build.Context
	done map[string]*types.Package
}

func (r *repoImporter) Import(path string) (*types.Package, error) {
	if p, ok := r.done[path]; ok {
		return p, nil
	}
	if path == "unsafe" {
		return types.Unsafe, nil
	}
	var bp *build.Package
	var err error
	if rel, ok := strings.CutPrefix(path, "ergo.local/prototype/"); ok {
		bp, err = r.ctx.ImportDir(filepath.Join(r.root, rel), 0)
	} else {
		bp, err = r.ctx.Import(path, r.root, 0) // the standard library, from GOROOT
	}
	if err != nil {
		return nil, err
	}
	var files []*ast.File
	for _, name := range bp.GoFiles {
		f, err := parser.ParseFile(r.fset, filepath.Join(bp.Dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	conf := types.Config{Importer: r, FakeImportC: true}
	p, err := conf.Check(path, r.fset, files, nil)
	r.done[path] = p
	return p, err
}

func numbered(src string) string {
	var b strings.Builder
	for i, l := range strings.Split(src, "\n") {
		fmt.Fprintf(&b, "%4d %s\n", i+1, l)
	}
	return b.String()
}

// The IR's JSON (internal/sketch/lower ir.go), as irGo reads it.
type irgFile struct {
	Funcs []irgFunc `json:"funcs"`
}

type irgFunc struct {
	Name   string `json:"name"`
	Locals []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"locals"`
	Body []*irgStmt `json:"body"`
}

type irgStmt struct {
	K     string     `json:"k"`
	Local int        `json:"local"`
	Val   *int       `json:"val"`
	Key   *int       `json:"key"`
	X     *irgExpr   `json:"x"`
	Y     *irgExpr   `json:"y"`
	Then  []*irgStmt `json:"then"`
	Else  []*irgStmt `json:"else"`
	Body  []*irgStmt `json:"body"`
	Op    string     `json:"op"`
	Label string     `json:"label"`
}

type irgExpr struct {
	K      string     `json:"k"`
	I      int        `json:"i"`
	T      string     `json:"t"`
	V      string     `json:"v"`
	Op     string     `json:"op"`
	X      *irgExpr   `json:"x"`
	Y      *irgExpr   `json:"y"`
	Name   string     `json:"name"`
	Args   []*irgExpr `json:"args"`
	Elems  []*irgExpr `json:"elems"`
	Keys   []string   `json:"keys"`
	Params []int      `json:"params"`
	Body   []*irgStmt `json:"body"`
}

// irGoMain is the lowered main.go with each bodiless declaration the IR
// file gives a body printed as Go (irGo), and the number of GoWith calls.
func irGoMain(files []lower.File) (string, int, error) {
	var main string
	var ir irgFile
	for _, f := range files {
		switch f.Path {
		case "main.go":
			main = string(f.Data)
		case lower.IRFileName:
			if err := json.Unmarshal(f.Data, &ir); err != nil {
				return "", 0, err
			}
		}
	}
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, "main.go", main, parser.SkipObjectResolution)
	if err != nil {
		return "", 0, err
	}
	aliases := map[string]string{}
	for _, im := range af.Imports {
		p, _ := strconv.Unquote(im.Path.Value)
		name := p[strings.LastIndex(p, "/")+1:]
		if im.Name != nil {
			name = im.Name.Name
		}
		aliases[p] = name
	}
	bodies := map[string]irgFunc{}
	for _, fn := range ir.Funcs {
		bodies[fn.Name] = fn
	}
	g := &irGo{aliases: aliases}
	type edit struct {
		at, end int
		text    string
	}
	var edits []edit
	for _, d := range af.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body != nil || fd.Recv != nil {
			continue
		}
		fn, ok := bodies[fd.Name.Name]
		if !ok {
			continue
		}
		var params []string
		n := 0
		for _, fl := range fd.Type.Params.List {
			typ := main[fset.Position(fl.Type.Pos()).Offset:fset.Position(fl.Type.End()).Offset]
			k := len(fl.Names)
			if k == 0 {
				k = 1
			}
			for range k {
				params = append(params, "l"+strconv.Itoa(n)+" "+typ)
				n++
			}
		}
		res := ""
		if fd.Type.Results != nil {
			res = " " + main[fset.Position(fd.Type.Results.Pos()).Offset:fset.Position(fd.Type.Results.End()).Offset]
		}
		g.locals = map[int]string{}
		for i, l := range fn.Locals {
			g.locals[n+i] = l.Type
		}
		var b strings.Builder
		fmt.Fprintf(&b, "func %s(%s)%s {\n", fd.Name.Name, strings.Join(params, ", "), res)
		for i := range n {
			fmt.Fprintf(&b, "\t_ = l%d\n", i)
		}
		g.stmts(&b, fn.Body, 1)
		b.WriteString("}")
		edits = append(edits, edit{fset.Position(fd.Pos()).Offset, fset.Position(fd.End()).Offset, b.String()})
	}
	out := main
	for i := len(edits) - 1; i >= 0; i-- {
		e := edits[i]
		out = out[:e.at] + e.text + out[e.end:]
	}
	return out, g.withs, nil
}

// irGo prints the IR kinds GenerateSpawn's bodies use as Go; any other
// kind is printed as a call of an undeclared function, so the check fails
// and names it.
type irGo struct {
	aliases map[string]string
	locals  map[int]string
	withs   int
	results []bool // per enclosing literal: its results are (T, error)
}

func (g *irGo) typ(s string) string {
	for p, a := range g.aliases {
		s = strings.ReplaceAll(s, p+".", a+".")
	}
	return s
}

func local(i int) string { return "l" + strconv.Itoa(i) }

func (g *irGo) stmts(b *strings.Builder, list []*irgStmt, depth int) {
	for _, s := range list {
		g.stmt(b, s, depth)
	}
}

func (g *irGo) stmt(b *strings.Builder, s *irgStmt, depth int) {
	in := strings.Repeat("\t", depth)
	switch s.K {
	case "def":
		fmt.Fprintf(b, "%s%s := %s\n%s_ = %s\n", in, local(s.Local), g.expr(s.X), in, local(s.Local))
	case "var":
		fmt.Fprintf(b, "%svar %s %s\n%s_ = %s\n", in, local(s.Local), g.typ(g.locals[s.Local]), in, local(s.Local))
	case "set":
		fmt.Fprintf(b, "%s%s %s= %s\n", in, local(s.Local), s.Op, g.expr(s.X))
	case "def2":
		fmt.Fprintf(b, "%s%s, %s := %s\n%s_, _ = %s, %s\n", in, local(s.Local), local(*s.Val), g.expr(s.X), in, local(s.Local), local(*s.Val))
	case "store":
		fmt.Fprintf(b, "%s%s = %s\n", in, g.expr(s.X), g.expr(s.Y))
	case "expr":
		fmt.Fprintf(b, "%s%s\n", in, g.expr(s.X))
	case "discard":
		fmt.Fprintf(b, "%s_ = %s\n", in, g.expr(s.X))
	case "defer":
		fmt.Fprintf(b, "%sdefer %s\n", in, g.expr(s.X))
	case "ret":
		if s.X == nil {
			fmt.Fprintf(b, "%sreturn\n", in)
		} else if n := len(g.results); n > 0 && g.results[n-1] && !strings.HasSuffix(s.X.K, "call") {
			// A value returned from a literal of (T, error): the IR's ret
			// gives the nil error (the derivative's decode).
			fmt.Fprintf(b, "%sreturn %s, nil\n", in, g.expr(s.X))
		} else {
			fmt.Fprintf(b, "%sreturn %s\n", in, g.expr(s.X))
		}
	case "if":
		fmt.Fprintf(b, "%sif %s {\n", in, g.expr(s.X))
		g.stmts(b, s.Then, depth+1)
		if len(s.Else) > 0 {
			fmt.Fprintf(b, "%s} else {\n", in)
			g.stmts(b, s.Else, depth+1)
		}
		fmt.Fprintf(b, "%s}\n", in)
	case "count":
		l := local(s.Local)
		fmt.Fprintf(b, "%sfor %s := %s; %s < %s; %s++ {\n", in, l, g.expr(s.X), l, g.expr(s.Y), l)
		g.stmts(b, s.Body, depth+1)
		fmt.Fprintf(b, "%s}\n", in)
	default:
		fmt.Fprintf(b, "%sirgo_unknown_stmt_%s()\n", in, s.K)
	}
}

func (g *irGo) exprs(list []*irgExpr) string {
	var parts []string
	for _, e := range list {
		parts = append(parts, g.expr(e))
	}
	return strings.Join(parts, ", ")
}

func (g *irGo) expr(e *irgExpr) string {
	if e == nil {
		return "irgo_missing()"
	}
	switch e.K {
	case "local":
		return local(e.I)
	case "const":
		if e.T == "string" {
			return "string(" + strconv.Quote(e.V) + ")"
		}
		if e.T == "" {
			return e.V
		}
		return g.typ(e.T) + "(" + e.V + ")"
	case "bin":
		return "(" + g.expr(e.X) + " " + e.Op + " " + g.expr(e.Y) + ")"
	case "un":
		return "(" + e.Op + g.expr(e.X) + ")"
	case "conv":
		return "(" + g.typ(e.T) + ")(" + g.expr(e.X) + ")"
	case "field":
		return g.expr(e.X) + "." + e.Name
	case "deref":
		return "(*" + g.expr(e.X) + ")"
	case "addr":
		return "(&" + g.expr(e.X) + ")"
	case "index":
		return g.expr(e.X) + "[" + g.expr(e.Y) + "]"
	case "len":
		return "len(" + g.expr(e.X) + ")"
	case "zero":
		return "*new(" + g.typ(e.T) + ")"
	case "nil":
		return "nil"
	case "pcall":
		if e.Name == "GoWith" {
			g.withs++
		}
		name := g.aliases[e.T] + "." + e.Name
		if e.V != "" {
			name += "[" + g.typ(e.V) + "]"
		}
		return name + "(" + g.exprs(e.Args) + ")"
	case "call":
		// A call of a function of main.go by its Go name, with its Go
		// type arguments (PORT-CALLS: a task call of an Ergo body).
		name := e.Name
		if e.T != "" {
			name += "[" + g.typ(e.T) + "]"
		}
		return name + "(" + g.exprs(e.Args) + ")"
	case "mcall":
		return g.expr(e.X) + "." + e.Name + "(" + g.exprs(e.Args) + ")"
	case "vcall":
		return g.expr(e.X) + "(" + g.exprs(e.Args) + ")"
	case "lit":
		var parts []string
		for i, x := range e.Elems {
			if i < len(e.Keys) {
				parts = append(parts, e.Keys[i]+": "+g.expr(x))
			} else {
				parts = append(parts, g.expr(x))
			}
		}
		return g.typ(e.T) + "{" + strings.Join(parts, ", ") + "}"
	case "func":
		ft := g.typ(e.T)
		list, rest, ok := splitParams(strings.TrimPrefix(ft, "func"))
		if !ok || len(list) != len(e.Params) {
			return "irgo_bad_func()"
		}
		var params []string
		for i, p := range e.Params {
			params = append(params, local(p)+" "+list[i])
		}
		var b strings.Builder
		fmt.Fprintf(&b, "func(%s)%s {\n", strings.Join(params, ", "), rest)
		for _, p := range e.Params {
			fmt.Fprintf(&b, "_ = %s\n", local(p))
		}
		results, _, _ := splitParams(strings.TrimSpace(rest))
		g.results = append(g.results, len(results) == 2 && results[1] == "error")
		g.stmts(&b, e.Body, 1)
		g.results = g.results[:len(g.results)-1]
		b.WriteString("}")
		return b.String()
	}
	return "irgo_unknown_expr_" + e.K + "()"
}

// splitParams splits "(A, B) rest" at the commas outside any bracket.
func splitParams(s string) ([]string, string, bool) {
	if !strings.HasPrefix(s, "(") {
		return nil, "", false
	}
	var list []string
	depth, start := 0, 1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				if part := strings.TrimSpace(s[start:i]); part != "" {
					list = append(list, part)
				}
				return list, s[i+1:], true
			}
		case ',':
			if depth == 1 {
				list = append(list, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	return nil, "", false
}
