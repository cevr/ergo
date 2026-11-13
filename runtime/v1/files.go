package v1

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	runtimefiles "ergo.local/prototype/runtime"
)

// Source holds this package's runtime files as the one embed owner,
// runtime/embed.go, holds them. This file is not among them: it serves
// only the compiler.
var Source = runtimefiles.Package("v1")

// SourceFiles names the files of Source that the lowering copies today:
// runtime.go alone, which imports no other runtime package. The lowering
// of the std packages task, scope and queue (check-v1 stage D) copies
// runtimefiles.Files("runtime/v1") instead, every file of this package and
// of runtime/rt, which it imports.
var SourceFiles = []string{"runtime.go"}

// rtPath is the import path of runtime/rt, whose *Task is the hidden task.
const rtPath = runtimefiles.Module + "/runtime/rt"

// v1Path is this package's import path.
const v1Path = runtimefiles.Module + "/runtime/v1"

// Hidden is a hidden parameter of a std extern's Go body: an argument the
// Ergo signature does not show and the lowering supplies at the call
// (docs/spec/stdlib.md, Hidden arguments). Its Go type alone names it: no
// Ergo type lowers to *Scope, Site, *rt.Task, Sleeper or ContextPackage.
type Hidden string

const (
	// Frame, a *Scope, is the frame scope of the innermost source function
	// at the call (in an exit action's block, the frame the lowering made
	// for that function; the scope routes to the action's frame itself), or,
	// in the block of a with that owns a resource, that with's scope
	// (Scope.OpenWith): the tasks the block starts belong to the with, and
	// an exit action still registers on the function's frame (OnExit).
	Frame Hidden = "frame"
	// CallSite, a Site, is the call's Ergo identity: the called declaration
	// and the call's source position.
	CallSite Hidden = "site"
	// CallerTask, an *rt.Task, is the task that runs the call.
	CallerTask Hidden = "task"
	// CallerScheduler, a Sleeper, is the Scheduler capability of the
	// caller's environment: the provider the program bound, so a test
	// Scheduler controls the time the body waits (timeout.After).
	CallerScheduler Hidden = "scheduler"
	// ReadingPackage is the package that declares the context-read extern.
	ReadingPackage Hidden = "context-package"
	// ResultRelease, a Releaser[T], is the generated release of the call's
	// result type T, or nil when T holds no resource (#197 L8 B4, Gap 47):
	// a boundary that drops a successful result releases it.
	ResultRelease Hidden = "release"
)

// hiddenOrder is the order hidden parameters take, each at most once.
var hiddenOrder = []Hidden{Frame, CallSite, CallerTask, CallerScheduler, ReadingPackage, ResultRelease}

// Body is the Go shape of one std extern's body: a function of this
// package, or, for an operation of a type that a std extern returns, the
// method of that name on the Go type the extern's body returns.
type Body struct {
	// TypeParams are the Go type parameters of a function, or of the
	// method's receiver type, in order. An Ergo row parameter has none
	// (docs/spec/stdlib.md, Types 2). Nil for a function type parameter.
	TypeParams []TypeParam
	// Hidden are the leading hidden parameters, in hiddenOrder.
	Hidden []Hidden
	// Params are the parameters after them, one per Ergo parameter, in
	// order.
	Params []Param
	// Results are the Go result types, as written: the Ergo value results,
	// then error when the Ergo signature has an error row.
	Results []string
	// Pure reports a body marked //ergo:pure that passed the reviewed
	// rules (pureRefusal): a call of it runs no capability, starts no task
	// and calls no unknown code, so the checker's footprint treats the call
	// as transparent (docs/spec/stdlib.md, Pure bodies).
	Pure bool
	// Host reports a body marked //ergo:host: it reaches host state (a
	// file, a stream, the environment, a process, the clock, the host's
	// random source), so a std function that calls it holds the host
	// authority (docs/spec/stdlib.md, Host packages 7). An exported body
	// that reaches a host member (hostReach) and is not marked is refused.
	Host bool
	// Owned names the body's owned form, from a directive line
	// "//ergo:owned Name" in its doc comment: a body of the same shape
	// that takes its first argument over, so it may keep that argument's
	// storage in its result with no copy (an unsafe one is listed in
	// internal/residue/testdata/unsafe.txt). The lowering calls it only where the
	// first argument is consumed: no binding reads that storage after the
	// call (docs/spec/types.md, Bindings 2, consumed arguments).
	Owned string
	// Wait is the body's waiting policy, from the directive line
	// "//ergo:wait KIND" of its doc comment (WaitKinds): which task a call
	// of it waits on, or none. It is "" for a body with no mark, which no
	// extern may bind (the lowering's emitter refuses it): a body states
	// that it does not wait (STD-NOPRIV NP4d; docs/spec/interop.md, Trusted
	// Go).
	Wait string
	// Release is the body's release route, from "//ergo:release ROUTE"
	// (caller: the body of a checked Close, masked on the caller's task;
	// frame: an exit action, masked on its frame's task), or "".
	Release string
	// BridgeTarget and BridgeProvider are "//ergo:bridge TARGET PROVIDER":
	// the body stands for the prelude extern whose Go target is TARGET,
	// with its first operand a zero-field PROVIDER the call erases (the
	// host Sleep of LiveScheduler).
	BridgeTarget, BridgeProvider string
}

// WaitMark, ReleaseMark and BridgeMark start the directive lines of a
// body's calling policy (Body.Wait, Body.Release, Body.BridgeTarget).
const (
	WaitMark    = "//ergo:wait "
	ReleaseMark = "//ergo:release "
	BridgeMark  = "//ergo:bridge "
)

// WaitKinds are the waiting policies a body states, each with whether it
// takes the caller's task (CallerTask) to wait on or to start under:
//   - none: the body never waits;
//   - caller: it waits on the caller's task, and a stop of it ends the wait;
//   - handle: it waits on the task of the handle it is given;
//   - drain: an uninterruptible stop and completed drain;
//   - child: the caller is the parent of the children it starts and joins;
//   - member: the caller becomes the first member of a clock's domain;
//   - parent: the caller is the parent of the task it starts;
//   - settle: it waits until the rest of a manual clock's domain is parked;
//   - end: it waits, uninterruptibly, for an actor's runner to end.
var WaitKinds = map[string]bool{
	"none": false, "caller": true, "handle": false, "drain": false, "child": true,
	"member": true, "parent": true, "settle": true, "end": true,
}

// ReleaseRoutes are the release routes a body states, each with whether it
// takes the caller's task.
var ReleaseRoutes = map[string]bool{"caller": true, "frame": false}

// BodyNames is the name of every exported function and method of Source
// that BodyOf reads, sorted, a refused one included.
func BodyNames() []string {
	names := make([]string, 0, len(bodyTable))
	for name := range bodyTable {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Bodies are the Go bodies of a package other than runtime/v1 that follows
// the hidden-argument convention: a trusted module's own Go package
// (docs/spec/interop.md, Trusted Go). Its files name v1's hidden types
// through their import of runtime/v1 (*v1.Scope, v1.Site, v1.Sleeper,
// v1.ContextPackage, v1.Releaser[T]) and the task as *rt.Task.
type Bodies struct{ decls goDecls }

// ReadBodies parses the non-test Go files at the root of fsys.
func ReadBodies(fsys fs.FS) (Bodies, error) {
	d, err := parseDeclsIn(fsys, false)
	return Bodies{d}, err
}

// Body is the body of the exported function name under BodyOf's rules,
// with three more. It is a plain function: a method's receiver and a
// type parameter are the package's own types, which no Ergo type lowers
// to. It states its waiting policy (WaitMark), since the lowering calls a
// body by its policy and none is assumed. It states no release route, no
// bridge and no owned form, which are the runtime's own.
func (b Bodies) Body(name string) (Body, error) {
	if g, ok := b.decls[name]; ok && g.fn != nil && (g.fn.Recv != nil || g.fn.Type.TypeParams != nil) {
		return Body{}, fmt.Errorf("%s is a method or a generic function; a module's body is a plain function", name)
	}
	body, err := b.decls.body(name)
	if err != nil {
		return Body{}, errors.New(strings.Replace(err.Error(), "runtime/v1: ", "", 1))
	}
	switch {
	case body.Wait == "":
		return Body{}, fmt.Errorf("%s states no %sKIND (%s)", name, WaitMark, kindList(WaitKinds))
	case body.Release != "" || body.BridgeTarget != "" || body.Owned != "":
		return Body{}, fmt.Errorf("%s states a release route, a bridge or an owned form, which only runtime/v1's bodies state", name)
	}
	return body, nil
}

// PureMark is the directive line that marks a body pure, in the doc
// comment of an exported top-level function.
const PureMark = "//ergo:pure"

// HostMark is the directive line that marks a body that reaches host
// state, in the doc comment of an exported function or method.
const HostMark = "//ergo:host"

// OwnedMark starts the directive line that names a body's owned form
// (Body.Owned): "//ergo:owned Name".
const OwnedMark = "//ergo:owned "

// HostImports are the Go packages each of whose members reaches host
// state. hostReach refuses an unmarked body that names one.
var HostImports = []string{"os", "os/exec", "os/signal", "syscall", "net"}

// HostMembers names, for a Go package outside HostImports, its members
// that reach host state: time's are PureImportHostMembers' (the clock, its
// timers, the local zone), and math/rand/v2's are its top-level draws
// from the host-seeded global source. Every other member computes on its
// arguments (rand.New over a seeded PCG is a program's stream).
var HostMembers = map[string][]string{
	"time": PureImportHostMembers["time"],
	"math/rand/v2": {"ExpFloat64", "Float32", "Float64", "Int", "Int32", "Int32N", "Int64", "Int64N", "IntN",
		"N", "NormFloat64", "Perm", "Shuffle", "Uint", "Uint32", "Uint32N", "Uint64", "Uint64N", "UintN"},
}

// hostMember reports whether member of the Go package path reaches host
// state.
func hostMember(path, member string) bool {
	return slices.Contains(HostImports, path) || slices.Contains(HostMembers[path], member)
}

// PureImports are the Go packages a file may import when one of its bodies
// is marked pure: packages that compute on values and reach no host state,
// except the members PureImportsExcept names.
var PureImports = []string{"strings", "strconv", "unicode", "unicode/utf8", "unicode/utf16", "slices", "sort", "math", "math/bits", "path/filepath", "encoding/hex", "encoding/base64", "crypto/sha256", "hash/crc32"}

// PureImportsExcept names, for a package of PureImports, its members that
// reach host state. A marked body that names one is refused. filepath.Abs
// reads the working directory; EvalSymlinks, Glob, Walk and WalkDir read
// the file system. Every other member of path/filepath is lexical.
var PureImportsExcept = map[string][]string{
	"path/filepath": {"Abs", "EvalSymlinks", "Glob", "Walk", "WalkDir"},
}

// PureImportHostMembers are the Go packages, beyond PureImports, that the
// file of a marked body may import, each with its members that read host
// state: the marked body may not name one, as a package member or as a
// method (std-align S2b). Go's time computes on instants and durations,
// but these read the host's clock, arm its timers or read its zone: Parse
// reads the local zone for a zone abbreviation, so a pure body parses with
// ParseInLocation and UTC. They stay out of PureImports, which is also the
// list a user extern may bind (check, G9): a user extern binds a whole
// package, so no member could be refused there. encoding/csv reads and
// writes the io.Reader and io.Writer it is given; a pure body can give it
// only a strings.Reader or a strings.Builder, since its file imports
// neither io nor os (std-csv U4), so it has no member to refuse.
var PureImportHostMembers = map[string][]string{
	"encoding/csv": nil,
	"time":         {"Now", "Since", "Until", "Sleep", "After", "AfterFunc", "Tick", "NewTicker", "NewTimer", "Local", "LoadLocation", "Parse"},
}

// TypeParam is one Go type parameter of a body: its name and its
// constraint as written (any, comparable).
type TypeParam struct {
	Name, Constraint string
}

// Param is one Go parameter that stands for an Ergo parameter.
type Param struct {
	// Type is the parameter's Go type, as written in the body's source.
	Type string
	// Func is the shape of a function-typed parameter, the Go function the
	// body calls: its own leading hidden parameters (the task that runs the
	// block, for task.Go) follow the same convention. Nil otherwise.
	Func *Body
	// Funcs is the shape of each element of a parameter that is a slice
	// of functions, such as the blocks of tasks.Race: the lowering adapts
	// each element as it adapts a Func. Nil otherwise.
	Funcs *Body
}

// BodyOf returns the Go shape of the body named name in Source: "Go" for
// the function Go, "Queue.Push" for the method Push of Queue. It fails when
// there is no such exported declaration, or when its parameters break the
// convention: a hidden type after an Ergo parameter, out of order or twice.
// It reads bodyTable, which TestBodyTableIsCurrent generates from Source
// with goDecls.body and fails on when it is stale, so it parses nothing
// (CHK1).
func BodyOf(name string) (Body, error) {
	f, ok := bodyTable[name]
	if !ok {
		return Body{}, fmt.Errorf("runtime/v1: no exported Go body %s", name)
	}
	if f.refusal != "" {
		return Body{}, errors.New(f.refusal)
	}
	return f.body, nil
}

// bodyFact is one row of bodyTable: a body's shape, or the text of the
// error goDecls.body refuses it with.
type bodyFact struct {
	body    Body
	refusal string
}

// sourceDecls returns Source parsed, once per process, and counts the
// read (SourceReads).
func sourceDecls() (goDecls, error) {
	sourceReads.Add(1)
	return parsedSource()
}

// parsedSource parses Source once per process.
var parsedSource = sync.OnceValues(func() (goDecls, error) { return parseDecls(Source) })

// sourceReads counts the reads of the parsed Source in this process.
var sourceReads atomic.Int64

// SourceReads returns how many times this process read Source's parsed
// declarations, the first of which parses every runtime/v1 Go file (CHK1).
// A check reads none: the body facts it needs are a generated table
// (bodyTable), so TestCheckWorkGrowsWithImports holds the count still
// across a check. It counts work, never time.
func SourceReads() int64 { return sourceReads.Load() }

// goDecl is one exported function, method or type of a parsed package,
// with the local name its file gives runtime/rt, the import paths of its
// file, and the package's names of functions and variables at top level.
type goDecl struct {
	fn  *ast.FuncDecl
	typ *ast.TypeSpec
	// q names runtime/rt and runtime/v1 as the file writes them (quals).
	q       quals
	imports []string
	// locals are the file's import paths by the local name it gives them.
	locals map[string]string
	values map[string]bool // shared by every goDecl of one parse
	index  *declIndex      // shared by every goDecl of one parse
}

// declIndex holds every top-level function and variable of a parsed
// package, exported or not, by name, with its file's local import names:
// the graph hostReach walks.
type declIndex struct {
	funcs, vars map[string][]reachDecl
}

// reachDecl is one declaration that hostReach may visit.
type reachDecl struct {
	node   ast.Node
	locals map[string]string
}

// goDecls are the exported functions, methods and types of a package's
// non-test files, by name ("Go", "Queue.Push", "Closed").
type goDecls map[string]goDecl

// GoType is the shape of an exported type of Source, which an Ergo type
// declaration names as its Go type (`extern go …/runtime/v1.Closed type
// Closed {}`; docs/spec/stdlib.md, The body contract; STD-NOPRIV NP4c).
// The declaration states the binding, so no table keyed by an Ergo name
// says which std type is which Go type.
type GoType struct {
	// Arity is the number of its type parameters.
	Arity int
	// Struct reports a plain struct type (no alias, no embedded field),
	// whose field names are Fields, in order.
	Struct bool
	Fields []string
}

// TypeOf returns the shape of the exported type name of Source, and
// whether there is one. It reads the generated table (typeTable), so no
// check parses Source (CHK1).
func TypeOf(name string) (GoType, bool) {
	t, ok := typeTable[name]
	return t, ok
}

// goType is the shape of the exported type name of the parse.
func (d goDecls) goType(name string) (GoType, bool) {
	g, ok := d[name]
	if !ok || g.typ == nil {
		return GoType{}, false
	}
	t := GoType{Arity: g.typ.TypeParams.NumFields()}
	if fields, err := d.structFieldsOf(name, t.Arity); err == nil {
		t.Struct = true
		if len(fields) > 0 {
			t.Fields = fields
		}
	}
	return t, true
}

// StructFields returns the field names, in order, of the exported struct
// type named name in Source. A record whose declaration names a
// Go type of Source (TypeOf) has that struct's fields.
// It fails when there is no such struct type.
func StructFields(name string) ([]string, error) {
	return StructFieldsOf(name, 0)
}

// StructFieldsOf is StructFields for a declaration with arity type
// parameters. A generic std storage record binds a generic Go record;
// matching arity prevents accidentally binding an instance to a plain type.
func StructFieldsOf(name string, arity int) ([]string, error) {
	decls, err := sourceDecls()
	if err != nil {
		return nil, err
	}
	return decls.structFieldsOf(name, arity)
}

func (d goDecls) structFields(name string) ([]string, error) {
	return d.structFieldsOf(name, 0)
}

func (d goDecls) structFieldsOf(name string, arity int) ([]string, error) {
	g, ok := d[name]
	if !ok || g.typ == nil {
		return nil, fmt.Errorf("runtime/v1: no exported Go type %s", name)
	}
	st, ok := g.typ.Type.(*ast.StructType)
	if !ok || g.typ.Assign.IsValid() || g.typ.TypeParams.NumFields() != arity {
		return nil, fmt.Errorf("runtime/v1: %s is not a non-generic struct type", name)
	}
	fields := []string{}
	for _, f := range st.Fields.List {
		if len(f.Names) == 0 {
			return nil, fmt.Errorf("runtime/v1: %s embeds a field", name)
		}
		for _, n := range f.Names {
			fields = append(fields, n.Name)
		}
	}
	return fields, nil
}

// parseDecls parses every non-test Go file at the root of fsys, runtime/v1's
// own package (or a fixture of it), whose hidden types are unqualified.
func parseDecls(fsys fs.FS) (goDecls, error) { return parseDeclsIn(fsys, true) }

// parseDeclsIn parses every non-test Go file at the root of fsys: v1's own
// package when own, or another package, which names v1's hidden types
// through its import of runtime/v1.
func parseDeclsIn(fsys fs.FS, own bool) (goDecls, error) {
	names, err := fs.Glob(fsys, "*.go")
	if err != nil {
		return nil, err
	}
	decls := goDecls{}
	values := map[string]bool{}
	index := &declIndex{funcs: map[string][]reachDecl{}, vars: map[string][]reachDecl{}}
	fset := token.NewFileSet()
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		// Comments carry the PureMark.
		f, err := parser.ParseFile(fset, name, data, parser.SkipObjectResolution|parser.ParseComments)
		if err != nil {
			return nil, err
		}
		q := quals{}
		if own {
			q.v1 = "."
		}
		var imports []string
		locals := map[string]string{}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			imports = append(imports, p)
			local := importName(p)
			if imp.Name != nil {
				local = imp.Name.Name
			}
			locals[local] = p
			if p == rtPath {
				q.rt = local
			}
			if p == v1Path && !own {
				q.v1 = local
			}
		}
		for _, d := range f.Decls {
			if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.TYPE {
				for _, s := range gd.Specs {
					if ts := s.(*ast.TypeSpec); ts.Name.IsExported() {
						decls[ts.Name.Name] = goDecl{typ: ts, q: q, imports: imports, locals: locals, values: values, index: index}
					}
				}
				continue
			}
			if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.VAR {
				for _, s := range gd.Specs {
					for _, n := range s.(*ast.ValueSpec).Names {
						values[n.Name] = true
						index.vars[n.Name] = append(index.vars[n.Name], reachDecl{s, locals})
					}
				}
				continue
			}
			fn, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if fn.Recv == nil {
				values[fn.Name.Name] = true
				index.funcs[fn.Name.Name] = append(index.funcs[fn.Name.Name], reachDecl{fn, locals})
			}
			if !fn.Name.IsExported() {
				continue
			}
			key := fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) == 1 {
				key = recvName(fn.Recv.List[0].Type) + "." + key
			}
			decls[key] = goDecl{fn: fn, q: q, imports: imports, locals: locals, values: values, index: index}
		}
	}
	return decls, nil
}

// importName is the name an import of path gives its package with no
// name of its own: the last element, or the one before a major version
// element ("rand" for math/rand/v2), as Go's packages declare.
func importName(path string) string {
	elems := strings.Split(path, "/")
	last := elems[len(elems)-1]
	if len(elems) > 1 && len(last) > 1 && last[0] == 'v' && strings.Trim(last[1:], "0123456789") == "" {
		return elems[len(elems)-2]
	}
	return last
}

// recvName is the base type name of a receiver: Queue for *Queue[T].
func recvName(e ast.Expr) string {
	for {
		switch x := e.(type) {
		case *ast.StarExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.IndexListExpr:
			e = x.X
		case *ast.Ident:
			return x.Name
		default:
			return ""
		}
	}
}

func (d goDecls) body(name string) (Body, error) {
	g, ok := d[name]
	if !ok || g.fn == nil {
		return Body{}, fmt.Errorf("runtime/v1: no exported Go body %s", name)
	}
	b, err := goShape(g.fn.Type, g.q)
	if err != nil {
		return Body{}, fmt.Errorf("runtime/v1: %s: %w", name, err)
	}
	if b.TypeParams, err = d.typeParams(g.fn); err != nil {
		return Body{}, fmt.Errorf("runtime/v1: %s: %w", name, err)
	}
	b.Host = marked(g.fn, HostMark)
	if marked(g.fn, PureMark) {
		if b.Host {
			return Body{}, fmt.Errorf("runtime/v1: %s is marked both %s and %s", name, PureMark, HostMark)
		}
		if why := pureRefusal(g, b); why != "" {
			return Body{}, fmt.Errorf("runtime/v1: %s is marked %s, but %s", name, PureMark, why)
		}
		b.Pure = true
	}
	if !b.Host {
		if via := hostReach(g); via != "" {
			return Body{}, fmt.Errorf("runtime/v1: %s reaches host state (%s) but is not marked %s", name, via, HostMark)
		}
	}
	if owned := ownedForm(g.fn); owned != "" {
		o, ok := d[owned]
		if !ok || o.fn == nil || o.fn.Recv != nil || g.fn.Recv != nil {
			return Body{}, fmt.Errorf("runtime/v1: %s names the owned form %s, which is no exported function", name, owned)
		}
		ob, err := goShape(o.fn.Type, o.q)
		if err != nil {
			return Body{}, fmt.Errorf("runtime/v1: %s: %w", owned, err)
		}
		if ob.TypeParams, err = d.typeParams(o.fn); err != nil {
			return Body{}, fmt.Errorf("runtime/v1: %s: %w", owned, err)
		}
		if len(b.Params) == 0 || !sameShape(b, ob) {
			return Body{}, fmt.Errorf("runtime/v1: the owned form %s of %s has another shape", owned, name)
		}
		b.Owned = owned
	}
	if err := policy(g.fn, &b); err != nil {
		return Body{}, fmt.Errorf("runtime/v1: %s: %w", name, err)
	}
	return b, nil
}

// policy reads the calling-policy directives of fn's doc comment into b
// (WaitMark, ReleaseMark, BridgeMark): each at most once, a known kind, and
// a body whose policy takes the caller's task takes it as a hidden
// argument.
func policy(fn *ast.FuncDecl, b *Body) error {
	if fn.Doc == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, c := range fn.Doc.List {
		for _, mark := range []string{WaitMark, ReleaseMark, BridgeMark} {
			arg, ok := strings.CutPrefix(c.Text, mark)
			if !ok {
				continue
			}
			if seen[mark] {
				return fmt.Errorf("%s is written twice", strings.TrimSpace(mark))
			}
			seen[mark] = true
			fields := strings.Fields(arg)
			switch mark {
			case WaitMark:
				caller, known := WaitKinds[arg]
				if len(fields) != 1 || !known {
					return fmt.Errorf("%s%s names no wait kind (%s)", WaitMark, arg, kindList(WaitKinds))
				}
				if caller && !slices.Contains(b.Hidden, CallerTask) {
					return fmt.Errorf("it waits by %s on the caller's task but takes no hidden *rt.Task", arg)
				}
				b.Wait = arg
			case ReleaseMark:
				caller, known := ReleaseRoutes[arg]
				if len(fields) != 1 || !known {
					return fmt.Errorf("%s%s names no release route (%s)", ReleaseMark, arg, kindList(ReleaseRoutes))
				}
				if caller && !slices.Contains(b.Hidden, CallerTask) {
					return fmt.Errorf("it releases on the caller's task but takes no hidden *rt.Task")
				}
				b.Release = arg
			case BridgeMark:
				if len(fields) != 2 {
					return fmt.Errorf("%s takes a Go target and a provider", strings.TrimSpace(BridgeMark))
				}
				b.BridgeTarget, b.BridgeProvider = fields[0], fields[1]
			}
		}
	}
	if (b.Release != "" || b.BridgeTarget != "") && b.Wait == "" {
		return fmt.Errorf("it states a release or a bridge but no %s", strings.TrimSpace(WaitMark))
	}
	return nil
}

// kindList is the sorted keys of kinds, for a refusal.
func kindList(kinds map[string]bool) string {
	return strings.Join(slices.Sorted(maps.Keys(kinds)), ", ")
}

// sameShape reports whether a and b have one Go shape, with no function
// parameter.
func sameShape(a, b Body) bool {
	if !slices.Equal(a.TypeParams, b.TypeParams) || !slices.Equal(a.Hidden, b.Hidden) || !slices.Equal(a.Results, b.Results) || len(a.Params) != len(b.Params) {
		return false
	}
	for i, p := range a.Params {
		q := b.Params[i]
		if p.Type != q.Type || p.Func != nil || p.Funcs != nil || q.Func != nil || q.Funcs != nil {
			return false
		}
	}
	return true
}

// ownedForm is the name an //ergo:owned directive of fn's doc comment
// gives, or "".
func ownedForm(fn *ast.FuncDecl) string {
	if fn.Doc == nil {
		return ""
	}
	for _, c := range fn.Doc.List {
		if name, ok := strings.CutPrefix(c.Text, OwnedMark); ok {
			return strings.TrimSpace(name)
		}
	}
	return ""
}

// marked reports a function whose doc comment holds the directive line.
func marked(fn *ast.FuncDecl, directive string) bool {
	if fn.Doc == nil {
		return false
	}
	for _, c := range fn.Doc.List {
		if c.Text == directive {
			return true
		}
	}
	return false
}

// hostReach names the first host member (hostMember) that the body g
// reaches, or "". It walks the body and every declaration of the package
// the body names, at any depth: a top-level function or variable by its
// name, and a method by its name on any receiver, since the walk has no
// types (so it may visit more than the call reaches, never less). A
// selector on an import names a member of that package. Calls into
// runtime/rt and through a function or interface value are not followed:
// the declaration is the claim there (a Sleeper's Sleep).
func hostReach(g goDecl) string {
	seen := map[ast.Node]bool{}
	queue := []reachDecl{{g.fn, g.locals}}
	via := ""
	for len(queue) > 0 && via == "" {
		d := queue[0]
		queue = queue[1:]
		if seen[d.node] {
			continue
		}
		seen[d.node] = true
		var visit func(n ast.Node) bool
		visit = func(n ast.Node) bool {
			if via != "" {
				return false
			}
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if id, ok := x.X.(*ast.Ident); ok {
					if p, ok := d.locals[id.Name]; ok {
						if hostMember(p, x.Sel.Name) {
							via = p + "." + x.Sel.Name
						}
						return false
					}
				}
				ast.Inspect(x.X, visit)
				return false
			case *ast.Ident:
				if g.index != nil {
					queue = append(queue, g.index.funcs[x.Name]...)
					queue = append(queue, g.index.vars[x.Name]...)
				}
			}
			return true
		}
		ast.Inspect(d.node, visit)
	}
	return via
}

// pureRefusal is why the marked body g, of shape b, may not be pure, or "".
// The mark is a reviewed claim; these rules are its mechanical part. The
// body is a top-level function that takes no hidden argument and no
// function. Its file imports only PureImports and the packages of
// PureImportHostMembers, and the body names none of those packages'
// host-reading members, so no host state is in reach. Its body names no function or variable of this package at top
// level, so it reaches no other body and no package state; and it starts no
// goroutine and uses no channel.
func pureRefusal(g goDecl, b Body) string {
	if g.fn.Recv != nil {
		return "it is a method; only a function may be pure"
	}
	for _, p := range g.imports {
		if _, guarded := PureImportHostMembers[p]; !slices.Contains(PureImports, p) && !guarded {
			return fmt.Sprintf("its file imports %s, which is not in %v", p, PureImports)
		}
	}
	for local, p := range g.locals {
		if local == "." && len(PureImportsExcept[p]) > 0 {
			return fmt.Sprintf("its file dot-imports %s, whose members %v reach the host", p, PureImportsExcept[p])
		}
	}
	if len(b.Hidden) > 0 {
		return fmt.Sprintf("it takes the hidden %s parameter", b.Hidden[0])
	}
	for i, p := range b.Params {
		if p.Func != nil {
			return fmt.Sprintf("its parameter %d is a function, whose code is unknown", i+1)
		}
	}
	var host []string
	for _, p := range g.imports {
		host = append(host, PureImportHostMembers[p]...)
	}
	why := ""
	var visit func(n ast.Node) bool
	visit = func(n ast.Node) bool {
		if why != "" {
			return false
		}
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if slices.Contains(host, x.Sel.Name) {
				why = fmt.Sprintf("its body names %s, which reads host state (PureImportHostMembers)", x.Sel.Name)
				return false
			}
			// x.Sel names a field, a method or an imported package's
			// member, never a top-level name of this package; x.X is
			// walked by the same rules, nested selectors included.
			if id, ok := x.X.(*ast.Ident); ok && slices.Contains(PureImportsExcept[g.locals[id.Name]], x.Sel.Name) {
				why = fmt.Sprintf("its body names %s.%s, which reaches the host (PureImportsExcept)", id.Name, x.Sel.Name)
				return false
			}
			ast.Inspect(x.X, visit)
			return false
		default:
			return pureNode(n, g.values, &why)
		}
	}
	ast.Inspect(g.fn.Body, visit)
	return why
}

// pureNode sets why when n breaks the body rule of pureRefusal, and
// reports whether to inspect n's children. An identifier that a local
// declaration shadows is refused too: the rule is conservative.
func pureNode(n ast.Node, values map[string]bool, why *string) bool {
	switch x := n.(type) {
	case *ast.Ident:
		if values[x.Name] {
			*why = fmt.Sprintf("its body names %s, a top-level function or variable of the package", x.Name)
		}
	case *ast.GoStmt:
		*why = "its body starts a goroutine"
	case *ast.SendStmt, *ast.SelectStmt:
		*why = "its body uses a channel"
	case *ast.UnaryExpr:
		if x.Op == token.ARROW {
			*why = "its body uses a channel"
		}
	}
	return *why == ""
}

// typeParams are a function's own type parameters, or a method's: the
// names its receiver gives them, with the constraints of the receiver
// type's declaration.
func (d goDecls) typeParams(fn *ast.FuncDecl) ([]TypeParam, error) {
	if fn.Recv == nil || len(fn.Recv.List) != 1 {
		return fieldParams(fn.Type.TypeParams, nil), nil
	}
	recv := fn.Recv.List[0].Type
	base := recvName(recv)
	g, ok := d[base]
	if !ok || g.typ == nil {
		return nil, fmt.Errorf("the receiver type %s is not declared", base)
	}
	if star, ok := recv.(*ast.StarExpr); ok {
		recv = star.X
	}
	var names []string
	switch x := recv.(type) {
	case *ast.IndexExpr:
		names = append(names, types.ExprString(x.Index))
	case *ast.IndexListExpr:
		for _, ix := range x.Indices {
			names = append(names, types.ExprString(ix))
		}
	}
	decl := fieldParams(g.typ.TypeParams, nil)
	if len(names) != len(decl) {
		return nil, fmt.Errorf("the receiver names %d type parameters of %s's %d", len(names), base, len(decl))
	}
	return fieldParams(g.typ.TypeParams, names), nil
}

// fieldParams lists a type-parameter list, renamed by names when given.
func fieldParams(list *ast.FieldList, names []string) []TypeParam {
	if list == nil {
		return nil
	}
	var out []TypeParam
	for _, f := range list.List {
		for _, n := range f.Names {
			name := n.Name
			if names != nil {
				name = names[len(out)]
			}
			out = append(out, TypeParam{Name: name, Constraint: types.ExprString(f.Type)})
		}
	}
	return out
}

// goShape reads a Go function type under the hidden-argument convention.
func goShape(ft *ast.FuncType, q quals) (Body, error) {
	var b Body
	next := 0 // the index in hiddenOrder a further hidden parameter may take
	for _, field := range ft.Params.List {
		n := max(len(field.Names), 1)
		for range n {
			h, hidden := hiddenOf(field.Type, q)
			switch {
			case hidden && len(b.Params) > 0:
				return Body{}, fmt.Errorf("the hidden %s parameter follows an Ergo parameter", h)
			case hidden:
				i := slices.Index(hiddenOrder, h)
				if i < next {
					return Body{}, fmt.Errorf("the hidden %s parameter is out of order or repeated (order: %v)", h, hiddenOrder)
				}
				next = i + 1
				b.Hidden = append(b.Hidden, h)
			default:
				p := Param{Type: types.ExprString(field.Type)}
				if inner, ok := field.Type.(*ast.FuncType); ok {
					fb, err := goShape(inner, q)
					if err != nil {
						return Body{}, fmt.Errorf("function parameter %d: %w", len(b.Params)+1, err)
					}
					p.Func = &fb
				}
				if at, ok := field.Type.(*ast.ArrayType); ok && at.Len == nil {
					if inner, ok := at.Elt.(*ast.FuncType); ok {
						fb, err := goShape(inner, q)
						if err != nil {
							return Body{}, fmt.Errorf("function slice parameter %d: %w", len(b.Params)+1, err)
						}
						p.Funcs = &fb
					}
				}
				b.Params = append(b.Params, p)
			}
		}
	}
	if ft.Results != nil {
		for _, field := range ft.Results.List {
			for range max(len(field.Names), 1) {
				b.Results = append(b.Results, types.ExprString(field.Type))
			}
		}
	}
	return b, nil
}

// quals are the names a file gives runtime/rt and runtime/v1: rt is its
// local name of runtime/rt or ""; v1 is "." in runtime/v1's own files,
// whose hidden types are unqualified, its local name of runtime/v1 in
// another package's file, or "" in a file that imports neither.
type quals struct{ rt, v1 string }

// v1Type reports whether e names runtime/v1's type name as the file
// writes it: name in v1's own files, v1.name in another package's.
func (q quals) v1Type(e ast.Expr, name string) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return q.v1 == "." && x.Name == name
	case *ast.SelectorExpr:
		id, ok := x.X.(*ast.Ident)
		return ok && q.v1 != "" && q.v1 != "." && id.Name == q.v1 && x.Sel.Name == name
	}
	return false
}

// hiddenOf names the hidden parameter whose Go type e is: *Scope, Site,
// *rt (the file's name for runtime/rt).Task, Sleeper, ContextPackage or
// Releaser[T], v1's types qualified as the file names v1 (quals).
func hiddenOf(e ast.Expr, q quals) (Hidden, bool) {
	switch x := e.(type) {
	case *ast.IndexExpr:
		if q.v1Type(x.X, "Releaser") {
			return ResultRelease, true
		}
	case *ast.StarExpr:
		if q.v1Type(x.X, "Scope") {
			return Frame, true
		}
		if y, ok := x.X.(*ast.SelectorExpr); ok {
			if id, ok := y.X.(*ast.Ident); ok && q.rt != "" && id.Name == q.rt && y.Sel.Name == "Task" {
				return CallerTask, true
			}
		}
	default:
		for name, h := range map[string]Hidden{"Site": CallSite, "Sleeper": CallerScheduler, "ContextPackage": ReadingPackage} {
			if q.v1Type(e, name) {
				return h, true
			}
		}
	}
	return "", false
}
