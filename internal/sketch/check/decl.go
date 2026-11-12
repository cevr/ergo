package check

import (
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// run checks the package: collect declarations, resolve types lazily, check
// signatures, check bodies one strongly connected component at a time, then
// the package-level rules that need every body (Go types2's phase order).
func (c *checker) run(files []*syntax.File) *Package {
	for i, f := range files {
		c.files = append(c.files, &fileInfo{syntax: f, index: i, scope: newScope(nil)})
	}
	c.pkg.files = c.files
	c.startWithBudget(files)
	// A decided source has no package clause: the directory names the
	// package (grammar.md, Old spellings), so the name is the last element
	// of the import path.
	c.pkg.Name = path.Base(c.pkg.Path)
	// While the package is checked, its diagnostics name its own types
	// bare, as its source writes them (Broken, not main.Broken); another
	// package's types keep their package name (a.Broken), which resolves
	// through the import (K4; diagnostics.md, Type names). Once checked,
	// the package's types print qualified for every later reader.
	c.pkg.qualify = false
	defer func() { c.pkg.qualify = true }()
	// Each pass starts with no file selected: a pass selects the file of
	// what it inspects before it reports, and one that does not reports
	// with no file and stops at the ownership assertion (owner.go) in a
	// one-file package as well, instead of inheriting the last file an
	// earlier pass visited (E2 round 2 M1).
	for _, pass := range []func(){
		c.collect,
		c.strayAsserts,
		c.checkImportAliases,
		c.solveDeclKinds,
		func() {
			for _, tn := range c.pkg.Types {
				// Defensive: resolveNamed selects the declaration again before reporting.
				c.cur = tn.file
				c.resolveNamed(tn.Type.(*Named))
			}
		},
		c.expandIncludes,
		c.checkUnitContextNames,
		// Expanding type instantiations are rejected before any walk
		// expands instances (round-2 M1, M5).
		c.checkInstantiationCycles,
		c.completeDefinedTypes,
		c.checkRefinements,
		c.collectSelfFuncs,
		func() {
			for _, f := range c.funcs {
				// Defensive: funcSig selects the function again before reporting.
				c.cur = f.file
				c.funcSig(f)
			}
		},
		// Expanding instantiations in signatures, a method's receiver
		// parameters included, are rejected before any row is inferred
		// (round-4 M1).
		c.checkInstantiationCycles,
		// A user extern's written signature is final here: an extern's
		// rows are never inferred (D-E5).
		c.checkExterns,
		c.collectBodyErrorRows,
		c.checkEntry,
		// The context pass runs once, before any body, so its budget verdict
		// does not depend on which body meets a context first (contextsum.go).
		c.contextSummaries,
		// Bodies converge first: a machine's initial value and the named
		// contexts are closed contexts that read the converged inferred rows
		// (M2).
		c.checkBodies,
		// A constructor's rows are inferred, so constructors are planned
		// once bodies converge, before any generator is decided.
		c.planConstructors,
		c.checkTests,
		c.checkLaws,
		c.planLaws,
		c.checkTypeDecls,
		c.checkContexts,
		c.checkContextCycles,
		c.checkErrorMethods,
		c.checkUnusedImports,
		c.finishInfo,
		c.closeFacts,
		// After closeFacts: every Close and failure row is final.
		c.checkResourceTypes,
		// After the resource types: what each with builds (withown.go),
		// then who owns each resource (ownership.go), which moves a local
		// provider into the with that builds it (R11).
		c.withOwnership,
		c.checkOwnership,
		// Laws under a context read the acquisitions (lawcontexts.go).
		c.planLawContexts,
		c.checkFrames,
		c.checkEscapes,
		c.footprints,
		// After the footprints: a durable machine's step is pure (MU9).
		c.checkDurableSteps,
		// After the footprints: an invariant law's predicate is pure (X2).
		c.checkLawPreds,
		// After every body: the first with operand past the with budget,
		// silent checks included (withbudget.go).
		c.reportWithBudget,
		// Last: every lazy read of a binding is done (gobinding.go).
		c.checkGoImports,
	} {
		c.cur = nil
		pass()
	}
	c.publishErrorTypes()
	c.cur = nil
	return c.pkg
}

// collectBodyErrorRows records every struct type that an error row of the
// package names, anywhere: signatures, bodies, function literals, package
// var initializers, machine initial values and context providers, before any
// body is checked, so whether a type is an error type (types.md, Error types
// 1) does not depend on declaration order (M3). It reads each element's
// head type name syntactically (`Fault`, `pkg.Fault`, `Fault[T]`), so an
// element whose type arguments name a body's type parameter still counts.
// Each row counts for its own owner (owners.go): a test's row makes an
// error type of the tests only, and a law's row of the laws only. It
// reports nothing; the checks of the rows report.
func (c *checker) collectBodyErrorRows() {
	for _, fi := range c.files {
		// Defensive: this pass only collects row heads and reports nothing.
		c.cur = fi
		syntax.Inspect(fi.syntax, func(n syntax.Node) bool {
			if er, ok := n.(*syntax.ErrorRow); ok {
				for _, x := range er.Types {
					if n := c.errorRowHead(x); n != nil {
						c.addErrorType(n, c.pkg.OwnerOf(er), x)
					}
					for _, n := range c.namedRowHeads(x) {
						c.addErrorType(n, c.pkg.OwnerOf(er), x)
					}
				}
			}
			return true
		})
	}
}

// errorRowHead returns the origin struct type an error-row element names by
// its head, or nil.
func (c *checker) errorRowHead(x syntax.Expr) *Named {
	if ix, ok := x.(*syntax.IndexExpr); ok {
		x = ix.X
	}
	tn, n := c.namedHead(x)
	if n == nil || tn.Decl == nil {
		return nil
	}
	if _, isStruct := tn.Decl.Spec.(*syntax.StructType); !isStruct {
		return nil
	}
	return n.origin()
}

// namedRowHeads returns the origin variants of the union an error-row
// element names by its head, when that union names a row (namedRow): a
// union of this package by its declaration's variant names, before any
// type resolves; one of another package by its resolved variants, each an
// error type there. Any other element gives nil.
func (c *checker) namedRowHeads(x syntax.Expr) []*Named {
	if ix, ok := x.(*syntax.IndexExpr); ok {
		x = ix.X
	}
	tn, n := c.namedHead(x)
	if n == nil || tn.Decl == nil {
		return nil
	}
	it, ok := tn.Decl.Spec.(*syntax.InterfaceType)
	if !ok || len(it.Union) == 0 {
		return nil
	}
	var out []*Named
	if p := n.Obj.Pkg; p != c.pkg {
		u, ok := n.underlying().(*Union)
		if !ok || p == nil {
			return nil
		}
		for _, v := range u.Variants {
			vn, ok := v.(*Named)
			if !ok || !p.IsErrorType(vn, OwnerProduction) {
				return nil
			}
			out = append(out, vn.origin())
		}
		return out
	}
	for _, v := range it.Union {
		var name syntax.Expr = v
		switch v := v.(type) {
		case *syntax.VariantSpec:
			name = v.Name
		case *syntax.IndexExpr:
			name = v.X
		}
		if _, vn := c.namedHead(name); vn != nil {
			out = append(out, vn.origin())
		}
	}
	return out
}

// namedHead returns the declared type a name or a qualified name denotes,
// without resolving it, or nil.
func (c *checker) namedHead(x syntax.Expr) (*TypeName, *Named) {
	var o Object
	switch x := x.(type) {
	case *syntax.Ident:
		o = c.lookupType(x.Name, nil)
	case *syntax.SelectorExpr:
		id, ok := x.X.(*syntax.Ident)
		if !ok {
			return nil, nil
		}
		if pn, ok := c.lookupType(id.Name, nil).(*PkgName); ok {
			o = pn.Pkg.Scope.names[x.Sel.Name]
		}
	}
	tn, ok := o.(*TypeName)
	if !ok {
		return nil, nil
	}
	n, ok := tn.Type.(*Named)
	if !ok {
		return nil, nil
	}
	return tn, n
}

// ---------------------------------------------------------------- collection

func (c *checker) collect() {
	// A package-level let with a pattern (196p, 196t) declares its names
	// once every type is declared, so a type name in it tests the variant.
	type patternSpec struct {
		spec *syntax.VarSpec
		file *fileInfo
	}
	var patterns []patternSpec
	for _, fi := range c.files {
		c.cur = fi
		f := fi.syntax
		for _, d := range f.Decls {
			if syntax.IsBindingDecl(d) {
				// A host type, const or method comes only from a binding
				// that ergo bind go writes (docs/spec/interop.md); the
				// checker reads bindings with #202's import checking.
				c.errorf(d, CodeOutside, "a binding declaration (extern go with type, let or a receiver) is #202's (Go imports); ergo bind go writes it, and it is not checked yet")
				continue
			}
			switch d := d.(type) {
			case *syntax.ImportDecl:
				if len(d.Specs) != 1 {
					c.undecided(d, "a grouped import")
					continue
				}
				c.importSpec(fi, d.Specs[0])
			case *syntax.TypeDecl:
				tn := &TypeName{Name: d.Name.Name, Pkg: c.pkg, Pos: d.Name.Pos(), Decl: d, file: fi, id: newID()}
				tn.Type = &Named{Obj: tn}
				declared := c.declare(d.Name, tn)
				if declared {
					c.pkg.Types = append(c.pkg.Types, tn)
				}
				// An inline variant declares a record in the package scope
				// (spec D-B1), after its union in source order.
				if it, ok := d.Spec.(*syntax.InterfaceType); ok {
					for _, x := range it.Union {
						if vs, ok := x.(*syntax.VariantSpec); ok {
							c.declareVariant(fi, d, vs)
						}
					}
					// A law's facts are its own (owners.go), also in a
					// declaration refused as a duplicate, whose laws no
					// pass checks: no build reads them.
					for _, l := range it.Laws {
						c.ownBody(l, OwnerLaw)
					}
					if declared {
						c.collectLaws(it, tn, fi)
					}
				}
				if mt, ok := d.Spec.(*syntax.MachineType); ok {
					for _, l := range mt.Laws {
						c.ownBody(l, OwnerLaw)
					}
					if declared {
						c.collectMachineLaws(mt, tn, fi)
					}
				}
			case *syntax.FuncDecl:
				fn := &Func{Name: d.Name.Name, Pkg: c.pkg, Pos: d.Name.Pos(), Decl: d, file: fi}
				c.funcs = append(c.funcs, fn)
				// A second function of one name waits for the types:
				// collectSelfFuncs accepts the group when the first
				// parameters are distinct local types (196m rule 3) and
				// reports the redeclaration otherwise.
				if prev, ok := c.pkg.Scope.names[d.Name.Name].(*Func); ok && d.Name.Name != "_" {
					c.preludeName(d.Name) // D-D3, at each declaration
					prev.same = append(prev.same, fn)
					continue
				}
				if !c.contextNamed(d.Name) && c.declare(d.Name, fn) {
					c.pkg.Funcs = append(c.pkg.Funcs, fn)
				}
			case *syntax.VarDecl:
				if len(d.Specs) != 1 {
					c.undecided(d, "a grouped let")
					continue
				}
				if d.Mut.IsValid() {
					c.mutPackageLet(d)
				}
				for _, spec := range d.Specs {
					if spec.Pattern != nil {
						patterns = append(patterns, patternSpec{spec, fi})
						continue
					}
					var group *pkgPattern
					if len(spec.Names) > 1 && len(spec.Values) == 1 {
						// `let q, r = e` destructures one tuple (196t).
						group = &pkgPattern{spec: spec}
						c.pkgPatterns[spec] = group
					}
					for i, name := range spec.Names {
						// A refused package-level let mut (mutPackageLet)
						// keeps mut, so its assignments add nothing (D3).
						// A prelude name binds, refused by D-D3 (declare):
						// only a value read sees it (P5).
						v := &Var{Name: name.Name, Pos: name.Pos(), id: name, pkg: true, used: true, mut: d.Mut.IsValid(), refused: c.isPreludeName(name.Name)}
						if !c.contextNamed(name) {
							c.declare(name, v)
						}
						pv := &pkgVar{obj: v, spec: spec, idx: i, file: fi, name: name, group: group}
						if group != nil {
							group.members = append(group.members, pv)
						}
						c.vars = append(c.vars, pv)
					}
				}
			case *syntax.TestDecl:
				c.collectTest(d, fi)
			case *syntax.LawDecl:
				c.collectTopLaw(d, fi)
			case *syntax.ContextDecl:
				c.preludeName(d.Name) // D-D3; the context still binds
				w := &Context{Name: d.Name.Name, Pos: d.Name.Pos(), Decl: d, file: fi, pkg: c.pkg}
				if _, dup := c.pkg.Contexts[w.Name]; dup {
					c.errorf(d.Name, CodeType, "context %s is declared twice", w.Name)
					continue
				}
				if isPkgValue(c.pkg.Scope.names[w.Name]) {
					c.errorf(d.Name, CodeType, "%s is already declared in this package", w.Name)
					continue
				}
				c.pkg.Contexts[w.Name] = w
				c.def(d.Name, w)
			default:
				c.undecided(d, "")
			}
		}
	}
	// A package-level let with a pattern declares the names the pattern
	// binds, read with every type declared (a type name in it tests the
	// type, D-B3 rule 1, by the let rule: a prelude name binds, refused by
	// D-D3, P5); checkPkgVar types them all from the one value.
	letTests := func(name string) bool { return c.patternTests(name, true, false) }
	for _, p := range patterns {
		c.cur = p.file
		group := &pkgPattern{spec: p.spec}
		c.pkgPatterns[p.spec] = group
		for _, id := range patternNames(p.spec.Pattern, letTests) {
			v := &Var{Name: id.Name, Pos: id.Pos(), id: id, pkg: true, used: true, refused: c.isPreludeName(id.Name)}
			if w, ok := c.pkg.Contexts[id.Name]; ok && id.Name != "_" && (w.file.index > p.file.index || w.file == p.file && w.Pos > id.Pos()) {
				// The pattern is collected after every context: when the
				// context is the later declaration, it is the duplicate.
				c.cur = w.file
				c.errorf(w.Decl.Name, CodeType, "%s is already declared in this package", w.Name)
				c.cur = p.file
				delete(c.pkg.Contexts, w.Name)
			}
			if !c.contextNamed(id) {
				c.declare(id, v)
			}
			pv := &pkgVar{obj: v, spec: p.spec, file: p.file, name: id, group: group}
			group.members = append(group.members, pv)
			c.vars = append(c.vars, pv)
		}
		if len(group.members) == 0 {
			// A pattern that binds nothing still checks its value once.
			pv := &pkgVar{obj: &Var{Name: "_", Pos: p.spec.Pos(), pkg: true, used: true}, spec: p.spec, file: p.file, group: group}
			group.members = append(group.members, pv)
			c.vars = append(c.vars, pv)
		}
	}
	// The export guard: the step of an exported machine type spells its
	// rows even when it is unexported (rows-and-constraints §2).
	for _, tn := range c.pkg.Types {
		m, ok := tn.Decl.Spec.(*syntax.MachineType)
		if !ok || !isExported(tn.Name) {
			continue
		}
		if id, ok := m.Step.(*syntax.Ident); ok {
			if f, ok := c.pkg.Scope.names[id.Name].(*Func); ok {
				c.guarded[f] = true
			}
		}
	}
}

// declare enters a package-level name; a second declaration of one name is
// S108 (a type and a function share the package's namespace). A prelude
// name is S108 (D-D3) and still declares, so its uses add nothing, except
// that a type may take a prelude type's name (preludeTypeName; L6).
func (c *checker) declare(id *syntax.Ident, o Object) bool {
	if id.Name == "_" {
		return false
	}
	// A package type may take a prelude type's name, which it hides in this
	// package (grammar.md, Reserved names 2; L6, owner q30).
	_, isType := o.(*TypeName)
	if !(isType && c.preludeTypeName(id.Name)) && c.preludeName(id) {
		// reported; the declaration binds below
	} else if prev, ok := c.pkg.Scope.names[id.Name]; ok {
		what := "declared"
		if _, isType := prev.(*TypeName); isType {
			what = "declared as a type"
		}
		c.errorf(id, CodeType, "%s is already %s in this package", id.Name, what)
		c.dupNames[id.Name] = true
		return false
	}
	c.pkg.Scope.insert(id.Name, o)
	c.def(id, o)
	return true
}

// contextNamed reports a package value (a let, a pattern let's name or a
// function) declared after a named context of its name as S108: values and
// contexts share one package namespace, so `with W` never meets two package
// bindings of W. A type and a context may share a name (`let A = context`).
func (c *checker) contextNamed(id *syntax.Ident) bool {
	if _, ok := c.pkg.Contexts[id.Name]; !ok || id.Name == "_" {
		return false
	}
	c.errorf(id, CodeType, "%s is already declared in this package", id.Name)
	return true
}

// checkUnitContextNames extends that rule to a record with no fields, which
// is a value as well as a type (written bare, types.md Structs 2): a context
// of its name is S108 at the later declaration (runtime-199 stage 4a r3
// m2; as Rust's E0428, where a unit struct is in the value namespace). It
// runs once the types are resolved. The context stays bound: the program is
// refused, and a with over it adds no cascade (D3).
func (c *checker) checkUnitContextNames() {
	for _, tn := range c.pkg.Types {
		w, ok := c.pkg.Contexts[tn.Name]
		if !ok || tn.Name == "_" || !isZeroRecord(tn.Type) {
			continue
		}
		at, f := w.Decl.Name, w.file
		if tn.file.index > w.file.index || tn.file == w.file && tn.Pos > w.Pos {
			at, f = tn.Decl.Name, tn.file
		}
		c.cur = f
		c.errorf(at, CodeType, "%s is already declared in this package", tn.Name)
	}
}

// isPkgValue reports whether a package-scope object is a value: a let or
// a function, not a type (a record with no fields is checkUnitContextNames').
func isPkgValue(o Object) bool {
	switch o.(type) {
	case *Var, *Func:
		return true
	}
	return false
}

func (c *checker) importSpec(fi *fileInfo, spec *syntax.ImportSpec) {
	// A decided import path is one unquoted token (grammar.md, Lexical 6).
	path := spec.Path.Value
	if spec.Path.Kind != syntax.PATH {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			c.errorf(spec.Path, CodeType, "malformed import path %s", spec.Path.Value)
			return
		}
		path = p
	}
	if spec.Kind == syntax.ImportGo {
		// Go's rule for an import path, before any name is read from it
		// (#202 comment, small-fix #231).
		if why := GoImportPathError(path); why != "" {
			c.errorf(spec.Path, CodeType, "malformed Go import path %q: %s", path, why)
			c.bindBrokenImport(fi, spec, path)
			return
		}
	}
	// A keyword cannot be the name an import binds, so a path whose last
	// element is one needs an alias (grammar.md, Declarations rule 1; 196w).
	// The message recognises Go's `import "context"` habit (#220).
	if def := importDefault(spec, path); spec.Name == nil && syntax.IsKeyword(def) {
		fix := "import " + def + "pkg " + path
		if spec.Kind == syntax.ImportGo {
			fix = "import go go" + def + " " + path
		}
		c.errorf(spec.Path, CodeType, "%s is a keyword; import the package under another name: %s", def, fix)
		c.bindBrokenImport(fi, spec, path)
		return
	}
	// The same rule holds for a last element that is not a name, such as
	// the folder okra-repo (196b Q3): the import needs an alias.
	if def := importDefault(spec, path); spec.Name == nil && !syntax.IsKeyword(def) && !importName(def) {
		c.errorf(spec.Path, CodeType, "%s is not a name; import the package under a name: import %s %s", def, strings.ReplaceAll(def, "-", ""), path)
		c.bindBrokenImport(fi, spec, path)
		return
	}
	switch spec.Kind {
	case syntax.ImportPlain, syntax.ImportNamed:
	case syntax.ImportGo:
		c.importGo(fi, spec, path)
		return
	default:
		c.undecided(spec.Path, "this import form")
		return
	}
	imp := c.imp
	if _, isStd := std.Package(path); isStd {
		// A std path always names std, whatever importer Check was given:
		// a program package cannot shadow log or queue (C30).
		imp = Std
	}
	pkg, err := imp.Import(path)
	if err != nil || pkg == nil {
		// An importer may wrap its *ImportError (fmt.Errorf %w,
		// errors.Join); findImportError searches the chain.
		if reason := findImportError(err); reason != nil {
			c.errorf(spec.Path, CodeUnknown, "unknown package %q: %s", path, reason.Reason)
		} else {
			c.errorf(spec.Path, CodeUnknown, "unknown package %q", path)
		}
		c.bindBrokenImport(fi, spec, path)
		return
	}
	name := pkg.Name
	if spec.Kind == syntax.ImportNamed {
		name = spec.Name.Name
	}
	if _, dup := fi.scope.names[name]; name != "_" && dup {
		c.errorf(spec, CodeType, "%s is imported twice", name)
		// Keep the first binding. The refused spec's default name is a
		// broken import, so a use of it cannot cascade with S104.
		c.bindBrokenImportName(fi, spec, path, lastElement(path))
		return
	}
	if spec.Kind == syntax.ImportNamed {
		// D-D3; the import still binds. A refused alias has its
		// diagnostic and adds no D-D7 one.
		if !c.preludeName(spec.Name) {
			c.aliases = append(c.aliases, importAlias{fi, spec, path})
		}
	}
	pn := &PkgName{Name: name, Pos: spec.Pos(), Pkg: pkg, spec: spec}
	if name == "_" {
		return
	}
	fi.scope.insert(name, pn)
	fi.imports = append(fi.imports, pn)
}

// importAlias is one resolved `import name path` spec of a file.
type importAlias struct {
	file *fileInfo
	spec *syntax.ImportSpec
	path string
}

// checkImportAliases is D-D7: an import alias is allowed only when the
// path's last element clashes with a package-level name or with the name
// another import of the same file binds; any other alias is S108, and
// `_` follows the same rule (an Ergo import has no side effect to keep).
// It runs once every package-level name is declared. Prior art: Go's style
// guide renames an import only to avoid a collision; Gleam qualifies by
// module and aliases with `as`; MoonBit qualifies by package.
func (c *checker) checkImportAliases() {
	refused := map[*syntax.ImportSpec]bool{}
	for _, fi := range c.files {
		c.cur = fi
		for _, pn := range fi.imports {
			if pn.spec.Kind != syntax.ImportPlain && pn.spec.Kind != syntax.ImportNamed {
				continue
			}
			// A context is in the package's separate context namespace, and an
			// import name clashes with it too, so `with log` never binds a
			// context beside the package log (runtime-199 stage 4a r3 m1).
			var o Object = c.pkg.Scope.names[pn.Name]
			kind := "declaration"
			if w := c.pkg.Contexts[pn.Name]; o == nil && w != nil {
				o, kind = w, "context"
			}
			if o != nil {
				switch o.(type) {
				case *Func:
					kind = "func"
				case *TypeName:
					kind = "type"
				case *Var, *Const:
					kind = "let"
				}
				// An alias that clashes too is refused here, once, so the
				// alias rule below adds nothing (D3a round 2 review m3).
				if pn.spec.Kind == syntax.ImportNamed {
					c.errorf(pn.spec, CodeType, "%s clashes with %s %s; choose another alias", pn.Name, kind, o.name())
				} else {
					c.errorf(pn.spec, CodeType, "%s clashes with %s %s; alias the import (import t %s)", pn.Name, kind, o.name(), pn.Pkg.Path)
				}
				pn.used, pn.broken = true, true
				refused[pn.spec] = true
			}
		}
	}
	for _, a := range c.aliases {
		if refused[a.spec] {
			continue
		}
		c.cur = a.file
		def := importDefault(a.spec, a.path)
		// A keyword, or an element that is not a name (196b Q3), is never
		// a bound name, so its alias is needed.
		clash := syntax.IsKeyword(def) || !importName(def)
		if _, ok := c.pkg.Scope.names[def]; ok {
			clash = true
		}
		for _, d := range a.file.syntax.Decls {
			d, ok := d.(*syntax.ImportDecl)
			if !ok || clash {
				continue
			}
			for _, s := range d.Specs {
				if s != a.spec && importBinds(s) == def {
					clash = true
				}
			}
		}
		if !clash {
			c.errorf(a.spec, CodeType, "an import alias is only for a name clash; write import %s (#196, fourth round, item 3)", a.path)
			if pn, ok := a.file.scope.names[importBinds(a.spec)].(*PkgName); ok {
				pn.used = true // The refused alias must not also be unused.
			}
		}
	}
}

// lastElement is an import path's last element: the name a plain import
// of it binds by default.
func lastElement(path string) string {
	return path[strings.LastIndex(path, "/")+1:]
}

// importDefault is the name an import binds with no alias: the last
// element, or for import go the Go package name (math/rand/v2 is rand;
// GoPackageName).
func importDefault(spec *syntax.ImportSpec, path string) string {
	if spec.Kind == syntax.ImportGo {
		return GoPackageName(path)
	}
	return lastElement(path)
}

// importName reports whether an import path element can be the name a
// plain import binds: a letter or `_`, then letters, digits and `_`. A
// path element may also hold `-` (grammar.md, Literals), and such an
// element needs an alias (196b Q3).
func importName(element string) bool {
	if element == "" {
		return false
	}
	for i, r := range element {
		if r != '_' && !unicode.IsLetter(r) && (i == 0 || !unicode.IsDigit(r)) {
			return false
		}
	}
	return true
}

// ImportError is an importer's refusal with a reason the checker adds to
// its S104 "unknown package" diagnostic, such as a program with no
// ergo.mod importing a local path (196b Q1). An importer may return it
// wrapped (fmt.Errorf with %w, errors.Join): findImportError searches the
// chain.
type ImportError struct {
	Path   string
	Reason string
}

func (e *ImportError) Error() string { return fmt.Sprintf("package %q: %s", e.Path, e.Reason) }

// findImportError's bounds, so a custom Unwrap with a cycle ends: a path
// from err is at most maxUnwrapDepth errors deep, and the search visits at
// most maxUnwrapVisits errors. An error may be of a type that is not
// comparable, so the search cannot keep a set of the errors it saw.
const (
	maxUnwrapDepth  = 64
	maxUnwrapVisits = 4096
)

// findImportError is the first *ImportError in err's tree, searched depth
// first in errors.As order through Unwrap() error and Unwrap() []error, or
// nil. It is errors.As without the errors import, which the reviewed
// provenance allow-list does not hold (batch-9 C2, owner default).
func findImportError(err error) *ImportError {
	type item struct {
		err   error
		depth int
	}
	stack := []item{{err, 0}}
	for n := 0; len(stack) > 0 && n < maxUnwrapVisits; n++ {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if it.depth >= maxUnwrapDepth {
			continue
		}
		switch w := it.err.(type) {
		case nil:
		case *ImportError:
			return w
		case interface{ Unwrap() error }:
			stack = append(stack, item{w.Unwrap(), it.depth + 1})
		case interface{ Unwrap() []error }:
			// Push in reverse, so the first error is searched first.
			errs := w.Unwrap()
			for i := len(errs) - 1; i >= 0; i-- {
				stack = append(stack, item{errs[i], it.depth + 1})
			}
		}
	}
	return nil
}

// importBinds is the name an import spec binds in its file: its alias, or
// its path's last element; "" for a malformed path.
func importBinds(s *syntax.ImportSpec) string {
	if s.Kind == syntax.ImportNamed && s.Name != nil {
		return s.Name.Name
	}
	if s.Path == nil {
		return ""
	}
	path := s.Path.Value
	if s.Path.Kind != syntax.PATH {
		p, err := strconv.Unquote(path)
		if err != nil {
			return ""
		}
		path = p
	}
	return lastElement(path)
}

// bindBrokenImport binds the name of a failed import to an empty package,
// so its uses add no diagnostic of their own (D3).
func (c *checker) bindBrokenImport(fi *fileInfo, spec *syntax.ImportSpec, path string) {
	name := path[strings.LastIndex(path, "/")+1:]
	if spec.Kind == syntax.ImportNamed && spec.Name != nil {
		name = spec.Name.Name
	}
	c.bindBrokenImportName(fi, spec, path, name)
}

func (c *checker) bindBrokenImportName(fi *fileInfo, spec *syntax.ImportSpec, path, name string) {
	if name == "_" || name == "" {
		return
	}
	if _, dup := fi.scope.names[name]; dup {
		return
	}
	empty := &Package{Path: path, Name: name, Scope: newScope(nil)}
	fi.scope.insert(name, &PkgName{Name: name, Pos: spec.Pos(), Pkg: empty, spec: spec, used: true, broken: true})
}

func (c *checker) checkUnusedImports() {
	_, entry := c.pkg.Scope.names["main"].(*Func)
	for _, fi := range c.files {
		c.cur = fi
		for _, pn := range fi.imports {
			if pn.used {
				continue
			}
			if carried(pn) {
				c.carry(pn, entry)
				continue
			}
			c.errorf(pn.spec, CodeUnused, "package %s is imported and not used", pn.Name)
		}
	}
}

// carried reports an import go of a package whose binding declares
// nothing (interop.md, Checking 6): Go's time/tzdata, whose only effect is
// its initialization. It has no name to use, so the import is its use.
// Only an unused Go import pays the binding's read (gobinding.go reads a
// binding lazily).
func carried(pn *PkgName) bool {
	h := hostOf(pn.Pkg)
	if h == nil {
		return false
	}
	h.load()
	return h.err == "" && len(pn.Pkg.Scope.names) == 0 && len(h.refused) == 0
}

// carry records a carried import in the package of main, which makes the
// binary: the lowering imports the Go package for its initialization
// (Info.Carries). Anywhere else the import would put the package into
// every binary that imports this one, a choice that is only the entry's,
// so it is S108 there.
func (c *checker) carry(pn *PkgName, entry bool) {
	if !entry {
		c.errorf(pn.spec, CodeType, "Go package %s declares nothing; it is carried into the binary for its initialization, and only the package of main, which makes the binary, carries it (interop.md, Checking 6)", pn.Pkg.Path)
		return
	}
	if !slices.Contains(c.pkg.Info.Carries, pn.Pkg.Path) {
		c.pkg.Info.Carries = append(c.pkg.Info.Carries, pn.Pkg.Path)
		slices.Sort(c.pkg.Info.Carries)
	}
}

// ---------------------------------------------------------------- type declarations

// resolveNamed resolves a declared type's body once, with Go types2's object
// colouring: a declaration reached again while it resolves is a cycle.
func (c *checker) resolveNamed(n *Named) {
	if n.state != 0 || n.Obj.Decl == nil {
		return
	}
	if h := hostOf(n.Obj.Pkg); h != nil && h.c != c {
		// A host type resolves in its binding (gobinding.go).
		h.c.resolveNamed(n)
		return
	}
	n.state = 1
	saved, shallow := c.cur, c.shallow
	c.cur, c.shallow = n.Obj.file, 0
	defer func() { c.cur, c.shallow = saved, shallow }()
	d := n.Obj.Decl
	ctx := &typeCtx{params: map[string]*TypeParam{}, decl: true}
	if d.TypeParams != nil {
		kinds, conflicts := c.bracketKinds(n.Obj)
		for _, field := range d.TypeParams.List {
			cons := c.constraintExpr(field)
			for _, name := range field.Names {
				c.preludeName(name) // D-D3; the parameter still binds
				tp := &TypeParam{Name: name.Name, Pos: name.Pos(), id: newID()}
				c.def(name, tp)
				switch {
				case cons != nil:
					tp.Constraint = c.constraint(cons, ctx)
				case conflicts[name.Name]:
					c.errorf(name, CodeType, "type parameter %s is used both as a type and as a row (types.md, Generics 1)", name.Name)
					tp.conflict = true
				default:
					tp.Kind = kinds[name.Name]
				}
				if _, dup := ctx.params[name.Name]; dup {
					c.errorf(name, CodeType, "type parameter %s is declared twice", name.Name)
				}
				ctx.params[name.Name] = tp
				n.TParams = append(n.TParams, tp)
			}
		}
	}
	restore := c.monoOwner(n.TParams)
	defer restore()
	switch spec := d.Spec.(type) {
	case *syntax.StructType:
		ctx.field = true
		s := c.structType(spec, ctx)
		s.obj = n.Obj
		n.under = s
		if c.includesExpanded {
			c.expandInclude(n)
		}
	case *syntax.InterfaceType:
		if x := aliasTarget(d); x != nil {
			// A union whose only variant is an existing type is that type
			// (types.md, Unions 7; F11): every use resolves to the target.
			n.under = invalid
			if len(n.TParams) > 0 {
				c.errorf(d.TypeParams, CodeType, "%s is an alias of its one variant, an existing type, and an alias takes no type parameters yet; write the type where it is used (types.md, Unions 7)", n.Obj.Name)
				n.alias = invalid
				break
			}
			n.alias = c.typExpr(x, ctx)
		} else if len(spec.Union) > 0 {
			n.under = c.unionType(n, spec, ctx)
		} else {
			it := c.interfaceType(spec, ctx, d.Name.Name)
			it.obj = n.Obj
			n.under = it
		}
	case *syntax.MachineType:
		// The state and signal must be unions (checkMachine, after every
		// type declaration: a type still being declared here, on a cycle
		// through this machine, has no underlying type yet).
		n.under = &MachineType{State: c.typExpr(spec.State, ctx), Signal: c.typExpr(spec.Signal, ctx)}
		c.machines = append(c.machines, n.Obj)
	default:
		n.under = c.definedType(n, spec, ctx)
	}
	n.state = 2
}

// bracketKinds returns the solved kind of each unconstrained bracket
// parameter of a type declaration and the parameters whose positions
// disagree (solveDeclKinds).
func (c *checker) bracketKinds(tn *TypeName) (map[string]Kind, map[string]bool) {
	kinds := map[string]Kind{}
	i := 0
	ks := c.kinds.kinds[tn]
	for _, f := range tn.Decl.TypeParams.List {
		for _, n := range f.Names {
			if i < len(ks) {
				kinds[n.Name] = ks[i]
			}
			i++
		}
	}
	return kinds, c.kinds.conflicts[tn]
}

func isErrorName(x syntax.Expr) bool {
	id, ok := x.(*syntax.Ident)
	return ok && id.Name == "error"
}

// constraintExpr returns a bracket field's written constraint, or nil. Ergo
// has no `error` constraint (owner answer 4, stage E1): `[E error]` is S104
// at `error`, and E stays unconstrained, its kind solved by position like
// `[E]`.
func (c *checker) constraintExpr(f *syntax.Field) syntax.Expr {
	if f.Type == nil {
		return nil
	}
	if isErrorName(f.Type) {
		c.errorf(f.Type, CodeUnknown, "error is not a constraint; write [E]: a parameter in an error row is an error row (types.md, Generics 1)")
		return nil
	}
	if id, ok := f.Type.(*syntax.Ident); ok && id.Name == "any" && c.lookupType("any", nil) == nil {
		// The parser took every `any` a constrained group follows
		// (Field.Any); this one gives nothing (types.md, Generics 2).
		c.errorf(f.Type, CodeUnknown, "Ergo has no any; a parameter without a constraint is written bare, [T], and any only ends such a group before a constrained one, [E any, K comparable] (types.md, Generics 2)")
		return nil
	}
	return f.Type
}

func (c *checker) structType(spec *syntax.StructType, ctx *typeCtx) *Struct {
	s := &Struct{}
	seen := map[string]bool{}
	for _, field := range spec.Fields.List {
		t := c.typExpr(field.Type, ctx)
		if field.Include {
			if n := c.includeTarget(field, t); n != nil {
				s.includes = append(s.includes, &include{member: field, at: len(s.Fields), target: n})
			}
			continue
		}
		key := ""
		if field.Key != nil {
			var err error
			key, err = strconv.Unquote(field.Key.Value)
			if err != nil || key == "" || !utf8.ValidString(key) {
				c.errorf(field.Key, CodeType, "a wire key must be non-empty valid UTF-8 (types.md, Codecs 2)")
			}
		}
		for _, name := range field.Names {
			if seen[name.Name] && name.Name != "_" {
				c.errorf(name, CodeType, "field %s is declared twice", name.Name)
				continue
			}
			seen[name.Name] = true
			f := &Field{Name: name.Name, Type: t, Pos: name.Pos(), WireKey: key}
			s.Fields = append(s.Fields, f)
			c.def(name, f)
		}
	}
	return s
}

// interfaceType resolves an operation type. Its laws are not part of the
// type (Law): collect records them with the declaration that holds them,
// the only place the decided parser builds an operation type (typExpr).
func (c *checker) interfaceType(spec *syntax.InterfaceType, ctx *typeCtx, owner string) *Interface {
	it := &Interface{}
	seen := map[string]bool{}
	for _, m := range spec.Methods {
		ft, ok := m.Type.(*syntax.FuncType)
		if !ok || len(m.Names) == 0 {
			c.errorf(m, CodeType, "an interface lists method signatures")
			continue
		}
		name := m.Names[0]
		mctx := &typeCtx{method: true}
		if ctx != nil {
			// A package var's type has no context (FuzzCheck found
			// var A interface{A()} dereferencing it).
			mctx.params, mctx.decl = ctx.params, ctx.decl
		}
		sig := c.signature(ft, mctx)
		if c.pkg.Host() {
			c.hostClauses(c.pkg.Path+"."+owner+"."+name.Name, sig, m)
		}
		if c.pkg.host == nil {
			closeOpTakes(name.Name, sig)
		}
		if ft.Needs != nil && c.pkg.host == nil {
			// A Go interface's method runs Go code: its binding charges
			// go.Foreign at the call (docs/spec/interop.md).
			c.errorf(ft.Needs, CodeType, "an interface method writes no capability row: a provider's requirements are captured when it is built (types.md, Capabilities 4; README Gap 17)")
			sig.Uses = newRow()
		}
		if seen[name.Name] {
			c.errorf(name, CodeType, "method %s is declared twice", name.Name)
			continue
		}
		seen[name.Name] = true
		m := &Method{Name: name.Name, Sig: sig, Pos: name.Pos()}
		if !isExported(name.Name) {
			m.pkg = c.pkg
		}
		it.Methods = append(it.Methods, m)
		c.def(name, m)
		c.defParams(ft, sig)
	}
	// Sorted once, here, for identity (intern.go shape; round 11 M3).
	it.byName = slices.Clone(it.Methods)
	slices.SortStableFunc(it.byName, func(a, b *Method) int { return strings.Compare(a.Name, b.Name) })
	return it
}

// defParams records the declaring names of a signature with no body (an
// operation, an extern function): each named parameter is its Var in sig,
// in the order signature made them.
func (c *checker) defParams(ft *syntax.FuncType, sig *Signature) {
	if ft == nil || ft.Params == nil {
		return
	}
	i := 0
	for _, field := range ft.Params.List {
		if len(field.Names) == 0 {
			i++
			continue
		}
		for _, pn := range field.Names {
			if i < len(sig.Params) {
				c.def(pn, sig.Params[i])
			}
			i++
		}
	}
}

func (c *checker) unionType(n *Named, spec *syntax.InterfaceType, ctx *typeCtx) Type {
	u := &Union{}
	seen := map[int32]bool{}
	for _, x := range spec.Union {
		t := c.typExpr(x, ctx)
		if isInvalid(t) {
			u.Bad = true
			continue
		}
		vn, ok := t.(*Named)
		if !ok {
			c.errorf(x, CodeType, "a union variant is a record declared in this package, not %s (types.md, Unions 1)", t)
			u.Bad = true
			continue
		}
		if p := vn.origin().Obj.Pkg; p != c.pkg {
			if isUnion(t) {
				c.errorf(x, CodeType, "a union variant is a record declared in this package, not %s (types.md, Unions 1)", t)
				u.Bad = true
				continue
			}
			// A union's variants are records of its package (196b Q8, a
			// permanent rule): a union's marker is a function of the
			// variant's type, and a type's functions live in the package
			// that declares it (196m). An error row still names a foreign
			// error type directly.
			c.errorf(x, CodeType, "a union's variants are records of its package; wrap %s: type %s = … | %s{ Cause %s }", t, n.Obj.Name, vn.origin().Obj.Name, t)
			u.Bad = true
			continue
		}
		// The variant's kind comes from its declaration, so a variant that is
		// still resolving (a struct whose field is this union) is accepted.
		if decl := vn.origin().Obj.Decl; decl == nil {
			c.errorf(x, CodeType, "a union variant is a record declared in this package, not %s (types.md, Unions 1)", t)
			u.Bad = true
			continue
		} else if _, isStruct := decl.Spec.(*syntax.StructType); !isStruct {
			c.errorf(x, CodeType, "a union variant is a record declared in this package; %s is %s (types.md, Unions 1)", t, shapeNoun(decl.Spec))
			u.Bad = true
			continue
		}
		key := c.types.id(t)
		if seen[key] {
			c.errorf(x, CodeType, "variant %s is listed twice", t)
			continue
		}
		seen[key] = true
		u.Variants = append(u.Variants, t)
	}
	if u.Bad && len(u.Variants) == 0 {
		// Every variant was lost to a reported error: the declaration is
		// refused, as a defined type over an unknown base is, so every
		// use of the type sees invalid and adds nothing (P4; D3).
		return invalid
	}
	return u
}

// ---------------------------------------------------------------- type expressions

// typeCtx is the context of a type expression: the type parameters in scope
// and the rules for unbound one-letter names (rows-and-constraints §3).
type typeCtx struct {
	params   map[string]*TypeParam // bracket and receiver parameters (196g: every parameter is declared)
	method   bool                  // a method or interface method: no type parameters of its own (D6)
	hostRecv string                // a binding method's receiver name, usable in takes
	decl     bool                  // a type declaration: bracket parameters only
	field    bool                  // a struct field: *T is not a field type (types.md, Borrows 1)
	param    bool                  // a parameter or receiver: *T allowed
	scope    *Scope
	lit      *litParams // a function literal's signature: its untyped parameters (D-E1)
}

// litParams carries a function literal's expected function type into its
// signature: a parameter written without a type takes the expected
// parameter's type (D-E1, #214). With none it is S108, once per literal.
type litParams struct {
	expected *Signature // the expected function type, or nil
	untyped  bool       // a parameter is written without a type
	failed   bool       // an untyped parameter's type was not inferred (reported)
}

// untypedParam types the literal parameter name, the i-th, written without
// a type: the expected function type's i-th parameter type, else S108.
func (c *checker) untypedParam(name *syntax.Ident, i int, lit *litParams) Type {
	if lit != nil {
		lit.untyped = true
		if lit.expected != nil && i < len(lit.expected.Params) {
			return lit.expected.Params[i].Type
		}
	}
	if lit == nil || !lit.failed {
		c.errorf(name, CodeType, "cannot infer the type of %s; write func(%s T)", name.Name, name.Name)
	}
	if lit != nil {
		lit.failed = true
	}
	return invalid
}

// untypedParamNames returns, for a function literal, each parameter
// written without a type with its index among the parameters.
func untypedParamNames(ft *syntax.FuncType) (names []*syntax.Ident, at []int) {
	if ft.Params == nil {
		return nil, nil
	}
	i := 0
	for _, field := range ft.Params.List {
		if len(field.Names) == 0 {
			i++
			continue
		}
		for _, name := range field.Names {
			if field.Type == nil {
				names = append(names, name)
				at = append(at, i)
			}
			i++
		}
	}
	return names, at
}

func (c *checker) lookupType(name string, ctx *typeCtx) Object {
	if ctx != nil {
		if tp, ok := ctx.params[name]; ok {
			return &TypeName{Name: name, Type: tp, Pkg: c.pkg}
		}
	}
	scope := c.pkg.Scope
	if ctx != nil && ctx.scope != nil {
		scope = ctx.scope
	}
	// A type position sees through a refused binder: it was refused as a
	// value binding, so the name keeps its meaning here (P2). It sees
	// through a local too, which hides a package type only in value and
	// pattern positions (types.md, Scope 1; L5): `let point point = …`.
	if o, at := scopeLookupType(scope, name); o != nil {
		// A refused import of a prelude name still binds (D-D3; lookup).
		if imp := c.shadowImport(name, at, c.cur); imp != nil {
			return imp
		}
		return o
	}
	if c.cur != nil {
		if o, ok := c.cur.scope.names[name]; ok {
			return o
		}
	}
	return nil
}

// typExpr resolves a type expression. An invalid type is reported once and
// suppresses every diagnostic that would follow from it (D3).
func (c *checker) typExpr(x syntax.Expr, ctx *typeCtx) Type {
	t := c.typExpr0(x, ctx)
	if n, ok := t.(*Named); ok && n.Orig == nil && len(n.TParams) > 0 {
		c.errorf(x, CodeType, "generic type %s needs type arguments", n)
		return invalid
	}
	if tp, ok := t.(*TypeParam); ok && tp.conflict {
		return invalid
	}
	if tp, ok := t.(*TypeParam); ok && tp.Kind == KindErrors {
		// An error row parameter in a type position names the row's value
		// type, the type of a `case err:` binding (types.md, Generics 1;
		// rows.md, Catch 7; q47).
		return c.rowValue(c.types.row(tp))
	}
	if tp, ok := t.(*TypeParam); ok && tp.Kind != KindType {
		c.errorf(x, CodeType, "%s is %s parameter, not a type", tp.Name, tp.Kind)
		return invalid
	}
	return t
}

func (c *checker) typExpr0(x syntax.Expr, ctx *typeCtx) Type {
	switch x := x.(type) {
	case *syntax.Ident:
		return c.typeName(x, ctx)
	case *syntax.SelectorExpr:
		return c.qualifiedType(x)
	case *syntax.ParenExpr:
		return c.typExpr0(x.X, ctx)
	case *syntax.IndexExpr:
		base := c.typExpr0(x.X, ctx)
		if isInvalid(base) {
			return invalid
		}
		n, ok := base.(*Named)
		if !ok || n.Orig != nil || len(n.TParams) == 0 {
			c.errorf(x, CodeType, "%s is not a generic type", base)
			return invalid
		}
		return c.instantiateExprs(n, x.Indices, x, ctx)
	case *syntax.ArrayType:
		elem := c.typExpr(x.Elt, ctx)
		if x.Len != "" {
			return c.arrayType(x, elem)
		}
		return &Slice{Elem: elem}
	case *syntax.MapType:
		if tn := c.declaredMap(); tn != nil {
			// The package's own Map hides the map type (L6).
			return c.typExpr0(c.mapInstance(x), ctx)
		}
		k := c.typExpr(x.Key, ctx)
		v := c.typExpr(x.Value, ctx)
		if !isInvalid(k) && !comparable(k) {
			c.errorf(x.Key, CodeType, "map key type %s is not comparable", k)
			return invalid
		}
		c.mapKeySite(x.Key, k)
		return &Map{Key: k, Elem: v}
	case *syntax.StarExpr:
		if ctx == nil || !ctx.param {
			c.errorf(x, CodeType, "a borrow *T is only a parameter or receiver type (types.md, Borrows 1)")
			return invalid
		}
		inner := *ctx
		inner.param = false
		elem := c.typExpr(x.X, &inner)
		return &Pointer{Elem: elem}
	case *syntax.FuncType:
		// `escaping` marks a function type that may outlive the call it
		// is passed to or run on another task (196e; ESC1). Only a parameter's type is non-escaping
		// without it (signature); elsewhere it repeats the default.
		inner := typeCtx{}
		if ctx != nil {
			inner = *ctx
		}
		inner.field, inner.param = false, false
		sig := c.signature(x, &inner)
		// A written function type's parameter names declare its
		// parameters (Info.Defs), as an operation's do (defParams); a
		// function literal's parameter of type func(v T) bool needs it.
		c.defParams(x, sig)
		return sig
	case *syntax.StructType, *syntax.InterfaceType:
		// The decided parser builds a record, an operation type or a union
		// only as a type declaration's body (resolveNamed).
		c.errorf(x, CodeType, "a record, an operation type or a union is a declared type: write type T { … }")
		return invalid
	case *syntax.ErrorRow:
		c.errorf(x, CodeType, "an error row appears only after fails in a signature")
		return invalid
	case *syntax.TupleExpr:
		// A tuple type is structural (196t): two or more elements, which
		// the parser guarantees.
		inner := typeCtx{}
		if ctx != nil {
			inner = *ctx
		}
		inner.param = false
		tup := &Tuple{Types: make([]Type, len(x.Elts))}
		for i, e := range x.Elts {
			tup.Types[i] = c.typExpr(e, &inner)
			if isInvalid(tup.Types[i]) {
				return invalid
			}
		}
		return tup
	case *syntax.VariantSpec:
		return c.variantType(x, ctx)
	case *syntax.AsPattern:
		c.errorf(x, CodeType, "expected a type")
		return invalid
	case *syntax.BinaryExpr:
		if x.Op == syntax.OR {
			c.errorf(x, CodeType, "a row A | B is the argument of a row parameter; here a type is expected")
		} else {
			c.errorf(x, CodeType, "expected a type")
		}
		return invalid
	case *syntax.BasicLit, *syntax.CompositeLit, *syntax.KeyValueExpr, *syntax.FuncLit, *syntax.SliceExpr,
		*syntax.TypeGuard, *syntax.CallExpr, *syntax.UnaryExpr, *syntax.TryExpr,
		*syntax.CatchExpr, *syntax.ContextLit, *syntax.MachineType, *syntax.InterpolatedString,
		*syntax.IfStmt, *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
		c.errorf(x, CodeType, "expected a type")
		return invalid
	}
	c.undecided(x, "")
	return invalid
}

func (c *checker) typeName(x *syntax.Ident, ctx *typeCtx) Type {
	o := c.lookupType(x.Name, ctx)
	if o == nil {
		if x.Name == "any" {
			c.errorf(x, CodeUnknown, "Ergo has no any; use a type parameter or a union (types.md, Basic types)")
			return invalid
		}
		c.errorf(x, CodeUnknown, "unknown type %s", x.Name)
		return invalid
	}
	c.recordUse(x, o)
	switch o := o.(type) {
	case *TypeName:
		if o == universeOnce.errorName {
			c.errorf(x, CodeType, "error is only the head of an error row")
			return invalid
		}
		// Keep identity while constructing declaration bodies. An active
		// defined base can temporarily have no body; the unified cycle
		// pass needs this edge, rather than an invalid placeholder.
		if n, ok := o.Type.(*Named); ok && ctx != nil && ctx.decl {
			if aliasTarget(n.Obj.Decl) != nil {
				c.resolveNamed(n)
				return c.aliasOf(n, x)
			}
			if c.shallow > 0 {
				return n // an alias's shallow reading (aliasOf)
			}
			c.resolveNamed(n)
			if !containsInvalid(n) || pendingDefinition(n) {
				return n
			}
			return invalid
		}
		return c.resolvedType(o.Type)
	case *capBinding:
		return c.resolvedType(o.tn.Type)
	case *PkgName:
		c.errorf(x, CodeType, "package %s is not a type", x.Name)
		return invalid
	case *Method:
		// A member law's operation scope holds the operations (laws.go):
		// an operation named like a type of the package hides the type
		// there (K4, G-ex-b-13).
		if tn, ok := c.pkg.Scope.names[x.Name].(*TypeName); ok && c.fn != nil && c.fn.law != nil && c.fn.law.Owner != nil {
			c.errorf(x, CodeType, "%s is the operation %s.%s here, which hides the type %s in the law; rename the operation or the type (types.md, Laws)", x.Name, c.fn.law.Owner.Name, x.Name, tn.Name)
			return invalid
		}
	}
	c.errorf(x, CodeType, "%s is not a type", x.Name)
	return invalid
}

// resolvedType is the semantic lookup boundary for declared types. A
// refused declaration keeps its object identity for references, but every
// use sees invalid, including rows, patterns and capability selectors.
func (c *checker) resolvedType(t Type) Type {
	if n, ok := t.(*Named); ok {
		c.resolveNamed(n)
		if n.Orig == nil && aliasTarget(n.Obj.Decl) != nil {
			t = c.aliasOf(n, nil)
		}
	}
	if containsInvalid(t) {
		return invalid
	}
	return t
}

// aliasTarget returns the one variant of `type A = B` when B is an
// existing type: such a union is that type, an alias (types.md, Unions 7;
// F11). A variant declared in place, `type A = B{…}`, makes a new type, and
// so does a union of two or more variants. It is nil for every other
// declaration.
func aliasTarget(d *syntax.TypeDecl) syntax.Expr {
	if d == nil {
		return nil
	}
	it, ok := d.Spec.(*syntax.InterfaceType)
	if !ok || len(it.Union) != 1 || len(it.Methods) > 0 || len(it.Laws) > 0 {
		return nil
	}
	if _, inline := it.Union[0].(*syntax.VariantSpec); inline {
		return nil
	}
	return it.Union[0]
}

// aliasOf is the type an alias names. An alias reached again while its
// target resolves, from a declaration body the target mentions (`type A =
// actor.Actor[S, E, O]` with `type E = … | Opened{Sub A}`), takes its target
// from a shallow reading of the target expression: the declarations it names
// are their Named types, bodies unread, the edge a declaration body keeps
// for the cycle pass (typeName). It reports nothing; the alias's own
// resolution reads the target again and reports. An alias reached during
// its own shallow reading names itself: S108 at the name that closes the
// cycle.
func (c *checker) aliasOf(n *Named, at syntax.Node) Type {
	if n.state == 1 {
		if n.alias != nil {
			return n.alias
		}
		if at == nil {
			return invalid
		}
		if n.aliasShallow {
			silent := c.silent
			c.silent = 0
			c.errorf(at, CodeType, "the alias %s names itself (types.md, Unions 7)", n.Obj.Name)
			c.silent = silent
			return invalid
		}
		n.aliasShallow = true
		saved := c.cur
		c.cur = n.Obj.file
		c.silent++
		c.shallow++
		t := c.typExpr(aliasTarget(n.Obj.Decl), &typeCtx{params: map[string]*TypeParam{}, decl: true})
		c.shallow--
		c.silent--
		c.cur = saved
		n.aliasShallow = false
		if !isInvalid(t) {
			n.alias = t
		}
		return t
	}
	if n.alias == nil {
		return invalid
	}
	return n.alias
}

func (c *checker) qualifiedType(x *syntax.SelectorExpr) Type {
	id, ok := x.X.(*syntax.Ident)
	if !ok {
		c.errorf(x, CodeType, "expected a type")
		return invalid
	}
	o := c.lookupType(id.Name, nil)
	pn, ok := o.(*PkgName)
	if !ok {
		if o == nil {
			c.errorf(id, CodeUnknown, "unknown package %s", id.Name)
		} else {
			c.errorf(x, CodeType, "%s is not a package", id.Name)
		}
		return invalid
	}
	pn.used = true
	c.recordUse(id, pn)
	if pn.broken {
		return invalid
	}
	m, ok := pn.Pkg.member(x.Sel.Name)
	if !ok || !isExported(x.Sel.Name) {
		c.unknownMember(x, pn)
		return invalid
	}
	tn, ok := m.(*TypeName)
	if !ok {
		c.errorf(x, CodeType, "%s.%s is not a type", id.Name, x.Sel.Name)
		return invalid
	}
	return c.resolvedType(tn.Type)
}

func (c *checker) recordUse(id *syntax.Ident, o Object) {
	if c.useGens != nil && c.curGen != 0 {
		c.useGens[id] = append(c.useGens[id], c.curGen)
	}
	if c.silent == 0 {
		c.pkg.Info.Uses[id] = o
	}
}

// constraint resolves a bracket constraint: comparable, ordered, number or
// an interface with methods (types.md, Generics 2).
func (c *checker) constraint(x syntax.Expr, ctx *typeCtx) Type {
	t := c.typExpr(x, ctx)
	if isInvalid(t) {
		return nil
	}
	it := asInterface(t)
	if it == nil {
		c.errorf(x, CodeType, "a constraint is an interface, not %s", t)
		return nil
	}
	if it.Set == SetAll && len(it.Methods) == 0 {
		return nil
	}
	return t
}

// instantiateExprs instantiates a generic type with written type arguments.
// A row-kinded parameter takes an error type, a capability or a row
// parameter, as a row of one element. The trailing row parameters may be
// left out, and each left out stands for the empty row (types.md,
// Generics 7): Command[Cmd] is Command[Cmd, E] with E empty. A type
// parameter, or a row parameter that a written argument follows, is never
// left out.
func (c *checker) instantiateExprs(n *Named, args []syntax.Expr, at syntax.Node, ctx *typeCtx) Type {
	c.resolveNamed(n)
	if len(args) > len(n.TParams) || len(args) < len(n.TParams) && !trailingRows(n.TParams[len(args):]) {
		c.errorf(at, CodeType, "%s takes %d type arguments, not %d", n.Obj.Name, len(n.TParams), len(args))
		return invalid
	}
	targs := make([]Type, len(n.TParams))
	for i := len(args); i < len(n.TParams); i++ {
		targs[i] = &RowArg{Row: newRow()}
	}
	for i, a := range args {
		tp := n.TParams[i]
		if tp.Kind != KindType {
			ra := c.rowArg(a, tp.Kind, ctx)
			if ra == nil {
				return invalid
			}
			targs[i] = ra
			continue
		}
		if b, ok := unparen(a).(*syntax.BinaryExpr); ok && b.Op == syntax.OR {
			// A row written for a type parameter is the row's value type,
			// as an error row parameter there is (Generics 1; q47).
			ra := c.rowArg(a, KindErrors, ctx)
			if ra == nil {
				return invalid
			}
			targs[i] = c.rowValue(ra.Row)
			continue
		}
		t := c.typExpr(a, ctx)
		if isInvalid(t) {
			return invalid
		}
		targs[i] = t
	}
	c.recordInstantiation(n.Obj.Name, n.TParams, targs, at)
	inst := c.instantiate(n, targs)
	if len(args) > 0 && !c.scriptWritten(inst, args[0]) {
		return invalid
	}
	for i, tp := range n.TParams {
		if tp.Kind == KindType && tp.Constraint != nil && n.state == 2 {
			cons := c.subst(tp.Constraint, paramMap(n.TParams, targs))
			if why := c.satisfies(targs[i], cons); why != "" {
				c.errorf(args[i], CodeType, "%s does not satisfy %s: %s", targs[i], cons, why)
				return invalid
			}
		}
	}
	return inst
}

// trailingRows reports whether every parameter of left, the parameters
// after the written type arguments, ranges over rows, so that each may be
// left out as the empty row.
func trailingRows(left []*TypeParam) bool {
	for _, tp := range left {
		if tp.Kind == KindType {
			return false
		}
	}
	return true
}

// instantiate returns the instance of a generic named type. Its body is
// substituted lazily, so a recursive generic type terminates. The cache is
// keyed by the instance's interned id, (origin, argument ids), and looked up
// before anything is built (class E): an argument interned before costs one
// lookup, however deep it is.
func (c *checker) instantiate(orig *Named, targs []Type) *Named {
	key, infer := c.types.named(orig, targs)
	cache := c.instance
	if infer {
		// The id waits on an unbound row variable: the instance is cached
		// for the component's inference only (class F).
		cache = c.inferInstances
	}
	if prev, ok := cache[key]; ok {
		return prev
	}
	inst := &Named{Obj: orig.Obj, Orig: orig, TArgs: targs, state: 2, ctx: c}
	c.types.remember(inst, key, infer)
	cache[key] = inst
	c.instancesMade++
	return inst
}

func (n *Named) instanceUnder(inst *Named) Type {
	if n.under == nil || inst.ctx == nil {
		return invalid
	}
	if _, ok := scriptArg(inst); ok {
		// A Script's record is derived from its capability (script.go).
		return inst.ctx.scriptUnder(inst)
	}
	m := map[*TypeParam]Type{}
	for i, tp := range n.TParams {
		if i < len(inst.TArgs) {
			m[tp] = inst.TArgs[i]
		}
	}
	return inst.ctx.subst(n.under, m)
}

// ---------------------------------------------------------------- rows in signatures

// rowExpr resolves the elements of a written error row or capability row.
// A refused element, already reported, stands in the row as invalid and the
// valid elements stay: the row is then unknown (Row.unknown), so absence
// checks on it are quiet and presence checks read its known part, as
// go/types keeps a parameter list with an invalid parameter type.
func (c *checker) rowExpr(xs []syntax.Expr, kind Kind, ctx *typeCtx) *Row {
	row := newRow()
	for _, x := range xs {
		t := c.rowElem(x, kind, ctx)
		if t == nil {
			t = invalid
		}
		for _, e := range rowMembers(t) {
			row = row.with(c.types, e)
		}
	}
	return row
}

// rowArg resolves a written row argument of a generic type or function: a
// row `A | B` as a needs or fails row is written, one element being a row
// of one, and an element that is a named row standing for its variants. A
// refused element gives nil, so the instance is invalid, as an invalid type
// argument makes it.
func (c *checker) rowArg(x syntax.Expr, kind Kind, ctx *typeCtx) *RowArg {
	row := newRow()
	for _, e := range orLeaves(x) {
		t := c.rowElem(e, kind, ctx)
		if t == nil {
			return nil
		}
		for _, m := range rowMembers(t) {
			row = row.with(c.types, m)
		}
	}
	return &RowArg{Row: row}
}

// rowMembers is the row a resolved row element t means: the variants of a
// union that names a row (types.md, Error types 4), else t alone. rowElem
// admits a union only when it names a row.
func rowMembers(t Type) []Type {
	if n, ok := t.(*Named); ok {
		if u, ok := n.underlying().(*Union); ok {
			return u.Variants
		}
	}
	return []Type{t}
}

// namedRow reports whether t, written at x in an error row, is a union, and
// then the variants it names (types.md, Error types 4): a row is a set of
// error types, so a union of error types names the set of its variants. A
// union of this package names a row, and its variants become error types
// as if the row listed them. A union of another package, std's included,
// names a row only when each of its variants is an error type of that
// package; otherwise it is S104 at x and vs is nil. A union's variants are
// records of its own package (Unions 1), so a named row never gathers
// another package's failures.
func (c *checker) namedRow(x syntax.Expr, t Type) (vs []Type, isRow bool) {
	n, ok := t.(*Named)
	if !ok {
		return nil, false
	}
	u, ok := n.underlying().(*Union)
	if !ok {
		return nil, false
	}
	if u.Bad {
		return nil, true // a variant is reported already
	}
	if p := n.origin().Obj.Pkg; p != c.pkg {
		for _, v := range u.Variants {
			vn, ok := v.(*Named)
			if !ok || p == nil || !p.IsErrorType(vn, OwnerProduction) {
				c.errorf(x, CodeUnknown, "%s is not an error type: a union of another package names a row only when each variant is an error type there, and %s is not (types.md, Error types 4)", t, v)
				return nil, true
			}
		}
	}
	return u.Variants, true
}

func (c *checker) rowElem(x syntax.Expr, kind Kind, ctx *typeCtx) Type {
	if t, ok := c.wiredElem(x, kind, ctx); ok {
		return t
	}
	var t Type
	switch x := x.(type) {
	case *syntax.Ident:
		o := c.lookupType(x.Name, ctx)
		if o == nil {
			c.errorf(x, CodeUnknown, "unknown %s %s", kindNoun(kind), x.Name)
			return nil
		}
		c.recordUse(x, o)
		switch o := o.(type) {
		case *TypeName:
			t = c.resolvedType(o.Type)
		case *capBinding:
			t = c.resolvedType(o.tn.Type)
		default:
			c.errorf(x, CodeUnknown, "%s is not %s", x.Name, kindNoun(kind))
			return nil
		}
	default:
		t = c.typExpr0(x, ctx)
	}
	if isInvalid(t) {
		return nil
	}
	if tp, ok := t.(*TypeParam); ok {
		if tp.conflict {
			return nil
		}
		if tp.Kind != kind {
			c.errorf(x, CodeType, "%s is %s parameter, used here as %s", tp.Name, tp.Kind, kind)
			return nil
		}
		return tp
	}
	if ra, ok := t.(*RowArg); ok {
		return ra
	}
	switch kind {
	case KindErrors:
		if n, ok := t.(*Named); ok && n.Orig == nil && len(n.TParams) > 0 {
			c.errorf(x, CodeType, "generic type %s needs type arguments", n)
			return nil
		}
		if vs, isRow := c.namedRow(x, t); isRow {
			if vs == nil {
				return nil
			}
			for _, v := range vs {
				c.addErrorType(v.(*Named), c.pkg.OwnerOf(x), x)
			}
			return t
		}
		if !isStructNamed(t) {
			c.errorf(x, CodeUnknown, "%s is not an error type: an error type is a named struct type, or a union of error types that names a row (types.md, Error types 1)", t)
			return nil
		}
		// The row's own owner, not the body being checked: a declaration
		// may be resolved first from a test.
		c.addErrorType(t.(*Named), c.pkg.OwnerOf(x), x)
	case KindUses:
		if n, ok := t.(*Named); ok && len(n.origin().TParams) > 0 && !isCapability(t) {
			if _, ops := n.Underlying().(*Interface); ops {
				if n.Orig == nil {
					c.errorf(x, CodeType, "generic capability %s needs type arguments: write %s[…]", n, n)
				} else {
					c.errorf(x, CodeType, "a capability's type arguments are concrete types, and %s names a type parameter", n)
				}
				return nil
			}
		}
		if !isCapability(t) {
			c.errorf(x, CodeUnknown, "%s is not a capability: a capability is a named interface with methods (rows.md, Capability rows 3)", t)
			return nil
		}
	}
	return t
}

func kindNoun(k Kind) string {
	if k == KindErrors {
		return "error type"
	}
	if k == KindUses {
		return "capability"
	}
	return "type"
}

// ---------------------------------------------------------------- signatures

// walkTypePositions calls f for every name in a type position of x, with
// the kind its position gives (rows-and-constraints §3).
func walkTypePositions(x syntax.Node, k Kind, f func(*syntax.Ident, Kind)) {
	typeWalker{f: f}.walk(x, k)
}

// typeWalker visits the names in type positions with the kind of the
// position. slots, when set, gives the parameter kinds of a generic type
// named as the head of an index expression, so `Task[T, E]` puts E in a row
// position (D6; M6).
type typeWalker struct {
	f     func(*syntax.Ident, Kind)
	slots func(head syntax.Expr) []Kind
	// index, when set, may take over an index expression (returning true)
	// before the default walk.
	index func(ix *syntax.IndexExpr) bool
	// maps, when set, reads `Map[K, V]` as the package's own Map
	// (checker.mapAsDeclared; L6), nil where the form is the map type.
	maps func(m *syntax.MapType) syntax.Expr
}

func (w typeWalker) walk(x syntax.Node, k Kind) {
	switch x := x.(type) {
	case nil:
	case *syntax.Ident:
		w.f(x, k)
	case *syntax.SelectorExpr:
	case *syntax.ParenExpr:
		w.walk(x.X, k)
	case *syntax.BinaryExpr:
		// A row argument written as a union, E | Expired.
		w.walk(x.X, k)
		w.walk(x.Y, k)
	case *syntax.IndexExpr:
		if w.index != nil && w.index(x) {
			return
		}
		w.walk(x.X, KindType)
		var kinds []Kind
		if w.slots != nil {
			kinds = w.slots(x.X)
		}
		for i, idx := range x.Indices {
			kind := KindType
			if i < len(kinds) {
				kind = kinds[i]
			}
			w.walk(idx, kind)
		}
	case *syntax.ArrayType:
		w.walk(x.Elt, KindType)
	case *syntax.MapType:
		if w.maps != nil {
			if ix := w.maps(x); ix != nil {
				w.walk(ix, k)
				return
			}
		}
		w.walk(x.Key, KindType)
		w.walk(x.Value, KindType)
	case *syntax.StarExpr:
		w.walk(x.X, KindType)
	case *syntax.FuncType:
		w.fields(x.Params)
		w.fields(x.Results)
		if x.Needs != nil {
			for _, n := range x.Needs.Names {
				w.walk(n, KindUses)
			}
		}
	case *syntax.ErrorRow:
		for _, t := range x.Types {
			w.walk(t, KindErrors)
		}
	case *syntax.StructType:
		w.fields(x.Fields)
	case *syntax.InterfaceType:
		for _, m := range x.Methods {
			w.walk(m.Type, KindType)
		}
		for _, u := range x.Union {
			w.walk(u, KindType)
		}
	case *syntax.MachineType:
		w.walk(x.State, KindType)
		w.walk(x.Signal, KindType)
	}
}

func (w typeWalker) fields(l *syntax.FieldList) {
	if l == nil {
		return
	}
	for _, field := range l.List {
		w.walk(field.Type, KindType)
	}
}

// slotKinds returns the parameter kinds of the generic type a head names,
// or nil when the head is not a generic type or is still resolving.
func (c *checker) slotKinds(head syntax.Expr) []Kind {
	tn, n := c.namedHead(head)
	if n == nil {
		return nil
	}
	if ks, ok := c.kinds.kinds[tn]; ok {
		// A local declaration's kinds are solved before any resolves, so
		// a recursive head has them too (round-2 M4).
		return ks
	}
	if n.state == 0 {
		c.resolveNamed(n)
	}
	if n.state != 2 {
		return nil
	}
	kinds := make([]Kind, len(n.TParams))
	for i, tp := range n.TParams {
		kinds[i] = tp.Kind
	}
	return kinds
}

// writtenParamType is the type of a parameter whose type is written as x
// and resolves to t: a parameter written as a function type without
// `escaping` is non-escaping (196e; escaping.go), a function's and a law's
// alike. A named function type is escaping, as a field's is.
func writtenParamType(x syntax.Expr, t Type) Type {
	if ft, ok := unparen(x).(*syntax.FuncType); ok && !ft.Escaping.IsValid() {
		if s, ok := t.(*Signature); ok {
			ne := *s
			ne.NonEscaping = true
			return &ne
		}
	}
	return t
}

// signature resolves a function type. Parameters, results, the error row and
// the capability row.
func (c *checker) signature(ft *syntax.FuncType, ctx *typeCtx) *Signature {
	sig := &Signature{Errors: newRow(), Uses: newRow()}
	pctx := *ctx
	pctx.param, pctx.field = true, false
	pctx.lit = nil // a function type inside a parameter is not the literal's
	if ft.Params != nil {
		for _, field := range ft.Params.List {
			var t Type
			if field.Type != nil {
				t = writtenParamType(field.Type, c.typExpr(field.Type, &pctx))
			}
			if len(field.Names) == 0 {
				if t == nil {
					t = c.undecided(field, "a parameter with neither a name nor a type").typ
				}
				sig.Params = append(sig.Params, &Var{Name: "_", Type: t, Pos: field.Pos(), param: true})
			}
			for _, name := range field.Names {
				pt := t
				if field.Type == nil {
					// A function literal's parameter written without a
					// type (D-E1, #214).
					pt = c.untypedParam(name, len(sig.Params), ctx.lit)
				}
				sig.Params = append(sig.Params, &Var{Name: name.Name, Type: pt, Pos: name.Pos(), id: name, param: true})
			}
		}
	}
	rctx := *ctx
	rctx.param, rctx.field = false, false
	rctx.lit = nil
	if ft.Results != nil {
		for _, field := range ft.Results.List {
			if er, ok := field.Type.(*syntax.ErrorRow); ok {
				sig.Fallible = true
				sig.Errors = c.rowExpr(er.Types, KindErrors, &rctx)
				continue
			}
			t := c.typExpr(field.Type, &rctx)
			for range max(1, len(field.Names)) {
				sig.Results = append(sig.Results, t)
			}
		}
	}
	if ft.Needs != nil {
		sig.Uses = c.rowExpr(ft.Needs.Names, KindUses, &rctx)
	}
	if ft.Takes != nil {
		c.takesClause(ft.Takes, sig, ctx.method, ctx.hostRecv)
	}
	return sig
}

// funcSig resolves a function's or method's signature, its type parameters
// (every one declared in brackets, 196g) and its receiver.
func (c *checker) funcSig(f *Func) *Signature {
	if f.state == 2 || f.state == 1 {
		if f.Sig == nil {
			f.Sig = &Signature{Errors: newRow(), Uses: newRow()}
		}
		return f.Sig
	}
	if h := hostOf(f.Pkg); h != nil && h.c != c {
		// A host function or method resolves in its binding (gobinding.go).
		return h.c.funcSig(f)
	}
	f.state = 1
	saved := c.cur
	c.cur = f.file
	defer func() { c.cur = saved }()
	d := f.Decl
	ctx := &typeCtx{params: map[string]*TypeParam{}}
	if c.pkg.Host() && d.Recv != nil && len(d.Recv.List) > 0 && len(d.Recv.List[0].Names) > 0 {
		ctx.hostRecv = d.Recv.List[0].Names[0].Name
		ctx.method = true
	}
	var tparams []*TypeParam
	// The bracket parameters are declared before any constraint resolves,
	// so the instantiations in the constraints and the signature are
	// attributed to them (round-4 M1).
	type pending struct {
		tp *TypeParam
		x  syntax.Expr
	}
	var constraints []pending
	var unconstrained []*TypeParam
	if d.TypeParams != nil {
		for _, field := range d.TypeParams.List {
			cons := c.constraintExpr(field)
			for _, name := range field.Names {
				c.preludeName(name) // D-D3; the parameter still binds
				tp := &TypeParam{Name: name.Name, Pos: name.Pos(), id: newID()}
				c.def(name, tp)
				switch {
				case cons != nil:
					constraints = append(constraints, pending{tp, cons})
				default:
					unconstrained = append(unconstrained, tp)
				}
				if _, dup := ctx.params[name.Name]; dup {
					c.errorf(name, CodeType, "type parameter %s is declared twice", name.Name)
				}
				ctx.params[name.Name] = tp
				tparams = append(tparams, tp)
			}
		}
	}
	// A signature instantiates generic types with the declaration's
	// parameters, as its body does:
	// they own those instantiations, so a growing one is an instantiation
	// cycle before rows are inferred (round-4 M1; go/types mono.go records
	// signature instances too).
	owner := append([]*TypeParam(nil), tparams...)
	restore := c.monoOwner(owner)
	for _, p := range constraints {
		p.tp.Constraint = c.constraint(p.x, ctx)
	}
	c.kindBracketParams(d.Type, ctx, unconstrained)
	c.monoLinkSelf(f, ctx)
	nbracket := len(tparams)
	c.mono.owner = owner
	sig := c.signature(d.Type, ctx)
	restore()
	sig.NBracket = nbracket
	sig.TParams = tparams
	f.Sig = sig
	f.state = 2
	closeTakes(f)
	if c.pkg.Host() && d.Extern != nil {
		c.hostClauses(d.Extern.Target.Value, sig, d)
	}
	if d.Body == nil {
		// A bodiless (extern) function's parameters are declared here; a
		// body's are declared when it is checked.
		c.defParams(d.Type, sig)
	}
	c.inferFlags(f)
	// An inferred row is a row variable (class F): the signature never
	// changes, inference solves the variable.
	if f.inferErrors {
		f.errVar = &RowVar{Fn: f, Kind: KindErrors}
		sig.Errors = c.types.row(f.errVar)
	}
	if f.inferUses {
		f.useVar = &RowVar{Fn: f, Kind: KindUses}
		sig.Uses = c.types.row(f.useVar)
	}
	return sig
}

// kindBracketParams gives each unconstrained bracket parameter of a
// function the kind of its positions in the signature: after fails an
// error row, after needs a capability row, elsewhere a type (grammar.md,
// Declarations 3; 196g as amended). It is the rule solveDeclKinds applies
// to type declarations; a parameter used in two kinds is S108, as there.
func (c *checker) kindBracketParams(ft *syntax.FuncType, ctx *typeCtx, unconstrained []*TypeParam) {
	free := map[*TypeParam]bool{}
	for _, tp := range unconstrained {
		free[tp] = true
	}
	if len(free) == 0 || ft == nil {
		return
	}
	seen := map[*TypeParam]Kind{}
	// A parameter passed to a declaration's parameter whose kinds conflict
	// takes no kind from it: that conflict is reported at the declaration,
	// once (a 196m method's first parameter, `func Run[E](j Job[E])`).
	conflicted := func(ix *syntax.IndexExpr) bool {
		tn, _ := c.namedHead(ix.X)
		if tn == nil || len(c.kinds.conflicts[tn]) == 0 || tn.Decl == nil || tn.Decl.TypeParams == nil {
			return false
		}
		i := 0
		for _, fld := range tn.Decl.TypeParams.List {
			for _, n := range fld.Names {
				if i < len(ix.Indices) && c.kinds.conflicts[tn][n.Name] {
					if id, ok := unparen(ix.Indices[i]).(*syntax.Ident); ok {
						if tp := ctx.params[id.Name]; tp != nil && free[tp] {
							tp.conflict = true
						}
					}
				}
				i++
			}
		}
		return false
	}
	typeWalker{slots: c.slotKinds, index: conflicted, maps: c.mapAsDeclared, f: func(id *syntax.Ident, k Kind) {
		tp := ctx.params[id.Name]
		if tp == nil || !free[tp] || tp.conflict {
			return
		}
		if prev, ok := seen[tp]; ok {
			j, ok := joinKinds(prev, k)
			if !ok {
				tp.conflict = true
				c.errorf(id, CodeType, "%s is used as %s and as %s; a parameter has one kind (types.md, Generics 1)", id.Name, prev, k)
				return
			}
			seen[tp] = j
			tp.Kind = j
			return
		}
		seen[tp] = k
		tp.Kind = k
	}}.walk(ft, KindType)
}

// collectSelfFuncs resolves x.f(…) to package functions (196m; round-6
// ruling B1). A package function whose first parameter's type is T or *T,
// for a non-interface type T declared in this package, is what x.f(…)
// calls on a value of T (rule 1) and what makes T satisfy an operation
// type (rule 2). It stays a function, with a function's rows, and a plain
// call f(x, …) still calls it. A field of T named f wins: x.f is the field
// and the function is reached only by a plain call. main is never reached
// this way.
//
// Several package functions may share a name when each one's first
// parameter is a distinct such type (rule 3); they are called only as
// x.f(…). Any other repetition of a name is the redeclaration it always
// was, reported at each later function.
func (c *checker) collectSelfFuncs() {
	for _, f := range c.funcs {
		if f.Name != "main" {
			// Defensive: selfOwner reads the receiver spelling without reporting.
			c.cur = f.file
			f.owner, f.selfArgs, f.Ptr = c.selfOwner(f)
			if f.owner != nil && asInterface(f.owner) != nil {
				f.opOwner, f.owner = f.owner, nil
			}
		}
	}
	for _, f := range c.pkg.Funcs {
		if len(f.same) == 0 {
			continue
		}
		group := append([]*Func{f}, f.same...)
		// A first parameter that names a duplicated declaration counts as
		// a distinct local type: whether the kept declaration is one
		// depends on which comes first, so the group's verdict must not
		// read it (a follow-on of the duplicate; stage D1 round 2 review
		// T1). Two members naming it still collide, whichever is kept. The
		// restrictions that do not read the declaration (main is never a
		// member; the type arguments are distinct type parameters of the
		// function) still apply first (round 3 review M3).
		owners := map[any]bool{}
		valid := true
		for _, g := range group {
			var key any
			if g.owner != nil {
				key = g.owner
			}
			if g.opOwner != nil {
				key = g.opOwner
			}
			if name, args, _ := selfOwnerName(g.Decl); name != "" && c.dupNames[name] && g.Name != "main" {
				// Defensive: selfArgsOK validates receiver arguments without reporting.
				c.cur = g.file
				if c.selfArgsOK(g.Decl, args) {
					key = name
				}
			}
			if key == nil || owners[key] {
				valid = false
				break
			}
			owners[key] = true
		}
		if valid {
			for _, g := range group {
				g.overloaded = true
				c.def(g.Decl.Name, g) // a later same-named function is in no scope
			}
			c.pkg.Funcs = append(c.pkg.Funcs, f.same...)
			continue
		}
		for _, g := range f.same {
			c.cur = g.file
			if d := derivedOf(f, g); d != nil {
				// One is a refinement's parse function (refine.go).
				c.errorf(g.Decl.Name, CodeType, "%s is already declared in this package: the where of %s derives it (types.md, Refinements 4)", g.Name, d.Name.Name)
			} else if g.Name == "main" {
				// main is never a member (types.md, Methods 3).
				c.errorf(g.Decl.Name, CodeType, "%s is already declared in this package", g.Name)
			} else {
				c.errorf(g.Decl.Name, CodeType, "%s is already declared in this package; functions share a name only when each one's first parameter is a distinct named type declared in this package (types.md, Methods 3)", g.Name)
			}
			g.owner, g.opOwner, g.selfArgs, g.Ptr = nil, nil, nil, false
		}
	}
	for _, f := range c.funcs {
		if f.owner != nil {
			f.owner.addMethod(f)
		}
		if n := f.opOwner; n != nil {
			if n.opFuncs == nil {
				n.opFuncs = map[string]*Func{}
			}
			n.opFuncs[f.Name] = f
		}
	}
	if c.pkg.Path == std.PreludePath {
		for _, f := range c.funcs {
			// Defensive: collOwner classifies a collection receiver without reporting.
			c.cur = f.file
			if key, args, ok := c.collOwner(f); ok {
				if c.pkg.collFuncs == nil {
					c.pkg.collFuncs = map[collKey]*Func{}
				}
				f.selfArgs = args
				c.pkg.collFuncs[key] = f
			}
		}
	}
}

// collOwner reports a prelude function x.f(…) reaches from a slice or a Map
// (D-D6, D-D8): its first parameter is written []T or Map[K, V], with T, K
// and V distinct type parameters of f, which the receiver's element, key
// and value types bind. The arguments are returned in that order.
func (c *checker) collOwner(f *Func) (collKey, []syntax.Expr, bool) {
	d := f.Decl
	if f.Name == "main" || d.Type == nil || d.Type.Params == nil || len(d.Type.Params.List) == 0 {
		return collKey{}, nil, false
	}
	var args []syntax.Expr
	key := collKey{name: f.Name}
	switch t := d.Type.Params.List[0].Type.(type) {
	case *syntax.ArrayType:
		if t.Len != "" {
			return collKey{}, nil, false // the prelude's are on slices
		}
		args = []syntax.Expr{t.Elt}
	case *syntax.MapType:
		args, key.isMap = []syntax.Expr{t.Key, t.Value}, true
	default:
		return collKey{}, nil, false
	}
	if d.TypeParams == nil || !c.selfArgsOK(d, args) {
		return collKey{}, nil, false
	}
	return key, args, true
}

// selfOwner returns the type x.f(…) reaches f from, or nil: the type of
// f's first parameter, written T, *T or, for a generic T, T[A, B] whose
// arguments are distinct type parameters of f, which a call on an instance
// binds.
func (c *checker) selfOwner(f *Func) (*Named, []syntax.Expr, bool) {
	d := f.Decl
	name, args, ptr := selfOwnerName(d)
	if name == "" {
		return nil, nil, false
	}
	tn, ok := c.pkg.Scope.names[name].(*TypeName)
	if !ok {
		return nil, nil, false
	}
	n, _ := tn.Type.(*Named)
	if n != nil && aliasTarget(n.Obj.Decl) != nil {
		// An alias is its type (types.md, Unions 7): f is a function of
		// the target when the target is a plain declared type, and an
		// ordinary function when it is an instance or not a declared type.
		n, _ = c.aliasOf(n, nil).(*Named)
		if n == nil || n.Orig != nil {
			return nil, nil, false
		}
	}
	if n == nil || n.Obj.Pkg != c.pkg || n.method(f.Name) != nil {
		return nil, nil, false
	}
	if it, isIface := n.underlying().(*Interface); isIface && (ptr || it.method(f.Name) != nil) {
		// An operation of the name wins, as a field does; a pointer to an
		// operation type is S108 (Borrows 5). The caller moves an
		// operation type's function to opOwner (G-ex-e-2).
		return nil, nil, false
	}
	// A field of the same name does not stop f: x.f selects the field
	// first, and f stays in the type's operation set, so it satisfies an
	// interface method f (types.md, Structs 1; round 7 review O2).
	if len(args) != len(n.TParams) || !c.selfArgsOK(d, args) {
		return nil, nil, false
	}
	return n, args, ptr
}

// selfOwnerName returns the type name d's first parameter writes, T, *T or
// T[A, B], with the type arguments and whether it is a pointer; "" when
// the first parameter writes another type or d has none. It returns a
// name, not a node: `Map[K, V]` writes the name Map with no Ident of its
// own, and the checker makes no node outside made and clone (provenance).
func selfOwnerName(d *syntax.FuncDecl) (string, []syntax.Expr, bool) {
	if d.Type == nil || d.Type.Params == nil || len(d.Type.Params.List) == 0 {
		return "", nil, false
	}
	rt := d.Type.Params.List[0].Type
	ptr := false
	if s, ok := rt.(*syntax.StarExpr); ok {
		rt, ptr = s.X, true
	}
	var args []syntax.Expr
	if m, ok := rt.(*syntax.MapType); ok {
		// Map[K, V] names a package type only where the package declares
		// its own Map (L6); the caller looks the name up there.
		return "Map", []syntax.Expr{m.Key, m.Value}, ptr
	}
	if ix, ok := rt.(*syntax.IndexExpr); ok {
		rt, args = ix.X, ix.Indices
	}
	id, ok := rt.(*syntax.Ident)
	if !ok {
		return "", nil, false
	}
	// A name the function's own bracket list declares is its type
	// parameter, which shadows a package type of that name (round 6
	// review m1): the function is an ordinary generic function.
	if d.TypeParams != nil {
		for _, field := range d.TypeParams.List {
			for _, name := range field.Names {
				if name.Name == id.Name {
					return "", nil, false
				}
			}
		}
	}
	return id.Name, args, ptr
}

// selfArgsOK reports whether every type argument of a first parameter's
// type is a distinct type parameter its bracket list declares (196g).
func (c *checker) selfArgsOK(d *syntax.FuncDecl, args []syntax.Expr) bool {
	declared := map[string]bool{}
	if d.TypeParams != nil {
		for _, field := range d.TypeParams.List {
			for _, name := range field.Names {
				declared[name.Name] = true
			}
		}
	}
	seen := map[string]bool{}
	for _, a := range args {
		id, ok := a.(*syntax.Ident)
		if !ok || seen[id.Name] {
			return false
		}
		if !declared[id.Name] {
			return false
		}
		seen[id.Name] = true
	}
	return true
}

// inferFlags decides which rows of a function are inferred from its body
// (rows.md, Capability rows 6; rows-and-constraints §7 amendment 1): an
// unexported function with a body, when the row is unwritten. main, LawX,
// exported functions, methods, bodiless declarations and the step of an
// exported machine (the export guard) spell their rows.
func (c *checker) inferFlags(f *Func) {
	d := f.Decl
	if d.Body == nil || isExported(f.Name) || f.Name == "main" || c.guarded[f] {
		return
	}
	hasErrRow := false
	if d.Type.Results != nil {
		for _, r := range d.Type.Results.List {
			if _, ok := r.Type.(*syntax.ErrorRow); ok {
				hasErrRow = true
			}
		}
	}
	f.inferErrors = !hasErrRow
	f.inferUses = d.Type.Needs == nil
}
