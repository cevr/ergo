package lower

import (
	"maps"
	"slices"
	"sort"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The naming pass. Before any body is lowered, it gives every package-scope
// Go entity the program can need its Go name, from the declarations and the
// checker's Info alone: it lowers no body and formats nothing. The lowering
// then names only locals, each in a scope whose parent holds every package
// name, so no local captures a package entity and no package entity a local
// (names.go). A package name the lowering asks for that the pass did not
// give is an internal compiler error (pkgName).
//
// The pass reserves, in this order:
//
//  1. the user package's declarations, in source order, file by file: types,
//     functions, lets and context builders, so a source name keeps its
//     spelling unless Go reserves it;
//  2. the generated package entities: the runtime, time and strconv imports, the
//     environment type, the host's Console adapter, the pattern test
//     helper, the call and declaration tables with their entry types
//     (calls.go, #230), the package initializer (decls.go, initFunc), the
//     type-set interfaces of ordered and number (decls.go, constraint) and
//     the imports of the Go packages that user externs target, in path
//     order (calls.go, hostImportNames);
//  3. every type and function of every library package the program reaches
//     (the prelude, imported std packages and imported user packages, by
//     path), used or not, then the lets of each imported user package
//     (sources.go), in initialization order;
//  4. the operations adapters of every operations conversion, the text
//     functions every error type's text needs, and the default function of
//     every defined type or instance whose default holds a Map (types.go,
//     Defaults), in a sorted order.
//
// Reserving an unused library or generated name only renames a local that
// would have taken it; it emits nothing.
//
// Prior art: Gleam's JavaScript backend registers a module's definitions
// before it emits bodies, then allocates locals by counters per scope;
// ReScript prints identifiers once their scope is known.

// nameAll is the naming pass.
func (l *lowerer) nameAll() {
	if l.lib != "" {
		l.nameExports()
	}
	l.declareNames()
	for _, p := range l.sourcedPkgs() {
		for _, tn := range l.canon.types(p) {
			if tn.Decl != nil {
				if _, ok := tn.Decl.Spec.(*syntax.MachineType); ok {
					l.pkgName(pkgKey{"machine initial", tn}, "ergoInitial"+tn.Name)
					l.nameFlatRuns(tn)
				}
			}
		}
	}
	l.pkgName(pkgKey{"import", runtimeImport}, "ergo")
	l.pkgName(pkgKey{"import", "time"}, "time")
	l.pkgName(pkgKey{"import", "embed"}, "embed")
	l.pkgName(pkgKey{"import", "strconv"}, "strconv") // interpolation (interp.go)
	if valueRule() {
		l.pkgName(pkgKey{"import", "maps"}, "maps")     // copies (values.go)
		l.pkgName(pkgKey{"import", "slices"}, "slices") // copies and clipped appends (values.go)
	}
	l.pkgName(pkgKey{"import", taskImport}, "rt")
	l.pkgName(pkgKey{"env", nil}, "Env")
	l.pkgName(pkgKey{"host", "Console"}, "hostConsole")
	l.pkgName(pkgKey{"is", nil}, "ergoIs")
	l.pkgName(pkgKey{"call", nil}, "ergoCall")
	l.pkgName(pkgKey{"calls", nil}, "ergoCalls")
	l.pkgName(pkgKey{"decl", nil}, "ergoDecl")
	l.pkgName(pkgKey{"decls", nil}, "ergoDecls")
	l.pkgName(pkgKey{"inlines", nil}, "ergoInlines") // inline.go, inlineTable
	l.pkgName(pkgKey{"init", nil}, "ergoInit")
	l.pkgName(pkgKey{"constraint", check.SetOrdered}, "ergoOrdered")
	l.pkgName(pkgKey{"constraint", check.SetNumber}, "ergoNumber")
	libs := l.libraries()
	l.hostImportNames(append([]*check.Package{l.pkg}, libs...))
	for _, p := range libs {
		for _, tn := range l.canon.types(p) {
			l.typeName(tn)
		}
		for _, f := range l.canon.funcs(p) {
			l.pkgName(pkgKey{"func", f}, f.Name)
		}
	}
	l.reserveOwned(append([]*check.Package{l.pkg}, libs...)) // values.go, V5
	l.reserveOk(append([]*check.Package{l.pkg}, libs...))    // update.go
	l.reserveKnown(append([]*check.Package{l.pkg}, libs...)) // known.go
	// A library's lets (user or embedded std), which the program lowers, used or not
	// (sources.go), and the builders of its named contexts, one for each
	// set of providers a with of the program acquires (196b U4), in its
	// source order.
	for _, p := range l.initLibraries() {
		l.eachDecl(p, func(d syntax.Decl) {
			switch d.(type) {
			case *syntax.VarDecl:
				l.declareName(d)
			case *syntax.ContextDecl:
				if !p.Std() {
					l.declareName(d)
				}
			}
		})
	}
	// A std package's named contexts have no source in the program; the
	// builders of those a with of the program names are std bodies,
	// named here by package, then context name (lower-gaps G2, batch 11).
	for _, p := range libs {
		if !p.Std() {
			continue
		}
		for _, name := range slices.Sorted(maps.Keys(p.Contexts)) {
			w := p.Contexts[name]
			if !l.stdContext(w) {
				continue
			}
			for _, with := range l.withsOf(w) {
				l.pkgName(pkgKey{"context", l.acquiredSet(with)}, "build"+w.Name)
			}
		}
	}
	type entry = generatedEntry
	var generated []entry
	for _, p := range append([]*check.Package{l.pkg}, libs...) {
		var convs []check.Conversion
		for x, c := range p.Info.Implicit {
			if l.readsFact(p, x) {
				convs = append(convs, c)
			}
		}
		for x, cs := range p.Info.ImplicitElems {
			if l.readsFact(p, x) {
				convs = append(convs, cs...)
			}
		}
		for _, w := range p.Contexts {
			for _, e := range w.Entries {
				convs = append(convs, e.Conversions...)
			}
		}
		for x, with := range p.Info.Withs {
			if !l.readsFact(p, x) {
				continue
			}
			for _, w := range with.Contexts {
				if w.Lit != nil {
					for _, e := range w.Entries {
						convs = append(convs, e.Conversions...)
					}
				}
			}
		}
		for x, with := range p.Info.ContextValues {
			if w := with.Contexts[0]; l.readsFact(p, x) && w.Lit != nil {
				for _, e := range w.Entries {
					convs = append(convs, e.Conversions...)
				}
			}
		}
		if l.tests != nil && p == l.pkg {
			// A law's run converts each provider into its operation type
			// (tests.go, lawRuns).
			for _, r := range l.runLaws() {
				if r.Law.Unrun != "" {
					continue
				}
				for _, b := range r.Bindings {
					if bindingUnrun(b) != "" {
						continue
					}
					for _, lp := range b {
						convs = append(convs, lp.Conv)
					}
				}
			}
			// A generator converts each value a builder builds into its
			// operation type (builders.go).
			seen := map[check.Type]bool{}
			l.genRoots(func(t check.Type) {
				l.genNamed(t, seen, func(n *check.Named) {
					if isOps(n) {
						convs = append(convs, l.opBuilder(n, nil).Conv)
					}
				})
			})
		}
		for _, c := range convs {
			key, base, ok := l.adapterName(c)
			if !ok {
				continue
			}
			generated = append(generated, entry{pkgKey{"adapter", key}, "adapter " + key, base})
		}
		for _, tn := range l.canon.types(p) {
			named, ok := tn.Type.(*check.Named)
			if !ok || !l.errorType(named, l.buildView()) || tn.Decl == nil {
				continue
			}
			for _, root := range l.textRoots(named) {
				l.quiet(func() {
					l.textTypes(root, tn.Decl.Name, map[pkgKey]bool{}, func(t check.Type) {
						key, base := l.textKey(t, tn.Decl.Name)
						generated = append(generated, entry{key, "text " + l.typeKey(t), base})
					})
				})
			}
		}
	}
	l.reserveScripts(func(key pkgKey, base string) {
		generated = append(generated, entry{key, "script " + key.obj.(string), base})
	})
	l.reserveDefaults(func(key pkgKey, base string) {
		generated = append(generated, entry{key, "default " + key.obj.(string), base})
	})
	l.reserveEquals(append([]*check.Package{l.pkg}, libs...), func(key pkgKey, t check.Type) {
		generated = append(generated, entry{key, "equal " + key.obj.(string), equalBase(t)})
	})
	l.reserveClones(append([]*check.Package{l.pkg}, libs...), func(key pkgKey, t check.Type) {
		generated = append(generated, entry{key, "clone " + key.obj.(string), cloneBase(t)})
	})
	l.reserveWires(append([]*check.Package{l.pkg}, libs...), func(key pkgKey, base, sort string) {
		generated = append(generated, entry{key, sort, base})
	})
	for _, e := range sortGenerated(generated) {
		l.pkgName(e.key, e.base)
	}
	l.nameTests() // the test build's names (tests.go); none in the run build
	l.prenamed = true
}

// generatedEntry is a generated package entity the naming pass reserves:
// its key, its canonical sort string and its base Go name.
type generatedEntry struct {
	key  pkgKey
	sort string
	base string
}

// sortGenerated orders the generated entries by their sort strings. The
// order is total: each entry's sort string is its key's canonical
// spelling (typeKey), so no map's iteration order and no display decides
// which entry takes a base name first (196b). Two entries that share a sort
// string are one entity, met twice; a different key or base under one
// sort string would make the order partial, and is a compiler defect.
func sortGenerated(generated []generatedEntry) []generatedEntry {
	sort.Slice(generated, func(i, j int) bool { return generated[i].sort < generated[j].sort })
	for i := 1; i < len(generated); i++ {
		a, b := generated[i-1], generated[i]
		if a.sort == b.sort && (a.key != b.key || a.base != b.base) {
			ice(nil, "two generated entries share the sort key %q", a.sort)
		}
	}
	return generated
}

// adapterKey identifies the adapter of an operations conversion by the
// canonical keys of its source and target types (typeKey): a named type by
// its package's full path, so two packages' Q, whose display is lib.Q in
// both, never share one (196b), and a type parameter by its declaration,
// whose adapter is generic over it (D-E1, C8), so two functions' T never
// share one, in one file or in two (paramFile).
func (l *lowerer) adapterKey(c check.Conversion) string {
	return l.typeKey(c.From) + " => " + l.typeKey(c.To)
}

// adapterName is the key and the base Go name of the adapter of the
// operations conversion c, and false when c is not one an adapter lowers:
// its source is a named type, a type parameter, or a pointer to either,
// and its target a named operation type.
func (l *lowerer) adapterName(c check.Conversion) (key, base string, ok bool) {
	to, ok := c.To.(*check.Named)
	if c.Kind != check.ConvOperations || !ok {
		return "", "", false
	}
	if to.Obj.Pkg.Host() {
		// One adapter per Go interface (gointerface.go); a Go value needs
		// none.
		if hostSourced(c.From) {
			return "", "", false
		}
		return goInterfaceKey(l, to), "goAs" + to.Obj.Name, true
	}
	var from string
	if named, _, isNamed := opsSource(c.From); isNamed {
		from = named.Obj.Name
	} else if tp, _ := paramSource(c.From); tp != nil {
		from = tp.Name
	} else {
		return "", "", false
	}
	return l.adapterKey(c), "ops" + from + "As" + to.Obj.Name, true
}

// paramSource is the type parameter an operations conversion converts
// from: T, or T behind a pointer *T (ptr); nil for any other source.
func paramSource(from check.Type) (tp *check.TypeParam, ptr bool) {
	if p, isPtr := from.(*check.Pointer); isPtr {
		from, ptr = p.Elem, true
	}
	tp, _ = from.(*check.TypeParam)
	return tp, ptr
}

// convParams are the type-kinded type parameters an operations
// conversion's source and target name, in order of first mention.
func convParams(c check.Conversion) []*check.TypeParam {
	out := freeTypeParams(c.From)
	for _, tp := range freeTypeParams(c.To) {
		if !slices.Contains(out, tp) {
			out = append(out, tp)
		}
	}
	return out
}

// opsSource is the named type an operations conversion converts from: T,
// or T behind a pointer *T (ptr). ok is false for any other source.
func opsSource(from check.Type) (named *check.Named, ptr, ok bool) {
	if p, isPtr := from.(*check.Pointer); isPtr {
		from, ptr = p.Elem, true
	}
	named, ok = from.(*check.Named)
	return named, ptr, ok
}

// libraryPackages are the packages other than the user's that the user
// package reaches through the Info facts this build reads (owners.go),
// transitively, ordered by path.
func (l *lowerer) libraryPackages() []*check.Package {
	seen := map[*check.Package]bool{l.pkg: true}
	var out []*check.Package
	var add func(p *check.Package)
	add = func(p *check.Package) {
		if p == nil || seen[p] {
			return
		}
		if p.Host() {
			// A Go package (import go) has no Ergo source to lower: its
			// declarations are Go's, named through its Go import
			// (hostImportNames).
			seen[p] = true
			l.goPkgs = append(l.goPkgs, p)
			return
		}
		if p.Info == nil {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	visited := map[*check.Context]bool{}
	visit := func(p *check.Package) {
		info := p.Info
		for id, o := range info.Uses {
			if !l.readsFact(p, id) {
				continue
			}
			switch o := o.(type) {
			case *check.PkgName:
				add(o.Pkg)
			case *check.TypeName:
				add(o.Pkg)
			case *check.Func:
				add(o.Pkg)
			}
		}
		for x, t := range info.Types {
			if n, ok := t.(*check.Named); ok && l.readsFact(p, x) {
				add(n.Obj.Pkg)
				// A Go type inside another (go.Ref[url.URL],
				// Option[go.Ref[bufio.Reader]]) is Go's, named through
				// its package's import.
				for _, h := range hostPkgsIn(n, nil) {
					add(h)
				}
			}
		}
		// A dot call reaches a function no name uses: the prelude's
		// xs.Append(x) on a slice (D-D8).
		for x, c := range info.Calls {
			if c.Func != nil && l.readsFact(p, x) {
				add(c.Func.Pkg)
			}
		}
		// A with reaches the package of each context it builds, and of
		// every context that one embeds, at any depth, with no name of the
		// package written (196b U4): `with mem.Paying { … }` names mem only
		// as an operand, and an embedding of m.W ⊃ n.W reaches n through
		// m. That package's facts are then visited like any other's: its
		// providers' types and functions, and its own withs.
		for x, with := range info.Withs {
			if !l.readsFact(p, x) {
				continue
			}
			for ; with != nil; with = with.Next {
				for _, w := range with.Contexts {
					contextPackages(w, add, visited)
				}
			}
		}
		for x, with := range info.ContextValues {
			if l.readsFact(p, x) {
				contextPackages(with.Contexts[0], add, visited)
			}
		}
	}
	visit(l.pkg)
	for i := 0; i < len(out); i++ {
		visit(out[i])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// contextPackages adds the package of w and of each context w embeds,
// transitively, each context once.
func contextPackages(w *check.Context, add func(*check.Package), visited map[*check.Context]bool) {
	if w == nil || visited[w] {
		return
	}
	visited[w] = true
	add(w.Package())
	for _, e := range w.Entries {
		if e != nil {
			contextPackages(e.Embedded, add, visited)
		}
	}
}

// quiet runs f and drops a refusal: the naming pass meets the same forms
// the lowering refuses later, where the refusal is located and reported.
func (l *lowerer) quiet(f func()) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(refusal); !ok {
				panic(r)
			}
		}
	}()
	f()
}
