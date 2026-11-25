package lint

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The waits of independent-waits (SKILL-RULES SR-ERGO-6, SR-P05): a wait
// is a let whose value exercises capabilities the checker knows in full,
// its footprint (rows.md, Footprints): each capability operation it
// calls and the footprint of each function it calls. A value that runs
// unknown code (a function value, an operation of an operation-type
// value, a host function, a function literal), or starts a task, has no
// known footprint and is no wait here.

// wait is a let's bound variables, the capabilities its value
// exercises, by type, and whether the value may leave the function or
// loop early (a return, break or continue in it, or a try): the next
// statement then runs only when it does not, and run together it would
// run anyway.
type wait struct {
	vars  map[*check.Var]bool
	caps  []string
	leave bool
}

// waitOf is the wait the statement s is, if it is one.
func waitOf(info *check.Info, s syntax.Stmt) (wait, bool) {
	d, ok := s.(*syntax.DeclStmt)
	if !ok || len(d.Decl.Specs) != 1 || len(d.Decl.Specs[0].Values) != 1 {
		return wait{}, false
	}
	sp := d.Decl.Specs[0]
	caps, known := footprintOf(info, sp.Values[0])
	if !known || len(caps) == 0 {
		return wait{}, false
	}
	w := wait{vars: map[*check.Var]bool{}, caps: caps}
	syntax.Inspect(d, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.Ident:
			if v, ok := info.Defs[x].(*check.Var); ok {
				w.vars[v] = true
			}
		case *syntax.ReturnStmt, *syntax.BranchStmt, *syntax.TryExpr:
			w.leave = true
		}
		return true
	})
	return w, true
}

// footprintOf is the capabilities e exercises, and whether every call in
// it is known.
func footprintOf(info *check.Info, e syntax.Expr) ([]string, bool) {
	var caps []string
	known := true
	syntax.Inspect(e, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.FuncLit:
			known = false
			return false
		case *syntax.CallExpr:
			if !check.CallAddsFootprint(info, x) {
				return true
			}
			site := info.Calls[x]
			if t := check.ProviderOpType(info, x); t != nil {
				caps = append(caps, t.String())
				return true
			}
			if site == nil || site.Kind != check.CallFunc || site.Func == nil || site.Func.Decl == nil || site.Func.Pkg == nil || site.Func.Pkg.Info == nil {
				known = false
				return true
			}
			fp, ok := site.Func.Pkg.Info.Footprints[site.Func.Decl]
			if !ok || fp.Opaque || fp.Tasks {
				known = false
				return true
			}
			for _, c := range fp.Caps {
				caps = append(caps, c.String())
			}
		}
		return true
	})
	return caps, known
}

// independent reports a second wait that reads no variable the first
// binds and shares no capability with it.
func independent(info *check.Info, first, second wait, s syntax.Stmt) bool {
	for _, c := range second.caps {
		for _, d := range first.caps {
			if c == d {
				return false
			}
		}
	}
	reads := false
	syntax.Inspect(s, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok {
			if v, ok := info.Uses[id].(*check.Var); ok && first.vars[v] {
				reads = true
			}
		}
		return !reads
	})
	return !reads
}
