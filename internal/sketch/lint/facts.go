package lint

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	sdk "ergo.local/prototype/lint"
)

// Facts derives the rule-pack fact snapshot (schema ergo.lint.facts v2,
// lint/facts.go) of the program: the families named, every other one
// unavailable as not-requested. On unchecked source every requested family
// is unavailable as unchecked-source. It reads the checker's result only:
// resolved objects, rows, footprints and with facts; it decides nothing of
// its own. Sources lists every file of the program's own packages; a fact
// names a place only in one of them.
func Facts(in Input, families []sdk.Family) *sdk.Snapshot {
	s := &sdk.Snapshot{
		Schema:   sdk.Schema{Name: sdk.FactSchemaName, Version: sdk.FactSchemaVersion},
		Semantic: sdk.Semantic{Target: "go"},
		Sources:  []sdk.Source{},
		Checked:  in.Checked,
		Families: []sdk.Family{},
	}
	revision := sha256.New()
	for _, unit := range in.Units {
		if !programUnit(unit) {
			continue
		}
		for _, f := range unit.Files {
			if f.Syntax == nil {
				continue
			}
			s.Sources = append(s.Sources, sdk.Source{File: f.Rel, Bytes: len(f.Syntax.Src), Text: f.Syntax.Src})
			for _, field := range []string{f.Rel, f.Syntax.Src} {
				var n [8]byte
				binary.BigEndian.PutUint64(n[:], uint64(len(field)))
				revision.Write(n[:])
				revision.Write([]byte(field))
			}
		}
	}
	s.Semantic.Revision = "sha256:" + hex.EncodeToString(revision.Sum(nil))
	want := map[sdk.Family]bool{}
	for _, family := range sdk.Families() {
		if !slices.Contains(families, family) {
			continue
		}
		if !in.Checked {
			s.Unavailable = append(s.Unavailable, sdk.UnavailableFamily{Family: family, Reason: sdk.ReasonUncheckedSource})
			continue
		}
		want[family] = true
		s.Families = append(s.Families, family)
	}
	if len(want) == 0 {
		return s
	}
	x := &extractor{snap: s, want: want, providerFile: map[syntax.Expr]File{}}
	for _, unit := range in.Units {
		if !programUnit(unit) {
			continue
		}
		x.indexProviders(unit)
	}
	for _, unit := range in.Units {
		if !programUnit(unit) {
			continue
		}
		x.unit(unit)
	}
	return s
}

// programUnit reports a checked package of the program's own: not std's,
// not a host binding's.
func programUnit(unit Unit) bool {
	return unit.Package != nil && unit.Package.Info != nil && !unit.Package.Std() && !unit.Package.Host()
}

// extractor fills one snapshot.
type extractor struct {
	snap *sdk.Snapshot
	want map[sdk.Family]bool
	// providerFile is the file of each context entry's provider
	// expression, for the spans of named contexts' entries that a with in
	// another file binds.
	providerFile map[syntax.Expr]File
}

// indexProviders records the file of every context entry's provider
// expression in the unit's files.
func (x *extractor) indexProviders(unit Unit) {
	for _, f := range unit.Files {
		if f.Syntax == nil {
			continue
		}
		file := f
		syntax.Inspect(f.Syntax, func(n syntax.Node) bool {
			if e, ok := n.(*syntax.ContextEntry); ok && e.Value != nil {
				x.providerFile[e.Value] = file
			}
			return true
		})
	}
}

func (x *extractor) unit(unit Unit) {
	pkg, info := unit.Package, unit.Package.Info
	for _, f := range unit.Files {
		if f.Syntax == nil {
			continue
		}
		if x.want[sdk.FamilyImports] {
			for _, imp := range pkg.ImportsOf(f.Syntax) {
				kind := sdk.ImportProgram
				switch {
				case imp.Pkg.Std():
					kind = sdk.ImportStd
				case imp.Pkg.Host():
					kind = sdk.ImportHost
				}
				x.snap.Imports = append(x.snap.Imports, sdk.Import{Name: imp.Name, Path: imp.Pkg.Path, Kind: kind, Span: spanOf(f, imp.Spec.Pos(), imp.Spec.End())})
			}
		}
		for _, d := range f.Syntax.Decls {
			switch d := d.(type) {
			case *syntax.TypeDecl:
				x.typeDecl(f, info, d)
			case *syntax.ContextDecl:
				x.contextDecl(f, pkg, d)
			case *syntax.FuncDecl:
				fn, _ := info.Defs[d.Name].(*check.Func)
				if fn == nil {
					continue
				}
				c := sdk.Callable{
					Identity: funcIdentity(fn), Package: pkg.Path, Name: fn.Name, Kind: sdk.CallableFunction, Exported: fn.Exported(),
					Span: spanOf(f, d.Name.Pos(), d.Name.End()), Extent: spanOf(f, d.Pos(), d.End()),
					Fails: rowIdentities(fn.Sig.Errors), Needs: rowIdentities(fn.Sig.Uses),
				}
				x.callable(f, info, c, d, d.Body)
			case *syntax.TestDecl:
				c := sdk.Callable{
					Identity: "test:" + pkg.Path + "." + d.Name.Name, Package: pkg.Path, Name: d.Name.Name, Kind: sdk.CallableTest,
					Span: spanOf(f, d.Name.Pos(), d.Name.End()), Extent: spanOf(f, d.Pos(), d.End()),
				}
				x.callable(f, info, c, d, d.Body)
			case *syntax.LawDecl:
				c := sdk.Callable{
					Identity: "law:" + pkg.Path + "." + d.Name.Name, Package: pkg.Path, Name: d.Name.Name, Kind: sdk.CallableLaw,
					Span: spanOf(f, d.Name.Pos(), d.Name.End()), Extent: spanOf(f, d.Pos(), d.End()),
				}
				x.callable(f, info, c, d, d.Body)
			}
		}
	}
}

// typeDecl records a type declaration.
func (x *extractor) typeDecl(f File, info *check.Info, d *syntax.TypeDecl) {
	if !x.want[sdk.FamilyDeclarations] {
		return
	}
	tn, _ := info.Defs[d.Name].(*check.TypeName)
	if tn == nil {
		return
	}
	n, _ := tn.Type.(*check.Named)
	if n == nil || n.Alias() != nil {
		return
	}
	t := sdk.TypeDecl{Identity: typeIdentity(n), Package: tn.Pkg.Path, Name: tn.Name, Kind: sdk.TypeDefined, Exported: exported(tn.Name), Span: spanOf(f, d.Name.Pos(), d.Name.End())}
	switch under := n.Underlying().(type) {
	case *check.Struct:
		if n.Defined() == nil {
			t.Kind = sdk.TypeRecord
		}
	case *check.Union:
		t.Kind = sdk.TypeUnion
		for _, v := range under.Variants {
			t.Variants = append(t.Variants, typeIdentity(v))
		}
	case *check.Interface:
		if n.Defined() == nil {
			t.Kind = sdk.TypeOperation
			for _, m := range under.Methods {
				t.Operations = append(t.Operations, m.Name)
			}
		}
	}
	x.snap.Types = append(x.snap.Types, t)
}

// contextDecl records a named context and what each entry binds.
func (x *extractor) contextDecl(f File, pkg *check.Package, d *syntax.ContextDecl) {
	if !x.want[sdk.FamilyDeclarations] {
		return
	}
	c := pkg.Contexts[d.Name.Name]
	if c == nil || c.Decl != d {
		return
	}
	out := sdk.Context{Identity: contextIdentity(pkg, c), Package: pkg.Path, Name: c.Name, Span: spanOf(f, d.Name.Pos(), d.Name.End())}
	c.EachProvider(func(capability check.Type, e *check.ContextEntry) bool {
		out.Entries = append(out.Entries, x.entry(capability, e))
		return true
	})
	slices.SortStableFunc(out.Entries, func(a, b sdk.ContextEntry) int { return strings.Compare(a.Capability, b.Capability) })
	x.snap.Contexts = append(x.snap.Contexts, out)
}

// entry is what a context entry binds capability to.
func (x *extractor) entry(capability check.Type, e *check.ContextEntry) sdk.ContextEntry {
	out := sdk.ContextEntry{Capability: typeIdentity(capability), Provider: typeIdentity(capability)}
	for i, c := range e.Caps {
		if typeIdentity(c) == out.Capability && i < len(e.Conversions) && e.Conversions[i].Kind != 0 && e.Conversions[i].From != nil {
			out.Provider, out.Converts = typeIdentity(e.Conversions[i].From), true
		}
	}
	if e.Provider != nil {
		if file, ok := x.providerFile[e.Provider]; ok {
			span := spanOf(file, e.Provider.Pos(), e.Provider.End())
			out.Span = &span
		}
	}
	return out
}

// callable records one function, test or law: its footprint, and the
// calls, withs and bindings of its body (function literals included).
func (x *extractor) callable(f File, info *check.Info, c sdk.Callable, decl syntax.Node, body *syntax.BlockStmt) {
	if fp, ok := info.Footprints[decl]; ok {
		c.Footprint = sdk.Footprint{Tasks: fp.Tasks, Opaque: fp.Opaque}
		for _, capability := range fp.Caps {
			c.Footprint.Capabilities = append(c.Footprint.Capabilities, typeIdentity(capability))
		}
		slices.Sort(c.Footprint.Capabilities)
		c.Footprint.Capabilities = slices.Compact(c.Footprint.Capabilities)
	}
	if x.want[sdk.FamilyCallables] {
		x.snap.Callables = append(x.snap.Callables, c)
	}
	if x.want[sdk.FamilyBindings] {
		x.bindings(f, info, c.Identity, decl)
	}
	if body == nil || !x.want[sdk.FamilyCalls] && !x.want[sdk.FamilyWiths] {
		return
	}
	syntax.Inspect(body, func(n syntax.Node) bool {
		call, ok := n.(*syntax.CallExpr)
		if !ok {
			return true
		}
		if w := info.Withs[call]; w != nil && x.want[sdk.FamilyWiths] {
			x.with(f, c.Identity, call, w)
		}
		if site := info.Calls[call]; site != nil && x.want[sdk.FamilyCalls] {
			x.snap.Calls = append(x.snap.Calls, sdk.Call{Caller: c.Identity, Callee: calleeOf(info, call, site), Kind: callKind(site.Kind), Span: spanOf(f, call.Pos(), call.End()), Fails: rowIdentities(site.Fails)})
		}
		return true
	})
}

// with records a with and its later operands as one fact.
func (x *extractor) with(f File, caller string, call *syntax.CallExpr, w *check.With) {
	out := sdk.With{Callable: caller, Span: spanOf(f, call.Pos(), call.End())}
	for ; w != nil; w = w.Next {
		for _, ctx := range w.Contexts {
			if ctx.Decl != nil {
				out.Contexts = append(out.Contexts, contextIdentityOf(ctx))
			}
		}
		for i, capability := range w.Discharged {
			if i < len(w.Entries) && w.Entries[i] != nil {
				out.Entries = append(out.Entries, x.entry(capability, w.Entries[i]))
			}
		}
		for _, a := range w.Acquired {
			if a.Entry == nil {
				continue
			}
			provider := ""
			for i, conv := range a.Entry.Conversions {
				if conv.Kind != 0 && conv.From != nil {
					provider = typeIdentity(conv.From)
					break
				}
				if provider == "" && i < len(a.Entry.Caps) {
					provider = typeIdentity(a.Entry.Caps[i])
				}
			}
			if provider != "" {
				out.Acquired = append(out.Acquired, provider)
			}
		}
	}
	x.snap.Withs = append(x.snap.Withs, out)
}

// bindings records each local of the callable that the checker defined in
// its syntax, with every use the checker resolved to it.
func (x *extractor) bindings(f File, info *check.Info, caller string, decl syntax.Node) {
	index := map[*check.Var]int{}
	syntax.Inspect(decl, func(n syntax.Node) bool {
		id, ok := n.(*syntax.Ident)
		if !ok {
			return true
		}
		if v, ok := info.Defs[id].(*check.Var); ok {
			if _, seen := index[v]; !seen {
				span := spanOf(f, id.Pos(), id.End())
				index[v] = len(x.snap.Bindings)
				x.snap.Bindings = append(x.snap.Bindings, sdk.Binding{
					Identity: caller + "@" + strconv.Itoa(span.Offset), Callable: caller, Name: v.Name,
					Type: typeString(v.Type), Mutable: v.Mut(), Span: span,
				})
			}
		}
		return true
	})
	syntax.Inspect(decl, func(n syntax.Node) bool {
		id, ok := n.(*syntax.Ident)
		if !ok {
			return true
		}
		if v, ok := info.Uses[id].(*check.Var); ok {
			if i, known := index[v]; known {
				x.snap.Bindings[i].Uses = append(x.snap.Bindings[i].Uses, spanOf(f, id.Pos(), id.End()))
			}
		}
		return true
	})
}

// spanOf is the span from..to of file f.
func spanOf(f File, from, to syntax.Pos) sdk.Span {
	line, column := f.Syntax.Position(from)
	return sdk.Span{File: f.Rel, Offset: int(from), Length: int(to - from), Line: line, Column: column}
}

// typeIdentity is the identity of a type: "type:" and its declaring
// package's path and its name for a named type (an instance names its
// generic type; a pointer names its element), and the checker's text for
// any other type.
func typeIdentity(t check.Type) string {
	if p, ok := t.(*check.Pointer); ok {
		t = p.Elem
	}
	n, ok := t.(*check.Named)
	if !ok {
		return "type:" + typeString(t)
	}
	if n.Orig != nil {
		n = n.Orig
	}
	if n.Obj == nil {
		return "type:" + typeString(t)
	}
	if n.Obj.Pkg == nil {
		return "type:" + n.Obj.Name
	}
	return "type:" + n.Obj.Pkg.Path + "." + n.Obj.Name
}

func typeString(t check.Type) string {
	if t == nil {
		return ""
	}
	return t.String()
}

// rowIdentities are a row's elements as type identities, sorted. An
// inferred row's variable stands for its solution.
func rowIdentities(r *check.Row) []string {
	var out []string
	var add func(r *check.Row, depth int)
	add = func(r *check.Row, depth int) {
		for _, t := range r.Elems() {
			if v, ok := t.(*check.RowVar); ok {
				if depth < 8 {
					add(v.Solution(), depth+1)
				}
				continue
			}
			out = append(out, typeIdentity(t))
		}
	}
	add(r, 0)
	slices.Sort(out)
	return slices.Compact(out)
}

// funcIdentity is "func:PKG.Name", with "#N" for the N-th later function
// of the same name in the package (functions x.f(…) reaches may share a
// name).
func funcIdentity(f *check.Func) string {
	id := "func:" + f.Name
	if f.Pkg == nil {
		return id
	}
	id = "func:" + f.Pkg.Path + "." + f.Name
	n := 0
	for _, other := range f.Pkg.Funcs {
		if other == f {
			break
		}
		if other.Name == f.Name {
			n++
		}
	}
	if n > 0 {
		id += "#" + strconv.Itoa(n)
	}
	return id
}

func contextIdentity(pkg *check.Package, c *check.Context) string {
	return "context:" + pkg.Path + "." + c.Name
}

// contextIdentityOf is the identity of a named context a with binds,
// found by the package that declares it.
func contextIdentityOf(c *check.Context) string {
	if pkg := c.Package(); pkg != nil {
		return contextIdentity(pkg, c)
	}
	return "context:" + c.Name
}

// calleeOf is the resolved callee of a call: a function's identity, or an
// operation's as its type's identity and the operation's name.
func calleeOf(info *check.Info, x *syntax.CallExpr, site *check.Call) string {
	switch site.Kind {
	case check.CallFunc:
		if site.Func != nil {
			return funcIdentity(site.Func)
		}
	case check.CallOp, check.CallProviderOp:
		if site.Op == nil {
			return ""
		}
		recv := site.Recv
		if sel, ok := x.Fun.(*syntax.SelectorExpr); ok && site.Kind == check.CallProviderOp {
			recv = sel.X
		}
		if recv != nil {
			if t := info.Types[recv]; t != nil {
				return typeIdentity(t) + "." + site.Op.Name
			}
		}
	}
	return ""
}

func callKind(k check.CallKind) string {
	switch k {
	case check.CallFunc:
		return sdk.CallFunction
	case check.CallOp:
		return sdk.CallOperation
	case check.CallProviderOp:
		return sdk.CallCapability
	case check.CallFuncValue:
		return sdk.CallValue
	case check.CallConversion:
		return sdk.CallConversion
	case check.CallWith:
		return sdk.CallWith
	}
	return sdk.CallBuiltin
}

// exported reports a name visible to importers: it starts with an upper
// case letter (grammar.md, Visibility).
func exported(name string) bool { return len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z' }
