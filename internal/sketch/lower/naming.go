package lower

import (
	"fmt"
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
	l.declareNames()
	for _, p := range l.sourcedPkgs() {
		for _, tn := range p.Types {
			if tn.Decl != nil {
				if _, ok := tn.Decl.Spec.(*syntax.MachineType); ok {
					l.pkgName(pkgKey{"machine initial", tn}, "ergoInitial"+tn.Name)
				}
			}
		}
	}
	l.pkgName(pkgKey{"import", runtimeImport}, "ergo")
	l.pkgName(pkgKey{"import", "time"}, "time")
	l.pkgName(pkgKey{"import", "strconv"}, "strconv") // interpolation (interp.go)
	l.pkgName(pkgKey{"import", taskImport}, "rt")
	l.pkgName(pkgKey{"env", nil}, "Env")
	l.pkgName(pkgKey{"host", "Console"}, "hostConsole")
	l.pkgName(pkgKey{"is", nil}, "ergoIs")
	l.pkgName(pkgKey{"call", nil}, "ergoCall")
	l.pkgName(pkgKey{"calls", nil}, "ergoCalls")
	l.pkgName(pkgKey{"decl", nil}, "ergoDecl")
	l.pkgName(pkgKey{"decls", nil}, "ergoDecls")
	l.pkgName(pkgKey{"init", nil}, "ergoInit")
	l.pkgName(pkgKey{"constraint", check.SetOrdered}, "ergoOrdered")
	l.pkgName(pkgKey{"constraint", check.SetNumber}, "ergoNumber")
	libs := l.libraries()
	l.hostImportNames(append([]*check.Package{l.pkg}, libs...))
	for _, p := range libs {
		for _, tn := range p.Types {
			l.typeName(tn)
		}
		for _, f := range p.Funcs {
			l.pkgName(pkgKey{"func", f}, f.Name)
		}
	}
	// A sourced library's lets, which the program lowers, used or not
	// (sources.go), in its source order.
	for _, p := range l.sourcedLibs() {
		l.eachDecl(p, func(d syntax.Decl) {
			if vd, ok := d.(*syntax.VarDecl); ok {
				l.declareName(vd)
			}
		})
	}
	type entry struct {
		key  pkgKey
		sort string
		base string
	}
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
		for _, c := range convs {
			key, base, ok := l.adapterName(c)
			if !ok {
				continue
			}
			generated = append(generated, entry{pkgKey{"adapter", key}, "adapter " + key, base})
		}
		for _, tn := range p.Types {
			named, ok := tn.Type.(*check.Named)
			if !ok || !l.errorType(named, l.buildView()) || tn.Decl == nil {
				continue
			}
			for _, root := range l.textRoots(named) {
				l.quiet(func() {
					l.textTypes(root, tn.Decl.Name, map[pkgKey]bool{}, func(t check.Type) {
						key, base := l.textKey(t, tn.Decl.Name)
						generated = append(generated, entry{key, "text " + base + " " + t.String(), base})
					})
				})
			}
		}
	}
	l.reserveDefaults(func(key pkgKey, base string) {
		generated = append(generated, entry{key, "default " + key.obj.(string), base})
	})
	sort.SliceStable(generated, func(i, j int) bool { return generated[i].sort < generated[j].sort })
	for _, e := range generated {
		l.pkgName(e.key, e.base)
	}
	l.nameTests() // the test build's names (tests.go); none in the run build
	l.prenamed = true
}

// adapterKey identifies the adapter of an operations conversion by its
// source and target types. A conversion inside a generic function also
// names the declaration of each type parameter it mentions, whose adapter
// is generic over them (D-E1, C8), so two functions' T never share one,
// in one file or in two (paramFile).
func (l *lowerer) adapterKey(c check.Conversion) string {
	key := c.From.String() + " => " + c.To.String()
	for _, tp := range convParams(c) {
		key += fmt.Sprintf(" %s@%v%s", tp.Name, tp.Pos, l.paramFile(tp))
	}
	return key
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
		if p == nil || seen[p] || p.Info == nil {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
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
			}
		}
		// A dot call reaches a function no name uses: the prelude's
		// xs.Append(x) on a slice (D-D8).
		for x, c := range info.Calls {
			if c.Func != nil && l.readsFact(p, x) {
				add(c.Func.Pkg)
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
