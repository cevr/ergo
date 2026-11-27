package facts

import (
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// File is one source file of a package, by the name a diagnostic gives it.
type File struct {
	Name   string
	Syntax *syntax.File
}

// Counts is what one Build did: the syntax nodes it visited (one walk:
// each node once), the names it resolved, and the site rows it appended
// (one per resolved name: no second pass).
type Counts struct {
	Nodes, Names, Rows int
}

// mark is what a parent node says about a child expression before the
// walk reaches it: the kind of site its name makes, and Inst.
type mark struct {
	kind SiteKind
	flag uint8
}

// target is a name the checker's Defs and Uses do not hold that the walk
// resolves from its parent: a field selected or keyed, an operation or a
// dot call's function selected, a test's name.
type target struct {
	key any
	sym Symbol
}

type builder struct {
	t     *Table
	pkg   *check.Package
	info  *check.Info
	ids   map[any]uint32
	marks map[syntax.Expr]mark
	tgts  map[*syntax.Ident]target
	calls map[syntax.Expr]*check.Call
	// owners holds, for each field and operation declared in this
	// package, its owner's handle; locals the ids of the locals,
	// parameters and type parameters of the current declaration.
	owners map[any]string
	locals []uint32
	// quals is the qualifier of each selected name whose qualifier is a
	// name; params the names a parameter list of a function, law or test
	// declares.
	quals  map[*syntax.Ident]*syntax.Ident
	params map[*syntax.Ident]bool
	// ctxPos marks the operands of a with and the embedded keys of a
	// context: a name there is a context. exported caches each imported
	// package's exported members by name.
	ctxPos   map[syntax.Expr]bool
	exported map[*check.Package]map[string]check.Object
	// decl is the current top-level declaration's symbol and handle.
	encl   uint32
	handle string
	file   uint16
	sites  []Site
	// tpUses are the uses of type parameters that no name of the
	// declaration defines (nameLocals).
	tpUses []struct{ id, off uint32 }
	// emitted holds each site's file and offset: a case's alternatives
	// (FK7a) expand into patterns that copy a written name, one copy per
	// combination, and the name is one site.
	emitted map[[2]uint32]bool
	n       Counts
	// funcNames counts the functions of each package by name, to give a
	// function of an overloaded name (196m) its receiver type in its
	// handle.
	funcNames map[*check.Package]map[string]int
	// charges and withs are what each call charges and each with's block
	// (rows.go).
	charges []charge
	withs   []withBlock
	catches []catchBlock
	// entryPkg is the package that declares each context entry a with
	// of pkg binds, by its context (rows.go).
	entryPkg map[*check.ContextEntry]*check.Package
}

// Build writes the fact tables of pkg from the checker's result, in one
// walk of its files.
func Build(pkg *check.Package, files []File) (*Table, Counts) {
	b := &builder{
		t:         &Table{Path: pkg.Path},
		pkg:       pkg,
		info:      pkg.Info,
		ids:       map[any]uint32{},
		marks:     map[syntax.Expr]mark{},
		tgts:      map[*syntax.Ident]target{},
		calls:     map[syntax.Expr]*check.Call{},
		owners:    map[any]string{},
		quals:     map[*syntax.Ident]*syntax.Ident{},
		params:    map[*syntax.Ident]bool{},
		ctxPos:    map[syntax.Expr]bool{},
		exported:  map[*check.Package]map[string]check.Object{},
		funcNames: map[*check.Package]map[string]int{},
	}
	for _, f := range files {
		b.t.Files = append(b.t.Files, f.Name)
	}
	encls := b.declare(files)
	for fi, f := range files {
		if f.Syntax == nil {
			continue
		}
		b.file = uint16(fi)
		for _, d := range f.Syntax.Decls {
			if !Written(d) {
				continue
			}
			b.encl, b.handle = encls[d], ""
			if b.encl != NoSym {
				b.handle = b.t.Symbols[b.encl].Handle
			}
			b.locals, b.tpUses = b.locals[:0], b.tpUses[:0]
			syntax.Inspect(d, b.visit)
			b.nameLocals()
		}
	}
	b.variants()
	b.ops()
	b.needs()
	b.index()
	b.callEdges()
	b.chargeEdges()
	b.machines(files, encls)
	return b.t, b.n
}

// Written reports a declaration the source holds. The parser derives
// others from it (a refined type's ParseX and its law, a codec pair's
// round-trip law, a derive bundle) and gives their names the positions of
// the source they come from; the tables hold their symbols, but no site of
// theirs, so that each site is a name in the text.
func Written(d syntax.Decl) bool {
	switch d := d.(type) {
	case *syntax.FuncDecl:
		return d.Derived == nil
	case *syntax.LawDecl:
		return d.Derived == nil && d.Pair == nil
	case *syntax.TypeDecl:
		return d.Origin == nil
	}
	return true
}

// declare gives every top-level declaration its symbol before the walk,
// so a use before the declaration names the same id, and records each
// declaration's members' owner.
func (b *builder) declare(files []File) map[syntax.Decl]uint32 {
	encls := map[syntax.Decl]uint32{}
	for fi, f := range files {
		if f.Syntax == nil {
			continue
		}
		for _, d := range f.Syntax.Decls {
			encls[d] = NoSym
			var name *syntax.Ident
			switch d := d.(type) {
			case *syntax.FuncDecl:
				name = d.Name
			case *syntax.TypeDecl:
				name = d.Name
			case *syntax.VarDecl:
				for i, nm := range d.Specs[0].Names {
					o, _ := b.info.Defs[nm].(*check.Var)
					if o == nil {
						continue
					}
					h := b.pkg.Path + "." + nm.Name
					id := b.id(h, Symbol{Handle: h, Kind: Let, Name: nm.Name, Exported: exported(nm.Name)})
					b.ids[o] = id
					b.declared(id, uint16(fi), nm)
					if i == 0 {
						encls[d] = id
					}
				}
				continue
			case *syntax.ContextDecl:
				name = d.Name
			case *syntax.LawDecl:
				name = d.Name
			case *syntax.TestDecl:
				encls[d] = b.id(b.testTarget(d))
				b.declared(encls[d], uint16(fi), d.Name)
				continue
			}
			if name == nil {
				continue
			}
			if o := b.info.Defs[name]; o != nil {
				if key, sym, ok := b.symbol(o, name, true); ok {
					encls[d] = b.id(key, sym)
					b.declared(encls[d], uint16(fi), name)
				}
			}
			if d, ok := d.(*syntax.TypeDecl); ok && encls[d] != NoSym {
				b.members(d, b.t.Symbols[encls[d]].Handle)
			}
		}
	}
	return encls
}

// members records the owner of each field, operation, variant field and
// member law of d.
func (b *builder) members(d *syntax.TypeDecl, owner string) {
	tn, _ := b.info.Defs[d.Name].(*check.TypeName)
	if tn == nil {
		return
	}
	n, _ := tn.Type.(*check.Named)
	if n == nil {
		return
	}
	switch u := n.Underlying().(type) {
	case *check.Struct:
		for _, f := range u.Fields {
			b.owners[f.Origin()] = owner
		}
	case *check.Interface:
		for _, m := range u.Methods {
			b.owners[m] = owner
		}
	case *check.Union:
		for _, v := range u.Variants {
			vn, _ := v.(*check.Named)
			if vn == nil || vn.Obj == nil || vn.Obj.Pkg != b.pkg {
				continue
			}
			if s, ok := vn.Underlying().(*check.Struct); ok {
				for _, f := range s.Fields {
					b.owners[f.Origin()] = b.pkg.Path + "." + vn.Obj.Name
				}
			}
		}
	}
	for _, l := range tn.Laws {
		b.owners[l] = owner
	}
}

func (b *builder) declared(id uint32, file uint16, name *syntax.Ident) {
	s := &b.t.Symbols[id]
	s.Declared, s.File, s.Off = true, file, uint32(name.NamePos)
}

// id is the symbol of key, added on first sight.
func (b *builder) id(key any, sym Symbol) uint32 {
	if id, ok := b.ids[key]; ok {
		return id
	}
	id := uint32(len(b.t.Symbols))
	b.ids[key] = id
	b.t.Symbols = append(b.t.Symbols, sym)
	return id
}

func (b *builder) testTarget(d *syntax.TestDecl) (any, Symbol) {
	h := b.pkg.Path + "." + d.Name.Name + "#test"
	return h, Symbol{Handle: h, Kind: Test, Name: d.Name.Name, Exported: true}
}

func (b *builder) visit(n syntax.Node) bool {
	b.n.Nodes++
	if e, ok := n.(syntax.Expr); ok && b.marks[e].kind == 0 && b.info.Patterns[e] != nil {
		b.marks[e] = mark{kind: Match, flag: b.marks[e].flag}
	}
	switch x := n.(type) {
	case *syntax.FuncDecl:
		b.paramList(x.Type.Params)
	case *syntax.LawDecl:
		b.paramList(x.Params)
		if b.encl == NoSym || b.t.Symbols[b.encl].Kind != Law {
			b.memberLaw(x)
			return false
		}
	case *syntax.TestDecl:
		b.paramList(x.Params)
		key, sym := b.testTarget(x)
		b.tgts[x.Name] = target{key, sym}
		b.marks[x.Name] = mark{kind: Def}
	case *syntax.Ident:
		b.ident(x)
		return false
	case *syntax.ImportSpec:
		b.importSpec(x)
	case *syntax.CallExpr:
		m := b.marks[x]
		m.kind = Call
		if b.info.Instances[x] != nil {
			m.flag |= Inst
		}
		b.marks[x.Fun] = m
		if c := b.info.Calls[x]; c != nil {
			b.calls[x.Fun] = c
		}
		b.noteCall(x)
		if b.info.Withs[x] != nil {
			for _, a := range x.Args {
				b.ctxPos[a] = true
			}
		}
	case *syntax.IndexExpr:
		m := b.marks[x]
		if b.info.Instances[x] != nil {
			m.flag |= Inst
		}
		if m != (mark{}) {
			b.marks[x.X] = m
		}
		if c := b.calls[x]; c != nil {
			b.calls[x.X] = c
		}
	case *syntax.ParenExpr:
		b.pass(x, x.X)
	case *syntax.StarExpr:
		b.pass(x, x.X)
	case *syntax.SelectorExpr:
		b.selector(x)
	case *syntax.AssignStmt:
		for _, l := range x.Lhs {
			b.marks[l] = mark{kind: Write}
		}
	case *syntax.CompositeLit:
		b.compositeLit(x)
	case *syntax.NeedsClause:
		b.markAll(x.Names, Needs)
	case *syntax.ErrorRow:
		b.markAll(x.Types, Fails)
	case *syntax.CatchArm:
		b.markAll(x.Types, Handles)
	case *syntax.CatchExpr:
		b.noteCatch(x)
	case *syntax.ContextEntry:
		if x.Kind != syntax.ContextEmbed {
			b.marks[x.Key] = mark{kind: Binds}
			b.markAll(x.Keys, Binds)
		} else {
			b.ctxPos[x.Key] = true
		}
	}
	return true
}

// memberLaw walks a law of an operation type or a machine: its parameters
// and locals are named under the law's handle, not its type's.
func (b *builder) memberLaw(x *syntax.LawDecl) {
	handle, locals, encl := b.handle, b.locals, b.encl
	b.handle, b.locals = "", nil
	if o := b.info.Defs[x.Name]; o != nil {
		if key, sym, ok := b.symbol(o, x.Name, true); ok {
			b.handle = sym.Handle
			b.encl = b.id(key, sym)
		}
	}
	syntax.Inspect(x, func(n syntax.Node) bool { return n == x || b.visit(n) })
	b.nameLocals()
	b.handle, b.locals, b.encl = handle, locals, encl
}

func (b *builder) paramList(l *syntax.FieldList) {
	if l == nil {
		return
	}
	for _, f := range l.List {
		for _, nm := range f.Names {
			b.params[nm] = true
		}
	}
}

func (b *builder) markAll(list []syntax.Expr, k SiteKind) {
	for _, e := range list {
		b.marks[e] = mark{kind: k}
	}
}

// pass hands x's mark and call to its operand.
func (b *builder) pass(x, to syntax.Expr) {
	if m, ok := b.marks[x]; ok {
		b.marks[to] = m
	}
	if c := b.calls[x]; c != nil {
		b.calls[to] = c
	}
}

// selector hands x's mark to the selected name, resolves a selected name
// the checker's Uses does not hold (a field, an operation, a dot call's
// function), and marks the root of an assigned place as written.
func (b *builder) selector(x *syntax.SelectorExpr) {
	if q, ok := x.X.(*syntax.Ident); ok {
		b.quals[x.Sel] = q
	}
	m := b.marks[x]
	if m != (mark{}) {
		b.marks[x.Sel] = m
	}
	if m.kind == Write {
		b.marks[x.X] = mark{kind: Write}
	}
	if b.info.Uses[x.Sel] != nil || b.info.Defs[x.Sel] != nil {
		return
	}
	if q, ok := x.X.(*syntax.Ident); ok {
		if pn, ok := b.info.Uses[q].(*check.PkgName); ok {
			// A qualified name: the checker's Uses holds the qualifier
			// only, and the name is the imported package's member of that
			// name, a context in a context position.
			if pn.Pkg == nil {
				return
			}
			var o check.Object
			if b.ctxPos[x] {
				if c := pn.Pkg.Contexts[x.Sel.Name]; c != nil {
					o = c
				}
			} else {
				o = b.exportedOf(pn.Pkg)[x.Sel.Name]
			}
			if o == nil {
				return
			}
			if key, sym, ok := b.symbol(o, x.Sel, false); ok {
				b.tgts[x.Sel] = target{key, sym}
			}
			return
		}
	}
	if c := b.calls[x]; c != nil {
		switch {
		case c.Op != nil:
			if key, sym, ok := b.method(c.Op, b.info.Types[x.X]); ok {
				b.tgts[x.Sel] = target{key, sym}
			}
			return
		case c.Func != nil && c.Recv != nil:
			if key, sym, ok := b.symbol(c.Func, nil, false); ok {
				b.tgts[x.Sel] = target{key, sym}
			}
			return
		}
	}
	if key, sym, ok := b.member(b.info.Types[x.X], x.Sel.Name); ok {
		b.tgts[x.Sel] = target{key, sym}
	}
}

// compositeLit marks the literal's type and its field keys: Construct for
// a value built, Match for a pattern.
func (b *builder) compositeLit(x *syntax.CompositeLit) {
	k := Construct
	t := b.info.Types[x]
	if p := b.info.Patterns[x]; p != nil {
		k = Match
		if p.Type != nil {
			t = p.Type
		}
	}
	if x.Type != nil {
		b.marks[x.Type] = mark{kind: k}
	}
	for _, e := range x.Elts {
		kv, ok := e.(*syntax.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*syntax.Ident)
		if !ok {
			continue
		}
		if fk, sym, ok := b.field(t, key.Name); ok {
			b.tgts[key] = target{fk, sym}
			b.marks[key] = mark{kind: k}
		}
	}
}

func (b *builder) importSpec(x *syntax.ImportSpec) {
	path := strings.Trim(x.Path.Value, "\"`")
	sym := Symbol{Handle: path, Kind: Package, Name: path[strings.LastIndexByte(path, '/')+1:], Exported: true}
	if x.Name != nil {
		// A named import's name is its site.
		b.tgts[x.Name] = target{path, sym}
		b.marks[x.Name] = mark{kind: Import}
		return
	}
	b.n.Names++
	b.emit(b.id(path, sym), uint32(x.Path.ValuePos), Import, 0)
}

func (b *builder) emit(id, off uint32, k SiteKind, flag uint8) {
	if b.emitted == nil {
		b.emitted = map[[2]uint32]bool{}
	}
	b.emitted[[2]uint32{uint32(b.file), off}] = true
	b.n.Rows++
	b.sites = append(b.sites, Site{Sym: id, Off: off, Encl: b.encl, File: b.file, Kind: k, Flag: flag})
	if k == Def && !b.t.Symbols[id].Declared {
		s := &b.t.Symbols[id]
		s.Declared, s.File, s.Off = true, b.file, off
	}
}

// ident emits the site of a resolved name.
func (b *builder) ident(x *syntax.Ident) {
	if b.emitted[[2]uint32{uint32(b.file), uint32(x.NamePos)}] {
		return // a copy of a written name (emitted)
	}
	m := b.marks[x]
	if tg, ok := b.tgts[x]; ok {
		b.n.Names++
		k := m.kind
		if k == 0 {
			k = Read
			switch tg.sym.Kind {
			case Type, Variant, Machine, TypeParam:
				k = TypeUse
			}
		}
		b.emit(b.id(tg.key, tg.sym), uint32(x.NamePos), k, m.flag)
		return
	}
	k := m.kind
	o := b.info.Defs[x]
	if o != nil {
		k = Def
	} else {
		o = b.info.Uses[x]
	}
	if o == nil {
		return
	}
	key, sym, ok := b.symbol(o, x, k == Def)
	if !ok {
		return
	}
	b.n.Names++
	if k == 0 {
		k = Read
		switch o.(type) {
		case *check.TypeName, *check.TypeParam:
			k = TypeUse
		}
	}
	id := b.id(key, sym)
	if k == Def {
		switch sym.Kind {
		case Param, Local, TypeParam:
			b.locals = append(b.locals, id)
		}
	} else if sym.Kind == TypeParam && !b.t.Symbols[id].Declared {
		b.tpUses = append(b.tpUses, struct{ id, off uint32 }{id, uint32(x.NamePos)})
	}
	b.emit(id, uint32(x.NamePos), k, m.flag)
}

// nameLocals gives the locals of the declaration just walked their
// handles: a parameter or type parameter always, a local when no other
// local, parameter or type parameter of the declaration has its name.
func (b *builder) nameLocals() {
	if b.handle == "" {
		return
	}
	count := map[string]int{}
	for _, id := range b.locals {
		count[b.t.Symbols[id].Name]++
	}
	for _, id := range b.locals {
		s := &b.t.Symbols[id]
		h := b.handle + "." + s.Name
		if s.Handle != "" || s.Kind == Local && count[s.Name] > 1 {
			continue
		}
		if _, taken := b.ids[h]; taken {
			continue // a member of the declaration has the name
		}
		s.Handle = h
		b.ids[h] = id
	}
	// A variant's own type parameter (Checking[T] in a union over T) has
	// no declaring name in the checker's maps, only uses: it is the
	// variant's, the nearest variant named before its first use.
	for _, u := range b.tpUses {
		s := &b.t.Symbols[u.id]
		if s.Handle != "" {
			continue
		}
		v := b.variantBefore(b.file, u.off)
		if v == "" {
			continue
		}
		h := v + "." + s.Name
		if _, taken := b.ids[h]; taken {
			continue
		}
		s.Handle = h
		b.ids[h] = u.id
	}
}

// variantBefore is the handle of the variant of the current declaration
// whose name is nearest before off in file, or "".
func (b *builder) variantBefore(file uint16, off uint32) string {
	h, at := "", uint32(0)
	encl := b.t.Symbols[b.encl]
	for i := range b.t.Symbols {
		v := &b.t.Symbols[i]
		if v.Kind == Variant && v.Declared && v.File == file && v.Off < off && v.Off >= encl.Off && v.Off >= at && v.Handle != "" {
			h, at = v.Handle, v.Off
		}
	}
	return h
}

// symbol is the key and the symbol of o. at is the name that refers to o
// (nil in the declaration pass); def reports its declaring name.
func (b *builder) symbol(o check.Object, at *syntax.Ident, def bool) (any, Symbol, bool) {
	switch o := o.(type) {
	case *check.Func:
		return b.funcSymbol(o)
	case *check.TypeName:
		if tp, ok := o.Type.(*check.TypeParam); ok {
			// A use of a type parameter names a TypeName of its own.
			return b.symbol(tp, at, def)
		}
		if o.Pkg == nil || o.Pkg.Path == "" {
			// A predeclared type: its handle is its name.
			return o.Name, Symbol{Handle: o.Name, Kind: Type, Name: o.Name, Exported: true}, true
		}
		h := o.Pkg.Path + "." + o.Name
		return h, Symbol{Handle: h, Kind: typeKind(o), Name: o.Name, Exported: exported(o.Name)}, true
	case *check.Var:
		if id, ok := b.ids[o]; ok {
			return o, b.t.Symbols[id], true
		}
		if h, ok := b.qualified(at); ok {
			return h, Symbol{Handle: h, Kind: Let, Name: o.Name, Exported: exported(o.Name)}, true
		}
		k := Local
		if def && b.params[at] {
			k = Param
		}
		return o, Symbol{Kind: k, Name: o.Name}, true
	case *check.Field:
		f := o.Origin()
		if h, ok := b.owners[f]; ok {
			h += "." + f.Name
			return h, Symbol{Handle: h, Kind: Field, Name: f.Name, Exported: exported(f.Name)}, true
		}
		return f, Symbol{Kind: Field, Name: f.Name, Exported: exported(f.Name)}, true
	case *check.Method:
		if h, ok := b.owners[o]; ok {
			h += "." + o.Name
			return h, Symbol{Handle: h, Kind: Operation, Name: o.Name, Exported: exported(o.Name)}, true
		}
		return o, Symbol{Kind: Operation, Name: o.Name, Exported: exported(o.Name)}, true
	case *check.Law:
		h := o.Pkg.Path + "."
		switch {
		case o.Owner != nil:
			h += o.Owner.Name + "."
		case o.Machine != nil:
			h += o.Machine.Name + "."
		}
		h += o.Name + "#law"
		return h, Symbol{Handle: h, Kind: Law, Name: o.Name, Exported: exported(o.Name)}, true
	case *check.Context:
		if o.Local || o.Decl == nil {
			return o, Symbol{Kind: Local, Name: o.Name}, true
		}
		h := o.Package().Path + "." + o.Name
		return h, Symbol{Handle: h, Kind: Context, Name: o.Name, Exported: exported(o.Name)}, true
	case *check.TypeParam:
		return o, Symbol{Kind: TypeParam, Name: o.Name}, true
	case *check.PkgName:
		if o.Pkg == nil {
			return nil, Symbol{}, false
		}
		h := o.Pkg.Path
		return h, Symbol{Handle: h, Kind: Package, Name: o.Pkg.Name, Exported: true}, true
	case *check.Builtin, *check.Const:
		return nil, Symbol{}, false
	}
	// A capability named in a body (a binding of the frame's needs row):
	// the capability's type.
	if at != nil {
		if n, ok := b.info.Types[at].(*check.Named); ok && n.Obj != nil {
			return b.symbol(n.Obj, nil, false)
		}
	}
	return nil, Symbol{}, false
}

func (b *builder) funcSymbol(f *check.Func) (any, Symbol, bool) {
	if f.Pkg == nil {
		return nil, Symbol{}, false
	}
	names := b.funcNames[f.Pkg]
	if names == nil {
		names = map[string]int{}
		for _, g := range f.Pkg.Funcs {
			names[g.Name]++
		}
		b.funcNames[f.Pkg] = names
	}
	h := f.Pkg.Path + "."
	if names[f.Name] > 1 && f.Sig != nil && len(f.Sig.Params) > 0 {
		if n := named(f.Sig.Params[0].Type); n != nil && n.Obj != nil {
			h += n.Obj.Name + "."
		}
	}
	h += f.Name
	return h, Symbol{Handle: h, Kind: Func, Name: f.Name, Exported: exported(f.Name)}, true
}

// method is the operation m of the operation type t.
func (b *builder) method(m *check.Method, t check.Type) (any, Symbol, bool) {
	if h, ok := b.owners[m]; ok {
		h += "." + m.Name
		return h, Symbol{Handle: h, Kind: Operation, Name: m.Name, Exported: exported(m.Name)}, true
	}
	if n := named(t); n != nil && n.Obj != nil && n.Obj.Pkg != nil {
		h := n.Obj.Pkg.Path + "." + n.Obj.Name + "." + m.Name
		return h, Symbol{Handle: h, Kind: Operation, Name: m.Name, Exported: exported(m.Name)}, true
	}
	return m, Symbol{Kind: Operation, Name: m.Name, Exported: exported(m.Name)}, true
}

// opOf is the operation m of the operation type n as an edge's end: an
// operation a member `..I` of n includes is I's, which declares it (none
// for the prelude's, which has no handle).
func (b *builder) opOf(m *check.Method, n *check.Named) (any, Symbol, bool) {
	if _, ok := b.owners[m]; !ok {
		i, _ := n.Underlying().(*check.Interface)
		for _, inc := range i.Includes() {
			ii, _ := inc.Underlying().(*check.Interface)
			if ii == nil {
				continue
			}
			for _, im := range ii.Methods {
				if im.Name != m.Name {
					continue
				}
				if inc.Obj == nil || inc.Obj.Pkg == nil || inc.Obj.Pkg.Path == "" {
					return nil, Symbol{}, false
				}
				return b.opOf(im, inc)
			}
		}
	}
	return b.method(m, n)
}

// member is the field or the operation name of a value of type t.
func (b *builder) member(t check.Type, name string) (any, Symbol, bool) {
	if key, sym, ok := b.field(t, name); ok {
		return key, sym, true
	}
	n := named(t)
	if n == nil {
		return nil, Symbol{}, false
	}
	if i, ok := n.Underlying().(*check.Interface); ok {
		for _, m := range i.Methods {
			if m.Name == name {
				return b.method(m, n)
			}
		}
	}
	return nil, Symbol{}, false
}

// field is the field name of a record value of type t.
func (b *builder) field(t check.Type, name string) (any, Symbol, bool) {
	n := named(t)
	var s *check.Struct
	if n != nil {
		s, _ = n.Underlying().(*check.Struct)
	} else {
		s, _ = deref(t).(*check.Struct)
	}
	if s == nil {
		return nil, Symbol{}, false
	}
	for _, f := range s.Fields {
		if f.Name != name {
			continue
		}
		f = f.Origin()
		if h, ok := b.owners[f]; ok {
			h += "." + name
			return h, Symbol{Handle: h, Kind: Field, Name: name, Exported: exported(name)}, true
		}
		if n != nil && n.Obj != nil && n.Obj.Pkg != nil {
			o := n.Obj
			if n.Orig != nil && n.Orig.Obj != nil {
				o = n.Orig.Obj
			}
			h := o.Pkg.Path + "." + o.Name + "." + name
			return h, Symbol{Handle: h, Kind: Field, Name: name, Exported: exported(name)}, true
		}
		return f, Symbol{Kind: Field, Name: name, Exported: exported(name)}, true
	}
	return nil, Symbol{}, false
}

// exportedOf is p's exported members by name: what a name qualified by
// p can name outside a context position.
func (b *builder) exportedOf(p *check.Package) map[string]check.Object {
	m := b.exported[p]
	if m != nil {
		return m
	}
	m = map[string]check.Object{}
	for _, o := range p.Exported() {
		if n := objName(o); n != "" {
			m[n] = o
		}
	}
	b.exported[p] = m
	return m
}

// objName is the declared name of a member of a package scope.
func objName(o check.Object) string {
	switch o := o.(type) {
	case *check.TypeName:
		return o.Name
	case *check.Func:
		return o.Name
	case *check.Var:
		return o.Name
	case *check.Context:
		return o.Name
	case *check.PkgName:
		return o.Name
	}
	return ""
}

// qualified is the handle of a name qualified by an imported package
// (`lib.X`): the walk saw its selector first.
func (b *builder) qualified(at *syntax.Ident) (string, bool) {
	if q := b.quals[at]; q != nil {
		if pn, ok := b.info.Uses[q].(*check.PkgName); ok && pn.Pkg != nil {
			return pn.Pkg.Path + "." + at.Name, true
		}
	}
	return "", false
}

// typeKind is a declared type's kind. The checker declares an inline
// variant of a union as a record whose declaration it makes from the
// variant (check/shapes.go, declareVariant): that declaration starts at
// the variant's name, where a written one starts at `type`.
func typeKind(o *check.TypeName) Kind {
	if o.Decl == nil {
		return Type
	}
	if _, ok := o.Decl.Spec.(*syntax.MachineType); ok {
		return Machine
	}
	if o.Decl.Type == o.Decl.Name.NamePos {
		return Variant
	}
	return Type
}

func named(t check.Type) *check.Named {
	n, _ := deref(t).(*check.Named)
	return n
}

func deref(t check.Type) check.Type {
	if p, ok := t.(*check.Pointer); ok {
		return p.Elem
	}
	return t
}

func exported(name string) bool { return name != "" && name[0] >= 'A' && name[0] <= 'Z' }

// index sorts the sites by symbol, file and offset, and builds the starts,
// the position index and the handle index.
func (b *builder) index() {
	t := b.t
	t.Sites = b.sites
	slices.SortFunc(t.Sites, func(x, y Site) int {
		if x.Sym != y.Sym {
			return cmp(x.Sym, y.Sym)
		}
		if x.File != y.File {
			return cmp(uint32(x.File), uint32(y.File))
		}
		return cmp(x.Off, y.Off)
	})
	reindex(t)
}

// reindex builds the starts, the position index and the handle index of
// t's sorted sites.
func reindex(t *Table) {
	t.Start = make([]uint32, len(t.Symbols)+1)
	for _, s := range t.Sites {
		t.Start[s.Sym+1]++
	}
	for i := 1; i < len(t.Start); i++ {
		t.Start[i] += t.Start[i-1]
	}
	t.ByPos = make([]uint32, len(t.Sites))
	for i := range t.ByPos {
		t.ByPos[i] = uint32(i)
	}
	slices.SortFunc(t.ByPos, func(x, y uint32) int {
		a, c := t.Sites[x], t.Sites[y]
		if a.File != c.File {
			return cmp(uint32(a.File), uint32(c.File))
		}
		if a.Off != c.Off {
			return cmp(a.Off, c.Off)
		}
		return cmp(x, y)
	})
	for id, s := range t.Symbols {
		if s.Handle != "" {
			t.Handles = append(t.Handles, uint32(id))
		}
	}
	slices.SortFunc(t.Handles, func(x, y uint32) int { return strings.Compare(t.Symbols[x].Handle, t.Symbols[y].Handle) })
}

func cmp(a, b uint32) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
