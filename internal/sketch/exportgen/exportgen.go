// Package exportgen generates the export codecs of the checker's model
// (CHK1b): internal/sketch/syntax/codec_gen.go, the codec of syntax trees,
// and internal/sketch/check/export_gen.go, the codec of everything else a
// checked std package holds. It reads the two packages with go/types and
// writes, for every type reachable from the snapshot's segment roots
// (check.stdBase and *check.Package), an encoder and a decoder: field by field, in
// declaration order, so no reflect or unsafe is needed and the generated
// code reads unexported fields as any code of the package does.
//
// Pointers are graph edges: the encoder gives each pointer an id the first
// time it meets it, and the decoder allocates the object before it decodes
// its fields, so shared objects and cycles survive. A pointer to a syntax
// type is an id in the syntax codec's table, so package check makes no
// syntax node (the provenance manifest). An interface is a tag over its
// implementations in the two packages. A function field must be nil, unless
// the reviewed list funcFields names its rebuild; sync types stay zero.
//
// TestExportCodecsAreCurrent (internal/sketch/check) fails when a generated
// file differs from what Generate writes, so a new field of the model never
// goes silently unexported.
//
// Prior art: Go's export data (cmd/compile/internal/noder, unified IR) and
// OCaml's .cmi files keep a checked package's interface; Gleam writes its
// checked modules to build/ as cache files keyed by source hash. Ergo keeps
// the whole checked graph, since the lowering and the evaluator read std
// bodies.
package exportgen

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	checkPath  = "ergo.local/prototype/internal/sketch/check"
	syntaxPath = "ergo.local/prototype/internal/sketch/syntax"
)

// skipFields are fields the codec does not carry: their zero value is the
// decoded state. Each names why.
var skipFields = map[string]string{
	// A func field rebuilt after decoding (check/export.go, afterDecode).
	"check.typeTable.substRow": "rebuilt: the owning checker's substRow",
	// Test hooks, nil outside tests (the encoder checks the others).
	"check.checker.contextLimit": "test hook",
	"check.checker.withLimit":    "test hook",
	"check.checker.nodeCheck":    "test hook",
	"check.checker.elidedAs":     "set only while an elided literal is checked",
	// The derive record (internal/sketch/derive). Std declares no plan
	// function or derive, so its record is empty and nil means the same;
	// check.ExportStd refuses a std package whose record is not empty.
	"check.Package.Derive":  "an empty derive record, nil when decoded",
	"check.checker.derive":  "the checked package's derive record (check.Package.Derive)",
	"syntax.File.Expansion": "the file's package's derive record (check.Package.Derive)",
}

// Generate returns the generated sources of the syntax codec and the check
// codec, reading the packages under root (the module's root directory).
func Generate(root string) (syntaxSrc, checkSrc []byte, err error) {
	fset := token.NewFileSet()
	imp := importer.ForCompiler(fset, "source", nil)
	files, err := parseDir(fset, filepath.Join(root, "internal/sketch/check"))
	if err != nil {
		return nil, nil, err
	}
	conf := types.Config{Importer: imp}
	checkPkg, err := conf.Check(checkPath, fset, files, nil)
	if err != nil {
		return nil, nil, err
	}
	var syntaxPkg *types.Package
	for _, p := range checkPkg.Imports() {
		if p.Path() == syntaxPath {
			syntaxPkg = p
		}
	}
	if syntaxPkg == nil {
		return nil, nil, fmt.Errorf("exportgen: check does not import syntax")
	}
	g := &gen{check: checkPkg, syntax: syntaxPkg}
	g.collectNamed()
	// The syntax codec: every node, and what nodes hold.
	sg := g.newEmitter(syntaxPkg)
	node := syntaxPkg.Scope().Lookup("Node").Type().Underlying().(*types.Interface)
	for _, t := range g.implementations(node, syntaxPkg) {
		sg.require(t)
	}
	if syntaxSrc, err = sg.output(); err != nil {
		return nil, nil, err
	}
	cg := g.newEmitter(checkPkg)
	// The roots: a snapshot's base segment and each package segment.
	for _, name := range []string{"stdBase", "Package"} {
		root := checkPkg.Scope().Lookup(name)
		if root == nil {
			return nil, nil, fmt.Errorf("exportgen: check has no %s", name)
		}
		cg.require(types.NewPointer(root.Type()))
	}
	if checkSrc, err = cg.output(); err != nil {
		return nil, nil, err
	}
	return syntaxSrc, checkSrc, nil
}

func parseDir(fset *token.FileSet, dir string) ([]*ast.File, error) {
	names, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var files []*ast.File
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "export_gen.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			return nil, err
		}
		f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	// export_gen.go is skipped above: the generated codec must not change
	// what it is generated from. A stub gives check's export.go the names it
	// calls, so the package type-checks without the generated file.
	stub, err := parser.ParseFile(fset, "export_stub.go", "package check\n\nfunc (e *encoder) encPtrstdBase(v *stdBase) {}\n\nfunc (d *decoder) decPtrstdBase() *stdBase { return nil }\n\nfunc (e *encoder) encPtrPackage(v *Package) {}\n\nfunc (d *decoder) decPtrPackage() *Package { return nil }\n", 0)
	if err != nil {
		return nil, err
	}
	return append(files, stub), nil
}

type gen struct {
	check, syntax *types.Package
	named         map[*types.Package][]*types.Named
}

func (g *gen) collectNamed() {
	g.named = map[*types.Package][]*types.Named{}
	for _, p := range []*types.Package{g.check, g.syntax} {
		scope := p.Scope()
		for _, name := range scope.Names() {
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok || tn.IsAlias() {
				continue
			}
			if n, ok := tn.Type().(*types.Named); ok && n.TypeParams().Len() == 0 {
				g.named[p] = append(g.named[p], n)
			}
		}
	}
}

// implementations are the concrete types of the two packages that
// implement it, a pointer type when only the pointer does, sorted by name.
func (g *gen) implementations(it *types.Interface, pkgs ...*types.Package) []types.Type {
	var out []types.Type
	for _, p := range pkgs {
		for _, n := range g.named[p] {
			if types.IsInterface(n) {
				continue
			}
			switch {
			case types.Implements(n, it) && !isStructType(n):
				out = append(out, n)
			case types.Implements(types.NewPointer(n), it):
				if _, isPtr := n.Underlying().(*types.Pointer); isPtr {
					continue
				}
				out = append(out, types.NewPointer(n))
			}
		}
	}
	return out
}

func isStructType(t types.Type) bool {
	_, ok := t.Underlying().(*types.Struct)
	return ok
}

// emitter writes the codec of one package.
type emitter struct {
	g     *gen
	pkg   *types.Package
	names map[string]string // type key -> function suffix
	queue []types.Type
	done  map[string]bool
	enc   bytes.Buffer
	dec   bytes.Buffer
	err   error
	seq   int
	// tableTypes are the pointer types of the syntax codec's table.
	tableTypes []*types.Pointer
	// imports are the packages the generated code names, path to name.
	imports map[string]string
}

func (g *gen) newEmitter(p *types.Package) *emitter {
	return &emitter{g: g, pkg: p, names: map[string]string{}, done: map[string]bool{}, imports: map[string]string{}}
}

func (e *emitter) qual(p *types.Package) string {
	if p == e.pkg {
		return ""
	}
	e.imports[p.Path()] = p.Name()
	return p.Name()
}

func (e *emitter) typeString(t types.Type) string { return types.TypeString(t, e.qual) }

func (e *emitter) key(t types.Type) string {
	return types.TypeString(t, func(p *types.Package) string { return p.Path() })
}

func (e *emitter) fail(format string, args ...any) {
	if e.err == nil {
		e.err = fmt.Errorf("exportgen: "+format, args...)
	}
}

// local reports a named type of the emitter's package.
func (e *emitter) local(n *types.Named) bool { return n.Obj().Pkg() == e.pkg }

// isSyntax reports a type of package syntax.
func (e *emitter) isSyntax(n *types.Named) bool { return n.Obj().Pkg() == e.g.syntax }

// table reports whether a pointer to t is an id in the syntax codec's
// table, as the check codec sees it.
func (e *emitter) syntaxRef(t types.Type) bool {
	if e.pkg == e.g.syntax {
		return false
	}
	switch t := types.Unalias(t).(type) {
	case *types.Pointer:
		if n, ok := types.Unalias(t.Elem()).(*types.Named); ok && e.isSyntax(n) && isStructType(n) {
			return true
		}
	case *types.Named:
		if e.isSyntax(t) && types.IsInterface(t) {
			return true
		}
	}
	return false
}

// fn is the function suffix of t, queueing t for emission.
func (e *emitter) fn(t types.Type) string {
	t = types.Unalias(t)
	k := e.key(t)
	if s, ok := e.names[k]; ok {
		return s
	}
	var s string
	switch t := t.(type) {
	case *types.Named:
		s = t.Obj().Name()
		if t.Obj().Pkg() == nil {
			s = "Builtin_" + s
		} else if !e.local(t) {
			s = exportName(t.Obj().Pkg().Name()) + "_" + s
		}
	case *types.Pointer:
		if n, ok := types.Unalias(t.Elem()).(*types.Named); ok && e.syntaxRef(t) {
			s = "PtrSyntax_" + n.Obj().Name()
		} else if ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == "math/big" {
			s = "PtrBig" + n.Obj().Name()
		} else if ok {
			s = "Ptr" + e.fn(n)
		} else {
			e.seq++
			s = fmt.Sprintf("T%d", e.seq)
		}
	default:
		e.seq++
		s = fmt.Sprintf("T%d", e.seq)
	}
	e.names[k] = s
	e.queue = append(e.queue, t)
	return s
}

func exportName(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (e *emitter) require(t types.Type) { e.fn(t) }

func (e *emitter) output() ([]byte, error) {
	for len(e.queue) > 0 {
		t := e.queue[0]
		e.queue = e.queue[1:]
		k := e.key(t)
		if e.done[k] {
			continue
		}
		e.done[k] = true
		e.emit(t)
	}
	if e.pkg == e.g.syntax {
		e.emitTable()
	}
	if e.err != nil {
		return nil, e.err
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "// Code generated by internal/sketch/exportgen; DO NOT EDIT.\n// Rewrite it with: go test ./internal/sketch/check -run TestExportCodecsAreCurrent -update-export\n\npackage %s\n\n", e.pkg.Name())
	if len(e.imports) > 0 {
		var ps []string
		for p := range e.imports {
			ps = append(ps, p)
		}
		sort.Strings(ps)
		b.WriteString("import (\n")
		for _, p := range ps {
			if name := e.imports[p]; name != p[strings.LastIndex(p, "/")+1:] {
				fmt.Fprintf(&b, "\t%s %q\n", name, p)
				continue
			}
			fmt.Fprintf(&b, "\t%q\n", p)
		}
		b.WriteString(")\n\n")
	}
	b.Write(e.enc.Bytes())
	b.Write(e.dec.Bytes())
	out, err := format.Source(b.Bytes())
	if err != nil {
		return b.Bytes(), fmt.Errorf("exportgen: format %s: %v", e.pkg.Name(), err)
	}
	return out, nil
}

// emit writes encT and decT for t.
func (e *emitter) emit(t types.Type) {
	name := e.fn(t)
	ts := e.typeString(t)
	switch u := t.(type) {
	case *types.Pointer:
		e.emitPointer(name, ts, u)
		return
	case *types.Named:
		if types.IsInterface(u) {
			e.emitInterface(name, ts, u)
			return
		}
		if st, ok := u.Underlying().(*types.Struct); ok {
			e.emitStruct(name, ts, u, st)
			return
		}
		// A defined type over a basic or composite type.
		under := u.Underlying()
		if b, ok := under.(*types.Basic); ok {
			fmt.Fprintf(&e.enc, "func (e *encoder) enc%s(v %s) { %s }\n\n", name, ts, e.basicEnc(b, "v"))
			fmt.Fprintf(&e.dec, "func (d *decoder) dec%s() %s { return %s(%s) }\n\n", name, ts, ts, e.basicDec(b))
			return
		}
		inner := e.fn(under)
		fmt.Fprintf(&e.enc, "func (e *encoder) enc%s(v %s) { e.enc%s(%s(v)) }\n\n", name, ts, inner, e.typeString(under))
		fmt.Fprintf(&e.dec, "func (d *decoder) dec%s() %s { return %s(d.dec%s()) }\n\n", name, ts, ts, inner)
		return
	case *types.Slice:
		el := e.fn(u.Elem())
		fmt.Fprintf(&e.enc, "func (e *encoder) enc%s(v %s) {\n\tif v == nil {\n\t\te.uint(0)\n\t\treturn\n\t}\n\te.uint(uint64(len(v)) + 1)\n\tfor _, x := range v {\n\t\te.enc%s(x)\n\t}\n}\n\n", name, ts, el)
		fmt.Fprintf(&e.dec, "func (d *decoder) dec%s() %s {\n\tn := d.uint()\n\tif n == 0 {\n\t\treturn nil\n\t}\n\tv := make(%s, n-1)\n\tfor i := range v {\n\t\tv[i] = d.dec%s()\n\t}\n\treturn v\n}\n\n", name, ts, ts, el)
	case *types.Array:
		el := e.fn(u.Elem())
		fmt.Fprintf(&e.enc, "func (e *encoder) enc%s(v %s) {\n\tfor _, x := range v {\n\t\te.enc%s(x)\n\t}\n}\n\n", name, ts, el)
		fmt.Fprintf(&e.dec, "func (d *decoder) dec%s() (v %s) {\n\tfor i := range v {\n\t\tv[i] = d.dec%s()\n\t}\n\treturn v\n}\n\n", name, ts, el)
	case *types.Map:
		k, v := e.fn(u.Key()), e.fn(u.Elem())
		fmt.Fprintf(&e.enc, "func (e *encoder) enc%s(v %s) {\n\tif v == nil {\n\t\te.uint(0)\n\t\treturn\n\t}\n\te.uint(uint64(len(v)) + 1)\n\tfor k, x := range v {\n\t\te.enc%s(k)\n\t\te.enc%s(x)\n\t}\n}\n\n", name, ts, k, v)
		fmt.Fprintf(&e.dec, "func (d *decoder) dec%s() %s {\n\tn := d.uint()\n\tif n == 0 {\n\t\treturn nil\n\t}\n\tv := make(%s, n-1)\n\tfor range n - 1 {\n\t\tk := d.dec%s()\n\t\tv[k] = d.dec%s()\n\t}\n\treturn v\n}\n\n", name, ts, ts, k, v)
	case *types.Basic:
		fmt.Fprintf(&e.enc, "func (e *encoder) enc%s(v %s) { %s }\n\n", name, ts, e.basicEnc(u, "v"))
		fmt.Fprintf(&e.dec, "func (d *decoder) dec%s() %s { return %s(%s) }\n\n", name, ts, ts, e.basicDec(u))
	case *types.Interface:
		if u.Empty() {
			e.emitAny(name, ts)
			return
		}
		e.fail("unnamed interface %s", ts)
	case *types.Signature:
		fmt.Fprintf(&e.enc, "func (e *encoder) enc%s(v %s) {\n\tif v != nil {\n\t\te.failf(\"a function value of type %%T is not exported\", v)\n\t}\n}\n\n", name, ts)
		fmt.Fprintf(&e.dec, "func (d *decoder) dec%s() %s { return nil }\n\n", name, ts)
	default:
		e.fail("no codec for %s (%T)", ts, t)
	}
}

func (e *emitter) basicEnc(b *types.Basic, v string) string {
	switch {
	case b.Info()&types.IsBoolean != 0:
		return fmt.Sprintf("e.bool(bool(%s))", v)
	case b.Info()&types.IsString != 0:
		return fmt.Sprintf("e.string(string(%s))", v)
	case b.Info()&types.IsUnsigned != 0:
		return fmt.Sprintf("e.uint(uint64(%s))", v)
	case b.Info()&types.IsInteger != 0:
		return fmt.Sprintf("e.int(int64(%s))", v)
	case b.Info()&types.IsFloat != 0:
		return fmt.Sprintf("e.float(float64(%s))", v)
	}
	e.fail("no codec for basic %s", b)
	return ""
}

func (e *emitter) basicDec(b *types.Basic) string {
	switch {
	case b.Info()&types.IsBoolean != 0:
		return "d.bool()"
	case b.Info()&types.IsString != 0:
		return "d.string()"
	case b.Info()&types.IsUnsigned != 0:
		return "d.uint()"
	case b.Info()&types.IsInteger != 0:
		return "d.int()"
	case b.Info()&types.IsFloat != 0:
		return "d.float()"
	}
	return ""
}

func (e *emitter) emitPointer(name, ts string, p *types.Pointer) {
	elem := types.Unalias(p.Elem())
	if n, ok := elem.(*types.Named); ok && e.pkg == e.g.syntax && e.local(n) && isStructType(n) {
		// The syntax codec's table: every object is an id, its fields
		// written once in id order (encObj).
		e.tableTypes = append(e.tableTypes, p)
		e.fn(n)
		fmt.Fprintf(&e.enc, "func (e *encoder) enc%s(v %s) {\n\tif v == nil {\n\t\te.uint(0)\n\t\treturn\n\t}\n\te.uint(e.id(v))\n}\n\n", name, ts)
		fmt.Fprintf(&e.dec, "func (d *decoder) dec%s() %s {\n\tx := d.obj(d.uint())\n\tif x == nil {\n\t\treturn nil\n\t}\n\treturn x.(%s)\n}\n\n", name, ts, ts)
		return
	}
	if e.syntaxRef(p) {
		fmt.Fprintf(&e.enc, "func (e *encoder) enc%s(v %s) {\n\tif v == nil {\n\t\te.uint(0)\n\t\treturn\n\t}\n\te.uint(e.nodes.ID(v))\n}\n\n", name, ts)
		fmt.Fprintf(&e.dec, "func (d *decoder) dec%s() %s {\n\tx := d.node()\n\tif x == nil {\n\t\treturn nil\n\t}\n\treturn x.(%s)\n}\n\n", name, ts, ts)
		return
	}
	if n, ok := elem.(*types.Named); ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == "math/big" && n.Obj().Name() == "Int" {
		fmt.Fprintf(&e.enc, "func (e *encoder) enc%s(v %s) { e.bigInt(v) }\n\n", name, ts)
		fmt.Fprintf(&e.dec, "func (d *decoder) dec%s() %s { return d.bigInt() }\n\n", name, ts)
		return
	}
	if n, ok := elem.(*types.Named); ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == "sync" {
		e.fail("a pointer to %s", ts)
		return
	}
	inner := e.fn(elem)
	// Struct elements decode in place, so a cycle back to the object finds
	// it already allocated.
	if n, ok := elem.(*types.Named); ok && isStructType(n) {
		fmt.Fprintf(&e.enc, "func (e *encoder) enc%s(v %s) {\n\tif e.ref(v, v == nil) {\n\t\treturn\n\t}\n\te.encFields%s(v)\n}\n\n", name, ts, inner)
		fmt.Fprintf(&e.dec, "func (d *decoder) dec%s() %s {\n\tx, fresh := d.ref()\n\tif !fresh {\n\t\tif x == nil {\n\t\t\treturn nil\n\t\t}\n\t\treturn x.(%s)\n\t}\n\tv := new(%s)\n\td.add(v)\n\td.decFields%s(v)\n\treturn v\n}\n\n", name, ts, ts, e.typeString(elem), inner)
		return
	}
	fmt.Fprintf(&e.enc, "func (e *encoder) enc%s(v %s) {\n\tif e.ref(v, v == nil) {\n\t\treturn\n\t}\n\te.enc%s(*v)\n}\n\n", name, ts, inner)
	fmt.Fprintf(&e.dec, "func (d *decoder) dec%s() %s {\n\tx, fresh := d.ref()\n\tif !fresh {\n\t\tif x == nil {\n\t\t\treturn nil\n\t\t}\n\t\treturn x.(%s)\n\t}\n\tv := new(%s)\n\td.add(v)\n\t*v = d.dec%s()\n\treturn v\n}\n\n", name, ts, ts, e.typeString(elem), inner)
}

// isLock reports a sync type held by value: a decoded object's lock is zero.
func isLock(t types.Type) bool {
	n, ok := types.Unalias(t).(*types.Named)
	return ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == "sync"
}

// hasLock reports a struct that holds a lock by value, at any depth.
func hasLock(st *types.Struct) bool {
	for i := 0; i < st.NumFields(); i++ {
		t := st.Field(i).Type()
		if isLock(t) {
			return true
		}
		if inner, ok := t.Underlying().(*types.Struct); ok && hasLock(inner) {
			return true
		}
	}
	return false
}

func (e *emitter) emitStruct(name, ts string, n *types.Named, st *types.Struct) {
	if !e.local(n) {
		if e.isSyntax(n) {
			e.fail("a syntax struct by value: %s", ts)
			return
		}
		e.fail("no codec for the foreign struct %s", ts)
		return
	}
	var enc, dec strings.Builder
	for i := 0; i < st.NumFields(); i++ {
		f := st.Field(i)
		key := e.pkg.Name() + "." + n.Obj().Name() + "." + f.Name()
		if _, skip := skipFields[key]; skip {
			continue
		}
		if f.Name() == "_" || isLock(f.Type()) {
			continue // a lock is zero in a decoded object
		}
		fn := e.fn(f.Type())
		fmt.Fprintf(&enc, "\te.enc%s(v.%s)\n", fn, f.Name())
		fmt.Fprintf(&dec, "\tv.%s = d.dec%s()\n", f.Name(), fn)
	}
	fmt.Fprintf(&e.enc, "func (e *encoder) encFields%s(v *%s) {\n%s}\n\n", name, ts, enc.String())
	fmt.Fprintf(&e.dec, "func (d *decoder) decFields%s(v *%s) {\n%s}\n\n", name, ts, dec.String())
	if hasLock(st) {
		return // used only through a pointer
	}
	fmt.Fprintf(&e.enc, "func (e *encoder) enc%s(v %s) { e.encFields%s(&v) }\n\n", name, ts, name)
	fmt.Fprintf(&e.dec, "func (d *decoder) dec%s() (v %s) {\n\td.decFields%s(&v)\n\treturn v\n}\n\n", name, ts, name)
}

func (e *emitter) emitInterface(name, ts string, n *types.Named) {
	if e.syntaxRef(n) {
		fmt.Fprintf(&e.enc, "func (e *encoder) enc%s(v %s) {\n\tif syntax.IsNil(v) {\n\t\te.uint(0)\n\t\treturn\n\t}\n\te.uint(e.nodes.ID(v))\n}\n\n", name, ts)
		fmt.Fprintf(&e.dec, "func (d *decoder) dec%s() %s {\n\tx := d.node()\n\tif x == nil {\n\t\treturn nil\n\t}\n\treturn x.(%s)\n}\n\n", name, ts, ts)
		return
	}
	if n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == "go/constant" && n.Obj().Name() == "Value" {
		fmt.Fprintf(&e.enc, "func (e *encoder) enc%s(v %s) { e.constant(v) }\n\n", name, ts)
		fmt.Fprintf(&e.dec, "func (d *decoder) dec%s() %s { return d.constant() }\n\n", name, ts)
		return
	}
	if n.Obj().Pkg() == nil && n.Obj().Name() == "error" {
		fmt.Fprintf(&e.enc, "func (e *encoder) enc%s(v %s) {\n\tif v != nil {\n\t\te.failf(\"an error value is not exported: %%v\", v)\n\t}\n}\n\n", name, ts)
		fmt.Fprintf(&e.dec, "func (d *decoder) dec%s() %s { return nil }\n\n", name, ts)
		return
	}
	impls := e.g.implementations(n.Underlying().(*types.Interface), e.pkg)
	if e.pkg == e.g.syntax {
		// Every syntax node is a pointer in the table: the value is its id.
		for _, t := range impls {
			if _, ok := t.(*types.Pointer); !ok {
				e.fail("syntax interface %s has the value implementation %s", ts, e.typeString(t))
			}
			e.fn(t)
		}
		fmt.Fprintf(&e.enc, "func (e *encoder) enc%s(v %s) {\n\tif IsNil(v) {\n\t\te.uint(0)\n\t\treturn\n\t}\n\te.uint(e.id(v))\n}\n\n", name, ts)
		fmt.Fprintf(&e.dec, "func (d *decoder) dec%s() %s {\n\tx := d.obj(d.uint())\n\tif x == nil {\n\t\treturn nil\n\t}\n\treturn x.(%s)\n}\n\n", name, ts, ts)
		return
	}
	e.emitSwitch(name, ts, impls)
}

// emitAny writes the codec of an empty interface: a tag over every local
// pointer to a struct the codec knows, the syntax table, and basic types.
func (e *emitter) emitAny(name, ts string) {
	var impls []types.Type
	for _, n := range e.g.named[e.pkg] {
		if isStructType(n) {
			impls = append(impls, types.NewPointer(n))
		}
	}
	for _, b := range []types.BasicKind{types.Bool, types.Int, types.Int32, types.Int64, types.String} {
		impls = append(impls, types.Typ[b])
	}
	e.emitSwitch(name, ts, impls)
}

func (e *emitter) emitSwitch(name, ts string, impls []types.Type) {
	var enc, dec strings.Builder
	enc.WriteString("\tswitch x := v.(type) {\n\tcase nil:\n\t\te.uint(0)\n")
	dec.WriteString("\tswitch d.uint() {\n\tcase 0:\n\t\treturn nil\n")
	tag := 1
	for _, t := range impls {
		fn := e.fn(t)
		fmt.Fprintf(&enc, "\tcase %s:\n\t\te.uint(%d)\n\t\te.enc%s(x)\n", e.typeString(t), tag, fn)
		fmt.Fprintf(&dec, "\tcase %d:\n\t\treturn d.dec%s()\n", tag, fn)
		tag++
	}
	if e.pkg != e.g.syntax {
		// Any syntax object rides in the syntax codec's table.
		fmt.Fprintf(&enc, "\tdefault:\n\t\tif id, ok := e.nodes.Lookup(x); ok {\n\t\t\te.uint(%d)\n\t\t\te.uint(id)\n\t\t\treturn\n\t\t}\n\t\te.failf(\"no codec for a %%T in %s\", x)\n\t}\n", tag, ts)
		fmt.Fprintf(&dec, "\tcase %d:\n\t\treturn d.node().(%s)\n\t}\n\td.failf(\"bad tag in %s\")\n\treturn nil\n", tag, ts, ts)
	} else {
		fmt.Fprintf(&enc, "\tdefault:\n\t\te.failf(\"no codec for a %%T in %s\", x)\n\t}\n", ts)
		fmt.Fprintf(&dec, "\t}\n\td.failf(\"bad tag in %s\")\n\treturn nil\n", ts)
	}
	fmt.Fprintf(&e.enc, "func (e *encoder) enc%s(v %s) {\n%s}\n\n", name, ts, enc.String())
	fmt.Fprintf(&e.dec, "func (d *decoder) dec%s() %s {\n%s}\n\n", name, ts, dec.String())
}

// emitTable writes the syntax table's dispatch: objTag (0 for a value
// that is no table object), newObj, encObj and decObj.
func (e *emitter) emitTable() {
	sort.Slice(e.tableTypes, func(i, j int) bool { return e.key(e.tableTypes[i]) < e.key(e.tableTypes[j]) })
	var tag, alloc, enc, dec strings.Builder
	for i, p := range e.tableTypes {
		ts := e.typeString(p)
		elem := e.typeString(p.Elem())
		fn := e.fn(p.Elem())
		fmt.Fprintf(&tag, "\tcase %s:\n\t\treturn %d\n", ts, i+1)
		fmt.Fprintf(&alloc, "\tcase %d:\n\t\treturn new(%s)\n", i+1, elem)
		fmt.Fprintf(&enc, "\tcase %s:\n\t\te.encFields%s(x)\n", ts, fn)
		fmt.Fprintf(&dec, "\tcase %s:\n\t\td.decFields%s(x)\n", ts, fn)
	}
	fmt.Fprintf(&e.enc, "// objTag is the table tag of x's type, or 0 when x is no table object.\nfunc objTag(x any) uint64 {\n\tswitch x.(type) {\n%s\t}\n\treturn 0\n}\n\n", tag.String())
	fmt.Fprintf(&e.enc, "// encObj writes the fields of the table object x.\nfunc (e *encoder) encObj(x any) {\n\tswitch x := x.(type) {\n%s\t}\n}\n\n", enc.String())
	fmt.Fprintf(&e.dec, "// newObj allocates a table object of the tag, or nil.\nfunc newObj(tag uint64) any {\n\tswitch tag {\n%s\t}\n\treturn nil\n}\n\n", alloc.String())
	fmt.Fprintf(&e.dec, "// decObj reads the fields of the table object x.\nfunc (d *decoder) decObj(x any) {\n\tswitch x := x.(type) {\n%s\t}\n}\n\n", dec.String())
}
