package lower

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestRuntime199DefectRecovery extends the defect owner's guard into rt
// (runtime.md, G2; runtime-199 stage 4b m4), resolving every name through
// go/types:
//
//   - rt's recovery APIs are derived from rt: each rt function or method
//     whose body, its literals included, calls recover, and each that
//     reaches one through rt's own calls. v1 enters them only at the edges
//     below, and each body it hands rt.Run or rt.Go is run by guarded, so v1
//     recovers a fresh panic before rt's fallback could;
//   - in v1, every recover's value flows into capture in the same function:
//     capture(t, recover()), or p := recover() and capture(…, p);
//   - v1 never uses rt's other defect paths: (*Cause).AddDefect outside
//     capture, (*Cause).Error (rt's own formatter, not the report), or
//     runtime/debug.Stack outside defectOf, in a call or a method value.
func TestRuntime199DefectRecovery(t *testing.T) {
	rtSrc := typedPackage(t, filepath.Join("..", "..", "..", "runtime", "rt"))
	v1 := typedPackage(t, filepath.Join("..", "..", "..", "runtime", "v1"))

	// rt's recovery APIs: a fixpoint over rt's static calls.
	direct, calls := map[string]bool{}, map[string]map[string]bool{}
	rtSrc.owners(func(owner string, decl ast.Decl) {
		if _, ok := decl.(*ast.FuncDecl); !ok {
			return
		}
		calls[owner] = map[string]bool{}
		// A recover whose value is kept (assigned or passed on) records a
		// panic; one only compared with nil, as the trace's sink guard
		// does, drops it and makes no defect.
		isRecover := func(e ast.Expr) bool {
			c, ok := ast.Unparen(e).(*ast.CallExpr)
			if !ok {
				return false
			}
			id, ok := ast.Unparen(c.Fun).(*ast.Ident)
			b, isBuiltin := rtSrc.info.Uses[id].(*types.Builtin)
			return ok && isBuiltin && b.Name() == "recover"
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.AssignStmt:
				direct[owner] = direct[owner] || slices.ContainsFunc(n.Rhs, isRecover)
			case *ast.CallExpr:
				direct[owner] = direct[owner] || slices.ContainsFunc(n.Args, isRecover)
			case *ast.Ident:
				if o, ok := rtSrc.info.Uses[n].(*types.Func); ok && o.Pkg() == rtSrc.pkg {
					calls[owner][funcKey(o)] = true
				}
			}
			return true
		})
	})
	recovering := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for owner, callees := range calls {
			if recovering[owner] {
				continue
			}
			if direct[owner] {
				recovering[owner] = true
				changed = true
				continue
			}
			for c := range callees {
				if recovering[c] {
					recovering[owner] = true
					changed = true
					break
				}
			}
		}
	}
	if !recovering["Run"] || !recovering["Scope.End"] || !recovering["Release"] {
		t.Fatalf("the rt scan no longer finds rt's recovery: %v", sortedKeys(recovering))
	}

	// The edges v1 may take into them, and what each must hand rt.
	allowed := map[string]string{
		"runOn -> Run":           "guarded",
		"Go -> Go":               "guarded",
		"Scope.end -> Scope.End": "",
		"release -> Release":     "classify",
		// timeout.After's body child and its deadline child.
		"After -> AfterSpan":       "guarded",
		"deadlines.NewTimer -> Go": "guarded",
		// actor.StartMachine's runner task and an entry's timer child (MU3).
		"MachineStart -> Go":       "guarded",
		"Actor[S, E, D].arm -> Go": "guarded",
	}
	isRT := func(f *types.Func) bool {
		return f.Pkg() != nil && strings.HasSuffix(f.Pkg().Path(), "/runtime/rt")
	}
	v1Func := func(e ast.Expr) string {
		var name string
		ast.Inspect(e, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				if f, ok := v1.info.Uses[id].(*types.Func); ok && f.Pkg() == v1.pkg && (f.Name() == "guarded" || f.Name() == "classify") {
					name = f.Name()
				}
			}
			return true
		})
		return name
	}
	seen := map[string]bool{}
	v1.owners(func(owner string, decl ast.Decl) {
		ast.Inspect(decl, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				f := calledFunc(v1, n.Fun)
				if f == nil || !isRT(f) || !recovering[funcKey(f)] {
					return true
				}
				edge := owner + " -> " + funcKey(f)
				seen[edge] = true
				want, ok := allowed[edge]
				if !ok {
					t.Errorf("v1 %s enters rt's recovery API %s outside the defect owner's edges", owner, funcKey(f))
					return true
				}
				if want == "" {
					return true
				}
				handed := false
				for _, a := range n.Args {
					handed = handed || v1Func(a) == want
				}
				if !handed {
					t.Errorf("v1 %s hands rt %s no body run by %s", owner, funcKey(f), want)
				}
			case *ast.SelectorExpr:
				f, ok := v1.info.Uses[n.Sel].(*types.Func)
				if !ok {
					return true
				}
				key := funcKey(f)
				switch {
				case isRT(f) && key == "Cause.Error":
					t.Errorf("v1 %s uses rt's (*Cause).Error, a defect formatter outside report", owner)
				case isRT(f) && key == "Cause.AddDefect" && owner != "capture":
					t.Errorf("v1 %s uses rt's (*Cause).AddDefect outside capture", owner)
				case f.Pkg() != nil && f.Pkg().Path() == "runtime/debug" && f.Name() == "Stack" && owner != "defectOf":
					t.Errorf("v1 %s takes debug.Stack outside defectOf", owner)
				case isRT(f) && recovering[key] && !isCallee(decl, n):
					t.Errorf("v1 %s takes rt's recovery API %s as a value", owner, key)
				}
			}
			return true
		})
		recoverFlows(t, v1, owner, decl)
	})
	for edge := range allowed {
		if !seen[edge] {
			t.Errorf("v1 no longer takes the edge %s; the scan no longer sees the owner", edge)
		}
	}
}

// recoverFlows checks that each recover in decl hands its value to capture
// in the same function: directly, or through the variable it is assigned.
func recoverFlows(t *testing.T, src *typedSource, owner string, decl ast.Decl) {
	t.Helper()
	isCall := func(e ast.Expr, name string) bool {
		c, ok := ast.Unparen(e).(*ast.CallExpr)
		if !ok {
			return false
		}
		id, ok := ast.Unparen(c.Fun).(*ast.Ident)
		if !ok {
			return false
		}
		switch o := src.info.Uses[id].(type) {
		case *types.Builtin:
			return o.Name() == name
		case *types.Func:
			return o.Pkg() == src.pkg && o.Name() == name
		}
		return false
	}
	// fn visits one function body; a literal in it is a function of its own.
	var fn func(body ast.Node)
	fn = func(body ast.Node) {
		var vars []types.Object // variables assigned a recover's value
		direct, recovers := 0, 0
		captured := map[types.Object]bool{}
		ast.Inspect(body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.FuncLit:
				if n != body {
					fn(n.Body)
					return false
				}
			case *ast.AssignStmt:
				for i, r := range n.Rhs {
					if isCall(r, "recover") && i < len(n.Lhs) {
						if id, ok := n.Lhs[i].(*ast.Ident); ok {
							o := src.info.Defs[id]
							if o == nil {
								o = src.info.Uses[id]
							}
							vars = append(vars, o)
						}
					}
				}
			case *ast.CallExpr:
				if isCall(n, "recover") {
					recovers++
				}
				if isCall(n, "capture") {
					for _, a := range n.Args {
						if isCall(a, "recover") {
							direct++
						}
						if id, ok := ast.Unparen(a).(*ast.Ident); ok {
							captured[src.info.Uses[id]] = true
						}
					}
				}
			}
			return true
		})
		flows := direct
		for _, v := range vars {
			if v != nil && captured[v] {
				flows++
			}
		}
		if recovers != flows {
			t.Errorf("v1 %s: %d recovers, %d of them handed to capture", owner, recovers, flows)
		}
	}
	if d, ok := decl.(*ast.FuncDecl); ok && d.Body != nil {
		fn(d.Body)
	}
}

// funcKey names f as declOwner names its declaration: "Recv.Method" or the
// function's name.
func funcKey(f *types.Func) string {
	f = f.Origin()
	sig, _ := f.Type().(*types.Signature)
	if sig == nil || sig.Recv() == nil {
		return f.Name()
	}
	rt := sig.Recv().Type()
	if p, ok := rt.(*types.Pointer); ok {
		rt = p.Elem()
	}
	if n, ok := rt.(*types.Named); ok {
		return n.Obj().Name() + "." + f.Name()
	}
	return f.Name()
}

// calledFunc is the function a call's Fun names, through an instantiation
// or a selector, or nil.
func calledFunc(src *typedSource, fun ast.Expr) *types.Func {
	fun = ast.Unparen(fun)
	switch x := fun.(type) {
	case *ast.IndexExpr:
		fun = x.X
	case *ast.IndexListExpr:
		fun = x.X
	}
	var id *ast.Ident
	switch x := fun.(type) {
	case *ast.Ident:
		id = x
	case *ast.SelectorExpr:
		id = x.Sel
	default:
		return nil
	}
	f, _ := src.info.Uses[id].(*types.Func)
	return f
}

// isCallee reports whether sel is the Fun of a call in decl.
func isCallee(decl ast.Decl, sel *ast.SelectorExpr) bool {
	found := false
	ast.Inspect(decl, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			fun := ast.Unparen(c.Fun)
			if ix, ok := fun.(*ast.IndexExpr); ok {
				fun = ix.X
			}
			if fun == ast.Expr(sel) {
				found = true
			}
		}
		return !found
	})
	return found
}
