package check

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/std/gostd"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Go imports (#202, docs/spec/interop.md). `import go net/url` reads the
// binding of net/url: an Ergo file of extern go declarations that ergo bind
// go generated from go/types. The checker never runs a Go tool (plan §2,
// option C): Go's standard library is embedded (gostd), and a module's own
// bindings come from the importer (GoImporter).
//
// A binding is read lazily, one declaration at a time. The import makes an
// empty package; the first use of a member parses the file and makes a
// shell of each declaration (a TypeName, a Func, a Var, with no type);
// the member used is resolved, and a host type's underlying type resolves
// when it is first read (Named.underlying). A program that imports no Go
// package reads no binding, and one that names url.Parse resolves Parse's
// signature and the types it names, not the rest of net/url.
//
// Each binding has its own checker (the binder), which resolves its type
// expressions in the binding's file scope. A binder's diagnostic means the
// binding is wrong; the program reports it at its import (checkGoImports).
//
// Prior art: Swift's Clang importer and Kotlin/Native's cinterop read a
// generated summary of the host library; Borgo commits generated .brg
// declaration files; Gleam and ReScript externals trust a written type.

// GoBindings is the set of Go packages one program imports: one package per
// Go import path, so a host type has one identity across the program's
// packages.
type GoBindings struct {
	lookup func(path string) (string, bool)
	pkgs   map[string]*Package
	// Stale, when set, is why the binding of a Go import path that lookup
	// found no longer matches the module's go.mod (IG7: the loader compares
	// the binding's stamp with go.mod, reading files only); "" when it
	// matches. The import is then S104 with the command that rewrites it.
	Stale func(path string) string
}

// NewGoBindings returns the Go packages read through lookup, which returns
// the binding source of a Go import path; nil reads Go's standard library
// (gostd).
func NewGoBindings(lookup func(path string) (string, bool)) *GoBindings {
	if lookup == nil {
		lookup = gostd.Binding
	}
	return &GoBindings{lookup: lookup, pkgs: map[string]*Package{}}
}

// GoImporter is an Importer that also resolves Go imports. An importer
// without it gets Go's standard library only, one GoBindings per Check.
type GoImporter interface {
	Importer
	GoBindings() *GoBindings
}

// goImp is a binder's importer: a binding imports Go packages only.
type goImp struct{ set *GoBindings }

func (g goImp) Import(path string) (*Package, error) {
	return nil, fmt.Errorf("a binding imports Go packages only (%s)", path)
}

func (g goImp) GoBindings() *GoBindings { return g.set }

// goBindings is the program's Go packages.
func (c *checker) goBindings() *GoBindings {
	if gi, ok := c.imp.(GoImporter); ok {
		return gi.GoBindings()
	}
	if c.ownGo == nil {
		c.ownGo = NewGoBindings(nil)
	}
	return c.ownGo
}

// importGo resolves `import go path` to the binding of path. The path
// follows Go's rule (GoImportPathError, checked first by importSpec); a
// path with no binding is S104 at
// the import, with the command that writes one. Nothing of the binding is
// read here.
func (c *checker) importGo(fi *fileInfo, spec *syntax.ImportSpec, path string) {
	name := GoPackageName(path)
	if spec.Name != nil {
		name = spec.Name.Name
	}
	for _, elem := range strings.Split(path, "/") {
		if elem == "internal" || elem == "vendor" {
			c.errorf(spec.Path, CodeOutside, "import go %q: Go admits an %s package only inside the tree that holds it, and the emitted program is outside every tree (docs/spec/interop.md)", path, elem)
			c.bindBrokenImportName(fi, spec, path, name)
			return
		}
	}
	set := c.goBindings()
	pkg, ok := set.Package(path)
	if !ok {
		c.errorf(spec.Path, CodeUnknown, "no binding for Go package %q; run ergo bind go %s (docs/spec/interop.md)", path, path)
		c.bindBrokenImportName(fi, spec, path, name)
		return
	}
	if set.Stale != nil {
		if why := set.Stale(path); why != "" {
			c.errorf(spec.Path, CodeUnknown, "the binding of Go package %q is stale: %s; run ergo bind go %s (docs/spec/interop.md)", path, why, path)
			c.bindBrokenImportName(fi, spec, path, name)
			return
		}
	}
	if _, dup := fi.scope.names[name]; name != "_" && dup {
		c.errorf(spec, CodeType, "%s is imported twice", name)
		return
	}
	if spec.Name != nil && !c.preludeName(spec.Name) {
		c.aliases = append(c.aliases, importAlias{fi, spec, path})
	}
	if name == "_" {
		return
	}
	pn := &PkgName{Name: name, Pos: spec.Pos(), Pkg: pkg, spec: spec}
	fi.scope.insert(name, pn)
	fi.imports = append(fi.imports, pn)
}

// checkGoImports reports, at its import, a binding that does not check: a
// binding ergo bind go wrote checks clean, so this is an edited or stale
// binding.
func (c *checker) checkGoImports() {
	for _, fi := range c.files {
		c.cur = fi
		for _, pn := range fi.imports {
			h := hostOf(pn.Pkg)
			if h == nil {
				continue
			}
			msg := h.err
			if ds := h.c.sorted(); msg == "" && len(ds) > 0 {
				msg = ds[0].String()
			}
			if msg != "" {
				c.errorf(pn.spec, CodeType, "the binding of Go package %q does not check: %s; run ergo bind go %s", pn.Pkg.Path, msg, pn.Pkg.Path)
			}
		}
	}
}

// unknownMember reports pkg.Name that names no exported declaration. For a
// Go package whose binding refuses Name, the diagnostic is S109 with the
// binding's reason, at this use: one unmappable declaration never blocks
// the package (docs/spec/interop.md).
func (c *checker) unknownMember(x *syntax.SelectorExpr, pn *PkgName) {
	if h := hostOf(pn.Pkg); h != nil {
		if why := h.refusal(x.Sel.Name); why != "" {
			c.errorf(x, CodeOutside, "%s.%s is not imported from Go: %s (the binding of %s refuses it)", pn.Name, x.Sel.Name, why, pn.Pkg.Path)
			return
		}
	}
	c.errorf(x.Sel, CodeUnknown, "%s.%s is not an exported declaration of package %s", pn.Name, x.Sel.Name, pn.Pkg.Path)
}

// hostSelector is x.Sel on a value of the host type n, or on a go.Ref[n]
// (handle) or a borrow of one. A field is read-only (owner q34): the
// operand is not addressable, and a read through a handle or a borrow
// charges go.Foreign, since Go code may write that memory. A method is
// Go's: one in the method set of *T only needs a handle or a borrow, as Go
// takes no address of a copy. A method or field the binding refuses is
// S109 with its reason.
func (c *checker) hostSelector(x *syntax.SelectorExpr, base operand, n *Named, handle bool) operand {
	shared := handle || isPointer(base.typ)
	if s := asStruct(n); s != nil {
		if f, _ := s.field(x.Sel.Name); f != nil {
			if shared {
				c.charge(x, goForeign())
			}
			return operand{mode: modeValue, typ: f.Type}
		}
	}
	h := hostOf(n.Obj.Pkg)
	sig, fn := c.methodOf(n, x.Sel.Name)
	if sig != nil {
		if fn != nil && h.recvPtr[fn] && !shared {
			c.errorf(x, CodeOutside, "the Go method %s.%s has a pointer receiver; call it on a go.Ref[%s] or a borrow, not on a copy (docs/spec/interop.md)", n, x.Sel.Name, n)
			return bad(x)
		}
		var m *Method
		if it := asInterface(n); it != nil {
			m = it.method(x.Sel.Name)
		}
		if fn != nil {
			c.recordDep(fn)
		}
		if c.fn != nil && c.fn.reach != nil {
			if fn != nil {
				c.fn.reach.funcs = append(c.fn.reach.funcs, fn)
			} else {
				c.fn.reach.methodNames = append(c.fn.reach.methodNames, x.Sel.Name)
			}
		}
		// A struct's method is its Func, so the call records it (CallFunc
		// with a receiver); an interface's is its Method (CallOp).
		return operand{mode: modeValue, typ: sig, method: m, fn: fn}
	}
	if why := h.refusal(n.Obj.Name + "." + x.Sel.Name); why != "" {
		c.errorf(x.Sel, CodeOutside, "%s.%s is not imported from Go: %s (the binding of %s refuses it)", n, x.Sel.Name, why, n.Obj.Pkg.Path)
		return bad(x)
	}
	c.errorf(x.Sel, CodeUnknown, "%s has no field or method %s", base.typ, x.Sel.Name)
	return bad(x)
}

// hostPkg is the binding of one Go package.
type hostPkg struct {
	set      *GoBindings
	src      string
	c        *checker // the binder
	fi       *fileInfo
	loaded   bool
	err      string                   // the binding does not parse
	vars     map[*Var]*syntax.VarSpec // a const or error var, typed on first use
	refused  map[string]string        // a refused declaration ("F", "T.M") and its reason
	recvPtr  map[*Func]bool           // a method in the method set of *T only
	variadic map[*Func]bool           // a function whose last parameter is Go's ...T
	context  map[*Func]bool           // a function whose hidden first parameter is Go's context.Context (IG10)
	goLines  map[int]string           // the `// ergo:` comment of each line, read by GoDecl (IG8) on first use
}

func hostOf(p *Package) *hostPkg {
	if p == nil {
		return nil
	}
	return p.host
}

// Package returns the Go package at path, and whether it has a binding.
// The package is empty until a member is read (member).
func (s *GoBindings) Package(path string) (*Package, bool) {
	if p, ok := s.pkgs[path]; ok {
		return p, p != nil
	}
	src, ok := s.lookup(path)
	if !ok {
		s.pkgs[path] = nil
		return nil, false
	}
	c := newChecker(path, goImp{s}, prelude().scope)
	c.pkg.Name = GoPackageName(path)
	c.pkg.qualify = true
	c.pkg.host = &hostPkg{set: s, src: src, c: c, vars: map[*Var]*syntax.VarSpec{}, refused: map[string]string{}, recvPtr: map[*Func]bool{}, variadic: map[*Func]bool{}, context: map[*Func]bool{}}
	s.pkgs[path] = c.pkg
	return c.pkg, true
}

// Diagnostics returns the diagnostics of every binding read so far, each
// prefixed with its Go import path: none when the bindings are right.
func (s *GoBindings) Diagnostics() []string {
	var out []string
	for _, path := range sortedKeys(s.pkgs) {
		p := s.pkgs[path]
		if p == nil {
			continue
		}
		if p.host.err != "" {
			out = append(out, path+": "+p.host.err)
		}
		for _, d := range p.host.c.sorted() {
			out = append(out, path+": "+d.String())
		}
	}
	return out
}

// ResolveAll resolves every declaration of the binding at path, as a use
// of each would (the acceptance of ergo bind go: every binding checks
// clean). It reports whether the package has a binding.
func (s *GoBindings) ResolveAll(path string) bool {
	p, ok := s.Package(path)
	if !ok {
		return false
	}
	h := p.host
	h.load()
	for _, name := range sortedKeys(p.Scope.names) {
		o := p.Scope.names[name]
		h.resolve(o)
		if tn, ok := o.(*TypeName); ok {
			n := tn.Type.(*Named)
			under(n)
			for _, m := range n.mlist {
				h.c.funcSig(m)
			}
		}
	}
	return true
}

// GoPackageName is the name an import go of path binds: the Go package
// name, which for Go's standard library and Go's module conventions is the
// last path element, or the one before a major-version element (math/rand/v2
// is rand).
func GoPackageName(path string) string {
	elems := strings.Split(path, "/")
	last := elems[len(elems)-1]
	if len(elems) > 1 && len(last) > 1 && last[0] == 'v' {
		if _, err := strconv.Atoi(last[1:]); err == nil {
			return elems[len(elems)-2]
		}
	}
	return last
}

// member returns p's declaration name, read from its binding for a Go
// package (lazily: only that declaration resolves).
func (p *Package) member(name string) (Object, bool) {
	h := p.host
	if h == nil {
		o, ok := p.Scope.names[name]
		return o, ok
	}
	h.load()
	o, ok := p.Scope.names[name]
	if ok {
		h.resolve(o)
	}
	return o, ok
}

// resolve gives a declaration of the binding its type.
func (h *hostPkg) resolve(o Object) {
	switch o := o.(type) {
	case *Func:
		h.c.funcSig(o)
	case *Var:
		if o.Type == nil {
			saved := h.c.cur
			h.c.cur = h.fi
			o.Type = h.c.typExpr(h.vars[o].Type, &typeCtx{params: map[string]*TypeParam{}})
			h.c.cur = saved
		}
	}
	// A TypeName's underlying type resolves when it is read.
}

// load parses the binding and declares a shell of each declaration.
func (h *hostPkg) load() {
	if h.loaded {
		return
	}
	h.loaded = true
	c := h.c
	f, err := syntax.Parse(h.src)
	if err != nil {
		h.err = "the binding does not parse: " + err.Error()
		return
	}
	f.Name = GoPackageName(c.pkg.Path) + ".ergo"
	fi := &fileInfo{syntax: f, scope: newScope(nil)}
	h.fi = fi
	c.files = []*fileInfo{fi}
	c.pkg.files = c.files
	saved := c.cur
	c.cur = fi
	defer func() { c.cur = saved }()
	var methods []*syntax.FuncDecl
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *syntax.ImportDecl:
			for _, spec := range d.Specs {
				path, _ := strconv.Unquote(spec.Path.Value)
				if spec.Path.Kind == syntax.PATH {
					path = spec.Path.Value
				}
				name := GoPackageName(path)
				if spec.Name != nil {
					name = spec.Name.Name
				}
				pn := &PkgName{Name: name, Pos: spec.Pos(), spec: spec, used: true}
				if pkg, ok := h.set.Package(path); ok {
					pn.Pkg = pkg
				} else {
					c.errorf(spec, CodeUnknown, "no binding for Go package %q", path)
					pn.Pkg, pn.broken = &Package{Path: path, Name: name, Scope: newScope(nil)}, true
				}
				fi.scope.insert(name, pn)
				fi.imports = append(fi.imports, pn)
			}
		case *syntax.TypeDecl:
			tn := &TypeName{Name: d.Name.Name, Pkg: c.pkg, Pos: d.Name.Pos(), Decl: d, file: fi, id: newID()}
			tn.Type = &Named{Obj: tn}
			c.pkg.Scope.insert(tn.Name, tn)
			c.pkg.Types = append(c.pkg.Types, tn)
		case *syntax.FuncDecl:
			if d.Recv != nil {
				methods = append(methods, d)
				continue
			}
			fn := &Func{Name: d.Name.Name, Pkg: c.pkg, Pos: d.Name.Pos(), Decl: d, file: fi}
			c.pkg.Scope.insert(fn.Name, fn)
			c.pkg.Funcs = append(c.pkg.Funcs, fn)
		case *syntax.VarDecl:
			for _, spec := range d.Specs {
				for _, name := range spec.Names {
					v := &Var{Name: name.Name, Pos: name.Pos(), id: name, pkg: true, used: true}
					h.vars[v] = spec
					c.pkg.Scope.insert(v.Name, v)
				}
			}
		default:
			c.errorf(d, CodeOutside, "a binding declares only Go types, functions, methods and constants")
		}
	}
	for _, d := range methods {
		recv := d.Recv.List[0].Type
		star := false
		if s, ok := recv.(*syntax.StarExpr); ok {
			recv, star = s.X, true
		}
		id, _ := recv.(*syntax.Ident)
		var tn *TypeName
		if id != nil {
			tn, _ = c.pkg.Scope.names[id.Name].(*TypeName)
		}
		if tn == nil {
			c.errorf(d.Recv, CodeUnknown, "the receiver of a binding method is a type of the binding")
			continue
		}
		fn := &Func{Name: d.Name.Name, Pkg: c.pkg, Pos: d.Name.Pos(), Decl: d, file: fi}
		h.recvPtr[fn] = star
		tn.Type.(*Named).addMethod(fn)
	}
	// markers holds the line of each `// ergo:variadic` and
	// `// ergo:context` comment (Binding files 2).
	markers := map[int]string{}
	for _, g := range f.Comments {
		for _, cm := range g.List {
			if cm.Text == "// ergo:variadic" || cm.Text == "// ergo:context" {
				line, _ := f.Position(cm.Pos())
				markers[line] = cm.Text
			}
			rest, ok := strings.CutPrefix(cm.Text, "// ergo:refused ")
			if !ok {
				continue
			}
			if name, reason, ok := strings.Cut(rest, ": "); ok {
				h.refused[name] = reason
			}
		}
	}
	// The markers are the lines right above a declaration: with
	// `// ergo:variadic` the lowering spreads the last argument (xs...);
	// with `// ergo:context` it passes the task's context first (IG10).
	if len(markers) > 0 {
		mark := func(fn *Func) {
			line, _ := f.Position(fn.Decl.Pos())
			for l := line - 1; markers[l] != ""; l-- {
				switch markers[l] {
				case "// ergo:variadic":
					h.variadic[fn] = true
				case "// ergo:context":
					h.context[fn] = true
				}
			}
		}
		for _, fn := range c.pkg.Funcs {
			mark(fn)
		}
		for _, tn := range c.pkg.Types {
			for _, m := range tn.Type.(*Named).mlist {
				mark(m)
			}
		}
	}
}

// GoVariadic reports a Go function or method of import go whose last
// parameter is Go's ...T (the binding's `// ergo:variadic`).
func GoVariadic(f *Func) bool {
	h := hostOf(f.Pkg)
	return h != nil && h.variadic[f]
}

// GoContext reports a Go function or method of import go whose first Go
// parameter is context.Context, hidden in its Ergo signature (the
// binding's `// ergo:context`): the call passes the calling task's
// context, which an interruption cancels (IG10).
func GoContext(f *Func) bool {
	h := hostOf(f.Pkg)
	return h != nil && h.context[f]
}

// refusal is the binding's reason for a Go declaration it does not import
// ("F", or "T.M" for a method), or "".
func (h *hostPkg) refusal(name string) string {
	h.load()
	return h.refused[name]
}

// isHostNamed reports a type of a Go package.
func isHostNamed(t Type) bool {
	n, ok := t.(*Named)
	return ok && n.Obj != nil && n.Obj.Pkg.Host()
}

// refElem is T for go.Ref[T], else nil.
func refElem(t Type) Type {
	n, ok := t.(*Named)
	if !ok || n.Orig != universeOnce.ref || len(n.TArgs) != 1 {
		return nil
	}
	return n.TArgs[0]
}

// GoRefElem is T for go.Ref[T] (a Go *T the lowering passes as is), and
// whether t is one.
func GoRefElem(t Type) (Type, bool) {
	e := refElem(t)
	return e, e != nil
}

// goForeign is the predeclared go.Foreign.
func goForeign() Type {
	universe()
	return universeOnce.goPkg.Scope.names["Foreign"].(*TypeName).Type
}

// GoImportPathError checks path by Go's rule for an import path
// (golang.org/x/mod/module.CheckImportPath, #202; small-fix #231): it
// returns the reason path is not one, or "".
func GoImportPathError(path string) string {
	switch {
	case !utf8.ValidString(path):
		return "invalid UTF-8"
	case path == "":
		return "empty string"
	case path[0] == '-':
		return "leading dash"
	case strings.Contains(path, "//"):
		return "double slash"
	case path[len(path)-1] == '/':
		return "trailing slash"
	}
	for _, elem := range strings.Split(path, "/") {
		switch {
		case elem == "":
			return "empty path element"
		case strings.Count(elem, ".") == len(elem):
			return fmt.Sprintf("invalid path element %q", elem)
		case elem[len(elem)-1] == '.':
			return "trailing dot in path element"
		}
		for _, r := range elem {
			if !goImportPathRune(r) {
				return fmt.Sprintf("invalid char %q", r)
			}
		}
		short := elem
		if i := strings.Index(short, "."); i >= 0 {
			short = short[:i]
		}
		for _, bad := range badWindowsNames {
			if strings.EqualFold(bad, short) {
				return fmt.Sprintf("%q disallowed as path element component on Windows", short)
			}
		}
	}
	return ""
}

func goImportPathRune(r rune) bool {
	return r == '-' || r == '.' || r == '_' || r == '~' || r == '+' ||
		'0' <= r && r <= '9' || 'A' <= r && r <= 'Z' || 'a' <= r && r <= 'z'
}

var badWindowsNames = []string{
	"CON", "PRN", "AUX", "NUL",
	"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
	"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
}
