package eval

import (
	"fmt"
	"sort"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Initialization (plan section 4: initialization is an execution
// dependency). The native runner runs every package's initializers before
// its first test: each imported package's before the package that imports
// it, and within a package in Info.InitOrder, Go's order. Check-time
// evaluation runs the same set in the same order, all of them, not only
// those an obligation reads, so an initializer that defects is never left
// out to make the two agree. An initializer whose footprint is not empty,
// or that uses a form the evaluator does not run, skips every obligation:
// no obligation runs under a world the native runner would not build.

// initFailure is why the initializers did not all run.
type initFailure struct {
	status Status
	reason string
	site   Site
}

// initialize runs the initializers of pkg and of every package it
// imports, and binds their lets in in.globals.
func (in *interp) initialize(pkg *check.Package) *initFailure {
	in.globals = map[*check.Var]*slot{}
	var order []*check.Package
	seen := map[*check.Package]bool{}
	var visit func(p *check.Package)
	visit = func(p *check.Package) {
		if p == nil || seen[p] || p.Host() || p.Info == nil {
			return
		}
		seen[p] = true
		for _, dep := range imports(p) {
			visit(dep)
		}
		order = append(order, p)
	}
	visit(pkg)
	for _, p := range order {
		if f := in.initPackage(p); f != nil {
			return f
		}
	}
	// Then each package's machines' initial values, in the same order
	// (lower/decls.go initFunc).
	for _, p := range order {
		if f := in.initMachines(p); f != nil {
			return f
		}
	}
	return nil
}

// imports are the packages p names, sorted by path, as gc orders the
// initialization of a package's imports. An import is used (an unused
// one is a diagnostic), so its name is in Uses.
func imports(p *check.Package) []*check.Package {
	var out []*check.Package
	seen := map[*check.Package]bool{}
	for _, o := range p.Info.Uses {
		if pn, ok := o.(*check.PkgName); ok && pn.Pkg != nil && !seen[pn.Pkg] {
			seen[pn.Pkg] = true
			out = append(out, pn.Pkg)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// initPackage runs p's initializers in InitOrder.
func (in *interp) initPackage(p *check.Package) *initFailure {
	if len(p.Info.InitOrder) == 0 {
		return nil
	}
	specs := map[syntax.Expr]*syntax.VarSpec{}
	decls := map[*syntax.VarSpec]*syntax.VarDecl{}
	for _, f := range p.Files() {
		for _, d := range f.Decls {
			vd, ok := d.(*syntax.VarDecl)
			if !ok {
				continue
			}
			for _, s := range vd.Specs {
				decls[s] = vd
				for _, v := range s.Values {
					specs[v] = s
				}
			}
		}
	}
	done := map[*syntax.VarSpec]bool{}
	for _, ini := range p.Info.InitOrder {
		spec := specs[ini.Value]
		if spec == nil {
			ice("the initializer at %d of %s has no let", ini.Value.Pos(), p.Path)
		}
		if done[spec] {
			continue
		}
		done[spec] = true
		decl := decls[spec]
		site := in.site(p, decl, spec.Pos())
		if decl.Extern != nil {
			return &initFailure{Skipped, fmt.Sprintf("the package let at %s is a Go value, which check-time evaluation never reads", siteText(site)), site}
		}
		if why := in.impure(p, spec); why != "" {
			return &initFailure{Skipped, fmt.Sprintf("the package initializer at %s cannot run at check time: %s", siteText(site), why), site}
		}
		fr := in.newFrame(p, decl)
		ab := in.letSpec(fr, spec)
		if ab != nil {
			at := in.site(ab.pkg, ab.decl, ab.at)
			switch ab.kind {
			case ctlDefect:
				return &initFailure{Defect, "a package initializer hit a defect: " + ab.msg, at}
			case ctlExhausted:
				return &initFailure{Incomplete, "a package initializer: " + ab.msg, at}
			case ctlUnsupported:
				return &initFailure{Skipped, fmt.Sprintf("the package initializer at %s cannot run at check time: %s", siteText(site), ab.msg), site}
			case ctlFail, ctlEscape:
				return &initFailure{Defect, "a package initializer failed: " + describe(ab.val), at}
			}
			ice("the control %d left a package initializer", ab.kind)
		}
		for v, s := range fr.vars {
			s.lend()
			in.globals[v] = s
		}
	}
	return nil
}

func siteText(s Site) string {
	return fmt.Sprintf("%s:%d:%d", s.File, s.Line, s.Column)
}
