// Package schemata folds many mutants of Go source into one tree (mutant
// schemata): each function a mutant changes keeps its baseline body behind
// a dispatcher that calls the mutant's body when the run's switch names
// that mutant. One build then serves every folded mutant; each mutant
// still runs in its own process.
//
// The switch is read once at start from the environment: SwitchEnv holds
// the mutant's row name (unset, or a name the tree does not hold, is the
// baseline), and TraceEnv, when set, is a file each dispatcher appends its
// mutant ids to the first time it runs, so the caller knows which sites a
// run reached.
//
// A mutant folds only when its file differs from the baseline's in
// function bodies alone: the same package, the same non-function
// declarations and imports, the same functions with the same signatures.
// Any other difference (a type, a signature, a package-level variable, an
// init function) leaves the mutant out, with the reason, for an isolated
// build.
package schemata

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// SwitchEnv is the environment variable naming the mutant a run enables.
const SwitchEnv = "ERGO_MUTANT"

// TraceEnv is the environment variable naming the file a run appends the
// ids of the mutant sites it reached to, one per line.
const TraceEnv = "ERGO_MUTANT_TRACE"

// File is a Go source file by its slash path.
type File struct {
	Path string
	Data []byte
}

// Variant is one mutant: the file it changes, as the mutant writes it.
// ID is its switch id (> 0), Name the value of SwitchEnv that enables it.
type Variant struct {
	ID   int
	Name string
	File File
}

// Result is a folded tree: the files to write (the changed baseline files
// with their switches), the ids it folded with the sites
// (file:function) each changes, and the ids it left out with the reason.
type Result struct {
	Files    []File
	Folded   map[int][]string
	Isolated map[int]string
}

type funcEdit struct {
	id   int
	text []byte
}

// Fold folds variants into base, the baseline's files the variants change.
// Ids and names must be distinct; ids positive.
func Fold(base []File, variants []Variant) Result {
	res := Result{Folded: map[int][]string{}, Isolated: map[int]string{}}
	baseByPath := map[string][]byte{}
	for _, f := range base {
		baseByPath[f.Path] = f.Data
	}
	parsed := map[string]*goFile{}
	parseErr := map[string]error{}
	parseBase := func(p string) (*goFile, error) {
		if g, ok := parsed[p]; ok {
			return g, parseErr[p]
		}
		g, err := parseGo(p, baseByPath[p])
		parsed[p], parseErr[p] = g, err
		return g, err
	}
	edits := map[string]map[string][]funcEdit{}
	names := map[string][]Variant{}
	for _, v := range variants {
		if v.ID <= 0 || v.Name == "" {
			res.Isolated[v.ID] = "a folded mutant needs a positive id and a name"
			continue
		}
		fns, why := diffVariant(baseByPath, parseBase, v.File)
		if why != "" {
			res.Isolated[v.ID] = why
			continue
		}
		if len(fns) == 0 {
			res.Isolated[v.ID] = "the mutated file equals the baseline's but for //line directives: no site to switch"
			continue
		}
		keys := make([]string, 0, len(fns))
		for k := range fns {
			keys = append(keys, k)
			res.Folded[v.ID] = append(res.Folded[v.ID], v.File.Path+":"+k)
		}
		sort.Strings(res.Folded[v.ID])
		if edits[v.File.Path] == nil {
			edits[v.File.Path] = map[string][]funcEdit{}
		}
		for _, k := range keys {
			edits[v.File.Path][k] = append(edits[v.File.Path][k], funcEdit{id: v.ID, text: fns[k]})
		}
		names[v.File.Path] = append(names[v.File.Path], v)
	}
	paths := make([]string, 0, len(edits))
	for p := range edits {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		g, _ := parseBase(p)
		res.Files = append(res.Files, File{Path: p, Data: spliceFile(g, fileSuffix(p), edits[p], names[p])})
	}
	return res
}

type goFile struct {
	src   []byte
	fset  *token.FileSet
	file  *ast.File
	funcs map[string]*ast.FuncDecl
	other []string
	dup   map[string]bool
}

func parseGo(name string, src []byte) (*goFile, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	g := &goFile{src: src, fset: fset, file: f, funcs: map[string]*ast.FuncDecl{}, dup: map[string]bool{}}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			key := funcKey(d)
			if _, seen := g.funcs[key]; seen {
				g.dup[key] = true
			}
			g.funcs[key] = d
		default:
			g.other = append(g.other, stripLines(g.text(d)))
		}
	}
	return g, nil
}

func (g *goFile) text(n ast.Node) string {
	return string(g.src[g.off(n.Pos()):g.off(n.End())])
}

func (g *goFile) off(p token.Pos) int { return g.fset.Position(p).Offset }

func funcKey(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return d.Name.Name
	}
	t := d.Recv.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	switch x := t.(type) {
	case *ast.IndexExpr:
		t = x.X
	case *ast.IndexListExpr:
		t = x.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name + "." + d.Name.Name
	}
	return "?." + d.Name.Name
}

var lineDirective = regexp.MustCompile(`//line [^\n]*|/\*line [^*]*\*/`)

func stripLines(s string) string { return lineDirective.ReplaceAllString(s, "") }

// diffVariant is the functions whose text the mutated file f changes, by
// key, with the mutated text; or why the mutant cannot fold.
func diffVariant(baseByPath map[string][]byte, parseBase func(string) (*goFile, error), f File) (map[string][]byte, string) {
	switch {
	case !strings.HasSuffix(f.Path, ".go"):
		return nil, "not a Go file: " + f.Path
	case strings.HasSuffix(f.Path, "_test.go"):
		return nil, "a test file: the dispatcher's package would differ by test binary"
	}
	b, ok := baseByPath[f.Path]
	if !ok {
		return nil, "no baseline for " + f.Path
	}
	if bytes.Equal(b, f.Data) {
		return nil, "the mutated file equals the baseline's"
	}
	bg, err := parseBase(f.Path)
	if err != nil {
		return nil, "the baseline's " + f.Path + " does not parse: " + err.Error()
	}
	vg, err := parseGo(f.Path, f.Data)
	if err != nil {
		return nil, "the mutated " + f.Path + " does not parse: " + err.Error()
	}
	if bg.file.Name.Name != vg.file.Name.Name {
		return nil, "the mutant renames the package of " + f.Path
	}
	if !slices.Equal(bg.other, vg.other) {
		return nil, "the mutant changes a type, variable, constant or import in " + f.Path
	}
	if len(bg.funcs) != len(vg.funcs) {
		return nil, "the mutant adds or removes a function in " + f.Path
	}
	out := map[string][]byte{}
	for k, vd := range vg.funcs {
		bd, ok := bg.funcs[k]
		if !ok {
			return nil, "the mutant adds function " + k + " in " + f.Path
		}
		if stripLines(vg.text(vd)) == stripLines(bg.text(bd)) {
			continue
		}
		if reason := foldable(k, bg, bd, vg, vd); reason != "" {
			return nil, reason
		}
		out[k] = []byte(vg.text(vd))
	}
	return out, ""
}

func foldable(key string, bg *goFile, bd *ast.FuncDecl, vg *goFile, vd *ast.FuncDecl) string {
	switch {
	case bg.dup[key] || vg.dup[key]:
		return "the mutant changes " + key + ", declared more than once"
	case bd.Name.Name == "init" || bd.Name.Name == "main" || bd.Name.Name == "_":
		return "the mutant changes " + key + ", which no dispatcher can switch"
	case bd.Body == nil || vd.Body == nil:
		return "the mutant changes " + key + ", a function without a body"
	case linked(bd):
		return "the mutant changes linked function " + key
	case strings.HasPrefix(bd.Name.Name, "ergoMutant") || strings.Contains(bd.Name.Name, "__ergomut"):
		return "the mutant changes " + key + ", whose name the switch reserves"
	}
	if stripLines(signature(bg, bd)) != stripLines(signature(vg, vd)) {
		return "the mutant changes the signature of " + key
	}
	return ""
}

func signature(g *goFile, d *ast.FuncDecl) string {
	return string(g.src[g.off(d.Pos()):g.off(d.Body.Lbrace)])
}

type insertion struct {
	at   int
	del  int
	text string
}

func spliceFile(g *goFile, sfx string, fileEdits map[string][]funcEdit, variants []Variant) []byte {
	keys := make([]string, 0, len(fileEdits))
	for k := range fileEdits {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	importAt := g.off(g.file.Name.End())
	for _, d := range g.file.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
			importAt = g.off(gd.End())
		}
	}
	ins := []insertion{{at: importAt, text: importBlock(sfx)}}
	var tail bytes.Buffer
	tail.WriteString(switchSource(sfx, variants))
	for _, k := range keys {
		d := g.funcs[k]
		list := fileEdits[k]
		slices.SortFunc(list, func(a, b funcEdit) int { return a.id - b.id })
		names, inserts := nameParams(g, d)
		ins = append(ins, inserts...)
		ins = append(ins, insertion{at: g.off(d.Body.Lbrace) + 1, text: dispatcher(sfx, d, names, list)})
		for _, e := range list {
			tail.WriteString("\n\n")
			tail.Write(renamed(e.text, d, e.id))
			tail.WriteString("\n")
		}
	}
	slices.SortStableFunc(ins, func(a, b insertion) int { return a.at - b.at })
	var out bytes.Buffer
	prev := 0
	for _, in := range ins {
		out.Write(g.src[prev:in.at])
		out.WriteString(in.text)
		prev = in.at + in.del
	}
	out.Write(g.src[prev:])
	out.Write(tail.Bytes())
	return out.Bytes()
}

type callNames struct {
	recv     string
	params   []string
	variadic bool
	results  int
}

func nameParams(g *goFile, d *ast.FuncDecl) (callNames, []insertion) {
	var n callNames
	var ins []insertion
	name := func(field *ast.Field, fresh string) []string {
		if len(field.Names) == 0 {
			ins = append(ins, insertion{at: g.off(field.Type.Pos()), text: fresh + " "})
			return []string{fresh}
		}
		var out []string
		for i, id := range field.Names {
			if id.Name == "_" {
				f := fresh + "_" + strconv.Itoa(i)
				ins = append(ins, insertion{at: g.off(id.Pos()), del: 1, text: f})
				out = append(out, f)
				continue
			}
			out = append(out, id.Name)
		}
		return out
	}
	if d.Recv != nil && len(d.Recv.List) == 1 {
		n.recv = name(d.Recv.List[0], "ergoMutRecv")[0]
	}
	if d.Type.Params != nil {
		for i, field := range d.Type.Params.List {
			n.params = append(n.params, name(field, "ergoMutP"+strconv.Itoa(i))...)
			if _, ok := field.Type.(*ast.Ellipsis); ok {
				n.variadic = true
			}
		}
	}
	if d.Type.Results != nil {
		n.results = d.Type.Results.NumFields()
	}
	return n, ins
}

func dispatcher(sfx string, d *ast.FuncDecl, n callNames, list []funcEdit) string {
	var b strings.Builder
	b.WriteString("\n\tif ergoMutantOn" + sfx + " {\n\t\tergoMutantMark" + sfx + "(")
	for i, e := range list {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(strconv.Itoa(e.id))
	}
	b.WriteString(")\n\t\tswitch ergoMutantSel" + sfx + " {\n")
	for _, e := range list {
		call := callExpr(d, n, e.id)
		fmt.Fprintf(&b, "\t\tcase %d:\n", e.id)
		if n.results > 0 {
			fmt.Fprintf(&b, "\t\t\treturn %s\n", call)
		} else {
			fmt.Fprintf(&b, "\t\t\t%s\n\t\t\treturn\n", call)
		}
	}
	b.WriteString("\t\t}\n\t}\n")
	return b.String()
}

func variantName(name string, id int) string {
	return name + "__ergomut" + strconv.Itoa(id)
}

func callExpr(d *ast.FuncDecl, n callNames, id int) string {
	var b strings.Builder
	if n.recv != "" {
		b.WriteString(n.recv + ".")
	}
	b.WriteString(variantName(d.Name.Name, id))
	if d.Type.TypeParams != nil && len(d.Type.TypeParams.List) > 0 {
		var tps []string
		for _, f := range d.Type.TypeParams.List {
			for _, id := range f.Names {
				tps = append(tps, id.Name)
			}
		}
		b.WriteString("[" + strings.Join(tps, ", ") + "]")
	}
	b.WriteString("(" + strings.Join(n.params, ", "))
	if n.variadic {
		b.WriteString("...")
	}
	b.WriteString(")")
	return b.String()
}

func renamed(text []byte, d *ast.FuncDecl, id int) []byte {
	fset := token.NewFileSet()
	src := append([]byte("package p\n"), text...)
	f, err := parser.ParseFile(fset, "", src, parser.SkipObjectResolution)
	if err != nil || len(f.Decls) != 1 {
		return text
	}
	fd, ok := f.Decls[0].(*ast.FuncDecl)
	if !ok {
		return text
	}
	at := fset.Position(fd.Name.Pos()).Offset
	end := at + len(fd.Name.Name)
	var out bytes.Buffer
	out.Write(src[len("package p\n"):at])
	out.WriteString(variantName(d.Name.Name, id))
	out.Write(src[end:])
	return out.Bytes()
}
