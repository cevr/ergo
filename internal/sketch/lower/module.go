package lower

import (
	"fmt"
	"go/format"
	"go/scanner"
	"go/token"
	"path"
	"sort"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	runtimefiles "ergo.local/prototype/runtime"
)

// The emitted module. Its main package is at the root. The runtime package
// keeps its import path, ergo.local/prototype/runtime/v1, through a replace
// of the compiler's module path by the ergo directory, which holds a copy of
// the runtime's source: so an extern go target names the Go package that
// implements it, the module needs no download and no go.sum, and the build
// key covers the runtime's exact bytes.
const (
	programModule = "ergo.local/program"
	runtimeModule = "ergo.local/prototype"
	runtimeDir    = "ergo"
	runtimeImport = runtimeModule + "/runtime/v1"
	taskImport    = runtimeModule + "/runtime/rt"
	goVersion     = "1.27"
)

// File is one file of the emitted Go module, at a slash-separated path
// relative to the module's root.
type File struct {
	Path string
	Data []byte
}

// Runtime returns the runtime files every emitted module holds, besides
// go.mod and main.go: the runtime module's go.mod and the source of
// runtime/v1 and of runtime/rt, which it imports. The lowered main runs
// under v1.Main, whose defect report names Ergo call sites (#230).
func Runtime() []File {
	files := []File{{Path: runtimeDir + "/go.mod", Data: []byte("module " + runtimeModule + "\n\ngo " + goVersion + "\n")}}
	src, err := runtimefiles.Files("runtime/v1")
	if err != nil {
		panic("lower: the runtime source is not embedded: " + err.Error())
	}
	for _, f := range src {
		files = append(files, File{Path: path.Join(runtimeDir, f.Path), Data: f.Data})
	}
	return files
}

// goMod is the emitted module's go.mod.
const goMod = "module " + programModule + "\n\ngo " + goVersion + "\n\nrequire " + runtimeModule + " v0.0.0\n\nreplace " + runtimeModule + " => ./" + runtimeDir + "\n"

// Refusal is a checked program that this lowering cannot lower yet: a form
// whose checker stage has not landed, or an extern with no Go body. It is
// located at the source construct, and names its owner.
type Refusal struct {
	Source       int // index into Unit.Sources
	Offset       int // byte offset into the source
	Line, Column int // 1-based; the column counts bytes
	Message      string
}

// Refused is the error of a lowering that met refusals. Nothing is emitted.
type Refused struct{ Refusals []Refusal }

func (e *Refused) Error() string {
	if len(e.Refusals) == 0 {
		return "lower: refused"
	}
	r := e.Refusals[0]
	return fmt.Sprintf("%d:%d: %s", r.Line, r.Column, r.Message)
}

// refusal is the panic value that ends the lowering of one declaration.
type refusal struct {
	pos syntax.Pos
	msg string
}

// Lower lowers the checked unit to the generated files of a Go module,
// go.mod and main.go; the module also holds Runtime's files, which the
// emission cache copies as a keyed input. Every form it lowers reads the checker's Info; a form it
// cannot lower yet is a Refusal at its source position, and a fact missing
// from Info is an internal compiler error at the site that needs it. The
// unit's package must have no diagnostic.
func Lower(u Unit) ([]File, error) {
	if err := u.Validate(); err != nil {
		return nil, err
	}
	if len(u.Files) != 1 {
		return nil, fmt.Errorf("lower: %d files in one package; packages of several files are 196b's", len(u.Files))
	}
	code, err := lowerPackage(u, false)
	if err != nil {
		return nil, err
	}
	return []File{{Path: "go.mod", Data: []byte(goMod)}, {Path: "main.go", Data: code}}, nil
}

// lowerPackage lowers the unit's package once: program runs the naming
// pass (naming.go), then lowers every body.
func lowerPackage(u Unit, noEntry bool) ([]byte, error) {
	l := newLowerer(u)
	l.noEntry = noEntry
	return l.program()
}

// pkgKey identifies a package-scope Go entity: its kind and its check
// object, or the text that identifies a generated one.
type pkgKey struct {
	kind string
	obj  any
}

// pkgEntry is a package-scope entity and its preferred name.
type pkgEntry struct {
	key  pkgKey
	base string
}

// pkgName is the Go name of a package-scope entity. The naming pass gives
// every one; after it, a new entity is a compiler defect: the pass and the
// lowering disagree on what the program needs.
func (l *lowerer) pkgName(key pkgKey, base string) string {
	if name, ok := l.pkgNames[key]; ok {
		return name
	}
	if l.prenamed {
		refuse(l.file, "internal compiler error: the package-scope name %s (%s) was not given by the naming pass (a compiler defect)", base, key.kind)
	}
	name := l.names.alloc(base)
	l.pkgNames[key] = name
	l.pkgOrder = append(l.pkgOrder, pkgEntry{key: key, base: base})
	return name
}

// lowerer holds the lowering of one package.
type lowerer struct {
	unit Unit
	pkg  *check.Package
	info *check.Info
	file *syntax.File
	src  string // the admitted path //line directives name

	names    *namer // the Go package scope
	pkgNames map[pkgKey]string
	pkgOrder []pkgEntry // in naming order
	prenamed bool       // the naming pass is done
	bodies   int        // function bodies lowered (TestNamingWorkIsLinear)
	formats  int        // go/format runs

	types    map[*check.TypeName]*typeDecl
	funcs    map[*check.Func]*funcDecl
	vars     map[*check.Var]string // package-level lets
	wires    map[wireSet]string
	adapters map[string]string        // operation-table adapters by source and target Go types
	texts    map[pkgKey]bool          // generated text functions emitted
	defaults map[pkgKey]bool          // generated default functions emitted (types.go, Defaults)
	sets     map[check.TypeSet]string // generated constraint interfaces emitted (decls.go, constraint)
	holds    map[*check.Named]holdState
	typeKeys map[check.Type]string
	// holdsVisits counts holdsMap calls (TestDefaultWorkIsLinear).
	holdsVisits int
	methods     map[*check.Package]map[*check.TypeName][]*check.Func

	envName   string
	envFields map[*check.TypeName]string
	envOrder  []*check.TypeName

	rtName, timeName, taskName string

	gen  strings.Builder // generated and library declarations, before any //line
	user strings.Builder // the user package's declarations, with //line

	// pending holds library declarations met while lowering, emitted into
	// gen until none is left.
	pending []func()

	refusals []Refusal
	reads    map[*check.Var]bool // locals that some expression reads

	calls    []callIdent // the call table (calls.go, #230)
	callSeen map[*syntax.CallExpr]bool
	decls    []declIdent // the declaration table (calls.go, #230)
	declSeen map[syntax.Node]bool
	// inits is the Go code of each package-level initializer, by its
	// value, which initFunc runs in Info.InitOrder (review m1).
	inits map[syntax.Expr]string

	// noEntry lowers a package with an empty Go main instead of a call of
	// its main: the corpus test lowers library-shaped fixtures this way.
	noEntry bool
}

// sortedRefusals orders refusals by position and drops repeats: a
// declaration can be refused while it is named and again while it is
// lowered.
func sortedRefusals(list []Refusal) []Refusal {
	sort.SliceStable(list, func(i, j int) bool { return list[i].Offset < list[j].Offset })
	var out []Refusal
	for _, r := range list {
		if n := len(out); n > 0 && out[n-1].Offset == r.Offset && out[n-1].Message == r.Message {
			continue
		}
		out = append(out, r)
	}
	return out
}

func newLowerer(u Unit) *lowerer {
	l := &lowerer{
		unit:      u,
		pkg:       u.Package,
		info:      u.Package.Info,
		file:      u.Files[0],
		src:       u.Sources[0].Path,
		names:     newNamer(nil),
		pkgNames:  map[pkgKey]string{},
		types:     map[*check.TypeName]*typeDecl{},
		funcs:     map[*check.Func]*funcDecl{},
		vars:      map[*check.Var]string{},
		wires:     map[wireSet]string{},
		adapters:  map[string]string{},
		texts:     map[pkgKey]bool{},
		defaults:  map[pkgKey]bool{},
		sets:      map[check.TypeSet]string{},
		holds:     map[*check.Named]holdState{},
		typeKeys:  map[check.Type]string{},
		methods:   map[*check.Package]map[*check.TypeName][]*check.Func{},
		envFields: map[*check.TypeName]string{},
	}
	l.reads = readVars(l.file, l.info)
	return l
}

// refuse ends the current declaration's lowering with a located refusal.
func refuse(n syntax.Node, format string, args ...any) {
	var pos syntax.Pos
	if n != nil {
		pos = n.Pos()
	}
	panic(refusal{pos: pos, msg: fmt.Sprintf(format, args...)})
}

// ice is an internal compiler error: Info lacks a fact that an accepted
// program's form needs (the Info contract, check-v1 spec). It is located,
// and it never becomes unchecked Go.
func ice(n syntax.Node, format string, args ...any) {
	refuse(n, "internal compiler error: "+format+" (the check-v1 Info contract)", args...)
}

// guard runs f, turning a refusal into a recorded Refusal.
func (l *lowerer) guard(f func()) {
	defer func() {
		if r := recover(); r != nil {
			ref, ok := r.(refusal)
			if !ok {
				panic(r)
			}
			line, col := l.file.Position(ref.pos)
			l.refusals = append(l.refusals, Refusal{Offset: int(ref.pos), Line: line, Column: col, Message: ref.msg})
		}
	}()
	f()
}

// program lowers the package and assembles main.go.
func (l *lowerer) program() ([]byte, error) {
	l.nameAll()
	main := l.mainFunc()
	for _, d := range l.file.Decls {
		l.guard(func() { l.decl(d) })
	}
	var entry strings.Builder
	switch {
	case l.noEntry:
		entry.WriteString("func main() {}\n\n")
	case main == nil:
		l.guard(func() {
			refuse(l.file, "this program has no main function, so ergo build and ergo run have no entry point")
		})
	default:
		l.guard(func() { l.entry(&entry, main) })
	}
	for len(l.pending) > 0 {
		next := l.pending[0]
		l.pending = l.pending[1:]
		l.guard(next)
	}
	if len(l.refusals) > 0 {
		return nil, &Refused{Refusals: sortedRefusals(l.refusals)}
	}
	table := l.callTable() // before the imports: it may name the runtime package
	inits := l.initFunc()
	var out strings.Builder
	out.WriteString("// Code generated by ergo from " + path.Base(strings.ReplaceAll(l.src, "\\", "/")) + ". DO NOT EDIT.\n\npackage main\n\n")
	var imports []string
	if l.rtName != "" {
		imports = append(imports, l.rtName+" "+`"`+runtimeImport+`"`)
	}
	if l.timeName != "" {
		imports = append(imports, l.timeName+` "time"`)
	}
	if l.taskName != "" {
		imports = append(imports, l.taskName+" "+`"`+taskImport+`"`)
	}
	if len(imports) > 0 {
		out.WriteString("import (\n\t" + strings.Join(imports, "\n\t") + "\n)\n\n")
	}
	// Entry, environment and library declarations come before the first
	// //line directive, so their lines stay main.go's.
	out.WriteString(entry.String())
	l.envType(&out)
	out.WriteString(l.gen.String())
	out.WriteString(table)
	out.WriteString(l.user.String())
	out.WriteString(inits)
	l.formats++
	formatted, err := format.Source([]byte(out.String()))
	if err != nil {
		return []byte(out.String()), fmt.Errorf("lower: the emitted Go does not parse (a compiler defect): %w", err)
	}
	return anchorLines(formatted), nil
}

// anchorLines maps every Go line after a //line directive to that directive.
// Go counts the lines that follow a //line directive on from it, so a
// statement go/format spreads over several Go lines (an anonymous struct
// type, a long literal) would name later .ergo lines, and so would the
// lines after an inline site directive (atSite). Each Go line that does not
// directly follow a //line directive gets that directive again, inline,
// before its first token, so a Go line names its own statement and an
// inline directive holds for the rest of its line only (stage C round 2
// review m3). A line inside a raw string or a block comment, and a line
// that starts with a comment, is left as it is.
func anchorLines(src []byte) []byte {
	fset := token.NewFileSet()
	file := fset.AddFile("", -1, len(src))
	var s scanner.Scanner
	s.Init(file, src, nil, scanner.ScanComments)
	type insert struct {
		at   int
		text string
	}
	var inserts []insert
	cur, curLine, lastLine := "", 0, 0
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		p := file.PositionFor(pos, false)
		first := p.Line != lastLine
		lastLine = p.Line
		if tok == token.STRING || tok == token.COMMENT {
			// A raw string or a block comment may span lines. (An implied
			// semicolon's text is a line feed, but it ends its own line.)
			lastLine += strings.Count(lit, "\n")
		}
		switch {
		case tok == token.COMMENT && strings.HasPrefix(lit, "//line "):
			cur, curLine = lit[len("//line "):], p.Line
		case !first || tok == token.COMMENT || tok == token.SEMICOLON || cur == "" || p.Line == curLine+1:
		default:
			inserts = append(inserts, insert{p.Offset, "/*line " + cur + "*/"})
		}
	}
	if len(inserts) == 0 {
		return src
	}
	var b strings.Builder
	prev := 0
	for _, in := range inserts {
		b.Write(src[prev:in.at])
		b.WriteString(in.text)
		prev = in.at
	}
	b.Write(src[prev:])
	return []byte(b.String())
}

// rt is the runtime package's name in main.go.
func (l *lowerer) rt() string {
	if l.rtName == "" {
		l.rtName = l.pkgName(pkgKey{"import", runtimeImport}, "ergo")
	}
	return l.rtName
}

// timePkg is the time package's name in main.go.
func (l *lowerer) timePkg() string {
	if l.timeName == "" {
		l.timeName = l.pkgName(pkgKey{"import", "time"}, "time")
	}
	return l.timeName
}

// taskPkg is the name in main.go of runtime/rt, whose Task the entry's body
// takes.
func (l *lowerer) taskPkg() string {
	if l.taskName == "" {
		l.taskName = l.pkgName(pkgKey{"import", taskImport}, "rt")
	}
	return l.taskName
}

// mainFunc is the package's main function, or nil.
func (l *lowerer) mainFunc() *check.Func {
	for _, f := range l.pkg.Funcs {
		if f.Name == "main" && f.Decl != nil && f.Decl.Extern == nil {
			return f
		}
	}
	return nil
}

// entry writes Go's main: it binds the host's providers of main's
// capabilities, calls the program's main and reports its typed failure.
func (l *lowerer) entry(b *strings.Builder, main *check.Func) {
	fd := l.funcDecl(main)
	if len(main.Sig.Params) > 0 || len(main.Sig.Results) > 0 || len(main.Sig.TParams) > 0 {
		refuse(main.Decl.Name, "main takes no parameters and returns no value")
	}
	rt := l.rt()
	locals := newNamer(l.names) // main's locals reference only the names above
	task, env := locals.alloc("t"), locals.alloc("env")
	fields := []string{envTask + ": " + task}
	var host string
	for _, cap := range rowElems(main.Sig.Uses) {
		n, ok := cap.(*check.Named)
		if !ok || n.Obj.Pkg == nil || n.Obj.Pkg.Path != "prelude" || n.Obj.Name != "Console" {
			refuse(main.Decl.Name, "main needs %s, and the host provides only Console; bind it with a with block inside main", cap)
		}
		// The runtime's Console prints a line; the capability's operation
		// takes the task first (rows.md, Rows 7), so the host's provider is
		// a one-method adapter.
		name := l.pkgName(pkgKey{"host", "Console"}, "hostConsole")
		hl := newNamer(l.names)
		line := hl.alloc("line")
		host = "// " + name + " is the host's Console.\ntype " + name + " struct{}\n\n" +
			"func (" + name + ") " + memberName("Println") + "(_ *" + l.taskPkg() + ".Task, " + line + " string) { " + rt + ".Console{}.Println(" + line + ") }\n\n"
		fields = append(fields, l.envField(n.Obj)+": "+name+"{}")
	}
	b.WriteString("func main() {\n")
	// main runs as the root task under v1.Main, which reports its exit:
	// a typed failure, a defect with its Ergo call sites (#230), or an
	// interruption (runtime.md, Exit causes). The environment holds that
	// task (stage D3b, Rule 1).
	b.WriteString("\t" + rt + ".Main(func(" + task + " *" + l.taskPkg() + ".Task) error {\n")
	b.WriteString("\t\t" + env + " := &" + l.env() + "{" + strings.Join(fields, ", ") + "}\n")
	if l.hasInits() {
		// The package-level lets run first, in the root task (review m1).
		b.WriteString("\t\t" + l.pkgName(pkgKey{"init", nil}, "ergoInit") + "(" + env + ")\n")
	}
	if fd.fallible {
		b.WriteString("\t\treturn " + fd.name + "(" + env + ")\n")
	} else {
		b.WriteString("\t\t" + fd.name + "(" + env + ")\n\t\treturn nil\n")
	}
	b.WriteString("\t})\n}\n\n" + host)
}

// readVars is the set of variables some expression of the file reads: every
// Use except an assignment's target, which Go does not count as a use.
func readVars(file *syntax.File, info *check.Info) map[*check.Var]bool {
	targets := map[*syntax.Ident]bool{}
	syntax.Inspect(file, func(n syntax.Node) bool {
		if s, ok := n.(*syntax.AssignStmt); ok {
			for _, lhs := range s.Lhs {
				if id, ok := lhs.(*syntax.Ident); ok {
					targets[id] = true
				}
			}
		}
		return true
	})
	reads := map[*check.Var]bool{}
	for id, o := range info.Uses {
		if v, ok := o.(*check.Var); ok && !targets[id] {
			reads[v] = true
		}
	}
	return reads
}
