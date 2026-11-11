package v1

import (
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
}

// PureMark is the directive line that marks a body pure, in the doc
// comment of an exported top-level function.
const PureMark = "//ergo:pure"

// HostMark is the directive line that marks a body that reaches host
// state, in the doc comment of an exported function or method.
const HostMark = "//ergo:host"

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
func BodyOf(name string) (Body, error) {
	decls, err := sourceDecls()
	if err != nil {
		return Body{}, err
	}
	return decls.body(name)
}

// sourceDecls parses Source once per process.
var sourceDecls = sync.OnceValues(func() (goDecls, error) { return parseDecls(Source) })

// goDecl is one exported function, method or type of a parsed package,
// with the local name its file gives runtime/rt, the import paths of its
// file, and the package's names of functions and variables at top level.
type goDecl struct {
	fn      *ast.FuncDecl
	typ     *ast.TypeSpec
	rt      string
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

// stdRecords is the binding table of std records (docs/spec/stdlib.md,
// Types 4; std-align S3b): the key is a record's std package and name,
// "fs.NotFound", and the value is the exported struct of Source that the
// record is. The key is the package and the name, never the name alone, so
// fs.NotFound and a later exec.NotFound bind two Go types; the values are
// distinct, so no Go type is two records (TestStdRecordTable). A std record
// with no row is a declaration of the program, as strings.Halves is.
var stdRecords = map[string]string{
	"base64.Malformed":     "Base64Malformed",
	"actor.Stopped":        "ActorStopped",
	"exec.Denied":          "ExecDenied",
	"exec.Failed":          "ExecFailed",
	"exec.NotFound":        "ExecNotFound",
	"exec.Result":          "ExecResult",
	"filepath.NotRelative": "NotRelative",
	"filepath.Parts":       "Parts",
	"fs.Denied":            "Denied",
	"fs.Failed":            "Failed",
	"fs.Escapes":           "Escapes",
	"fs.Info":              "Info",
	"fs.NotFound":          "NotFound",
	"hex.Malformed":        "HexMalformed",
	"http.ListenFailed":    "HTTPListenFailed",
	"http.InvalidLimits":   "HTTPInvalidLimits",
	"http.InvalidClient":   "HTTPInvalidClient",
	"http.SendFailed":      "HTTPSendFailed",
	"http.OriginRefused":   "HTTPOriginRefused",
	"http.RedirectLimit":   "HTTPRedirectLimit",
	"http.SinkFailed":      "HTTPSinkFailed",
	"http.StatusRefused":   "HTTPStatusRefused",
	"http.Limits":          "HTTPLimits",
	"http.Field":           "HTTPField",
	"http.Response":        "HTTPResponse",
	"http.WriteFailed":     "HTTPWriteFailed",
	"json.Malformed":       "JSONMalformed",
	"toml.Malformed":       "TOMLMalformed",
	"toml.Unrepresentable": "TOMLUnrepresentable",
	"io.ReadFailed":        "ReadFailed",
	"term.NotTerminal":     "TermNotTerminal",
	"term.Window":          "TermWindow",
	"os.Exit":              "Exit",
	"queue.Closed":         "Closed",
	"rpc.Failed":           "RPCFailed",
	"rpc.Malformed":        "RPCMalformed",
	"rpc.Envelope":         "RPCEnvelope",
	"rpc.Unrepresentable":  "RPCUnrepresentable",
	"rpc.Transport":        "RPCTransport",
	"strconv.Malformed":    "Malformed",
	"strconv.OutOfRange":   "OutOfRange",
	"csv.ParseError":       "CSVParseError",
	"csv.Position":         "CSVPosition",
	"time.ParseError":      "ParseError",
	"time.Reading":         "Reading",
	"utf8.Decoded":         "Decoded",
	"timeout.Expired":      "Expired",
	"tasks.Closed":         "TasksClosed",
}

// StdRecord returns the name of the struct of Source that the record name
// of the std package pkg is, and whether the binding table has one.
func StdRecord(pkg, name string) (string, bool) {
	goName, ok := stdRecords[pkg+"."+name]
	return goName, ok
}

// StdRecords returns a copy of the binding table, keyed "pkg.Name".
func StdRecords() map[string]string { return maps.Clone(stdRecords) }

// StructFields returns the field names, in order, of the exported struct
// type named name in Source. A record type declared in a std package that
// is not an operations type, such as queue.Closed, is the Go type its row
// of the binding table names (StdRecord), so its fields are the record's.
// It fails when there is no such struct type.
func StructFields(name string) ([]string, error) {
	decls, err := sourceDecls()
	if err != nil {
		return nil, err
	}
	return decls.structFields(name)
}

func (d goDecls) structFields(name string) ([]string, error) {
	g, ok := d[name]
	if !ok || g.typ == nil {
		return nil, fmt.Errorf("runtime/v1: no exported Go type %s", name)
	}
	st, ok := g.typ.Type.(*ast.StructType)
	if !ok || g.typ.TypeParams != nil || g.typ.Assign.IsValid() {
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

// parseDecls parses every non-test Go file at the root of fsys.
func parseDecls(fsys fs.FS) (goDecls, error) {
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
		rt := ""
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
				rt = "rt"
				if imp.Name != nil {
					rt = imp.Name.Name
				}
			}
		}
		for _, d := range f.Decls {
			if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.TYPE {
				for _, s := range gd.Specs {
					if ts := s.(*ast.TypeSpec); ts.Name.IsExported() {
						decls[ts.Name.Name] = goDecl{typ: ts, rt: rt, imports: imports, locals: locals, values: values, index: index}
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
			decls[key] = goDecl{fn: fn, rt: rt, imports: imports, locals: locals, values: values, index: index}
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
	b, err := goShape(g.fn.Type, g.rt)
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
	return b, nil
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
func goShape(ft *ast.FuncType, rt string) (Body, error) {
	var b Body
	next := 0 // the index in hiddenOrder a further hidden parameter may take
	for _, field := range ft.Params.List {
		n := max(len(field.Names), 1)
		for range n {
			h, hidden := hiddenOf(field.Type, rt)
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
					fb, err := goShape(inner, rt)
					if err != nil {
						return Body{}, fmt.Errorf("function parameter %d: %w", len(b.Params)+1, err)
					}
					p.Func = &fb
				}
				if at, ok := field.Type.(*ast.ArrayType); ok && at.Len == nil {
					if inner, ok := at.Elt.(*ast.FuncType); ok {
						fb, err := goShape(inner, rt)
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

// hiddenOf names the hidden parameter whose Go type e is: *Scope, Site,
// *rt (the file's name for runtime/rt).Task, Sleeper or ContextPackage.
func hiddenOf(e ast.Expr, rt string) (Hidden, bool) {
	switch x := e.(type) {
	case *ast.Ident:
		if x.Name == "Site" {
			return CallSite, true
		}
		if x.Name == "Sleeper" {
			return CallerScheduler, true
		}
		if x.Name == "ContextPackage" {
			return ReadingPackage, true
		}
	case *ast.IndexExpr:
		if id, ok := x.X.(*ast.Ident); ok && id.Name == "Releaser" {
			return ResultRelease, true
		}
	case *ast.StarExpr:
		switch y := x.X.(type) {
		case *ast.Ident:
			if y.Name == "Scope" {
				return Frame, true
			}
		case *ast.SelectorExpr:
			if id, ok := y.X.(*ast.Ident); ok && rt != "" && id.Name == rt && y.Sel.Name == "Task" {
				return CallerTask, true
			}
		}
	}
	return "", false
}
