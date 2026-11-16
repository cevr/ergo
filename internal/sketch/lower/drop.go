package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The variant with no result (OPT5, a form of formLowering). A statement
// that discards a call's value (`let _ = c.Put(k, v)`, or a call as a
// statement) still paid for the value: the callee built it and returned
// it through memory. lru's Put built an Option of the evicted entry on
// every eviction, which its bench loop dropped (attribution, wf10 OPT5
// round 2: 8 to 17 instructions per op).
//
// Such a call goes to the callee's variant with no result: the same body
// lowered once more where the frame has no result, so each return
// evaluates its value for its effects alone (retExpr, as in a function
// with no result) and returns nothing. Go then drops the stores no one
// reads. The body is the callee's, so its effects, failures, defects and
// polls are the same, in the same order; only the value no one reads is
// not returned.
//
// The callee qualifies when it is an Ergo body (callText has already sent
// an extern, a builtin and a std Go body elsewhere) and has one result.
// An owned parameter (V5) changes nothing: a direct call of such a
// function is an owned call, whose caller passes the argument unshared,
// so the variant, like the body the call names otherwise, copies none
// (TestFormsAgree's tally). A fallible callee keeps its error
// result: the variant returns the error alone, as a function with no
// result does, and the statement's site handles it as before. No Task
// result reaches here: a discarded Task is refused (S108). A Close's
// variant is masked as the Close is (closeBody). The reference lowering
// (formLowering "reference") calls the function itself.
//
// Prior art: GHC's worker/wrapper split and OCaml's flambda specialise a
// function on what its caller uses of its result (unboxing, dead-result
// elimination once inlined); MLton drops an unused result across its whole
// program (useless-variable elimination). Gleam, Elixir and ReScript
// return the value and leave its drop to the host (the BEAM, a JavaScript
// engine), which drops it only when it inlines the call.

// droppedKey is the package-scope key of a function's variant with no
// result.
func droppedKey(g *check.Func) pkgKey { return pkgKey{"dropped", g} }

// reserveDropped gives, in the naming pass, the name of the variant with
// no result of each package-scope function of pkgs that may have one (an
// Ergo body with one result); a method's variant is a method name
// (droppedName).
func (l *lowerer) reserveDropped(pkgs []*check.Package) {
	if !formsOn() {
		return
	}
	for _, p := range pkgs {
		for _, f := range l.canon.funcs(p) {
			if len(f.Sig.Results) != 1 || f.Decl == nil || f.Decl.Body == nil || f.Decl.Extern != nil {
				continue
			}
			if td, _ := l.methodReceiver(f); td == nil {
				l.pkgName(droppedKey(f), f.Name+"Dropped")
			}
		}
	}
}

// droppedSite reports whether the call x of c.Func, of declaration fd, is
// a statement whose value is discarded and the callee has a variant with
// no result.
func (fc *funcCtx) droppedSite(x *syntax.CallExpr, c *check.Call, fd *funcDecl) bool {
	if !formsOn() || fc.stmtCall != x {
		return false // a value someone reads
	}
	f := c.Func
	if len(f.Sig.Results) != 1 || fd.extern != "" || f.Decl == nil || f.Decl.Body == nil {
		return false // no value, or no Ergo body to lower again
	}
	if len(fc.l.ownedParams(f)) > 0 && (fc.l.vals == nil || !fc.l.vals.ownCall[x]) {
		// Every direct call of a function with an owned parameter is an
		// owned call (values.go, decide): the caller passes each owned
		// argument unshared, and the variant, a body, copies none.
		ice(x, "a call of %s, which has an owned parameter, that is not an owned call", f.Name)
	}
	fc.l.stats.Dropped++
	return true
}

// droppedName is the Go name of g's variant with no result, queued for
// emission once. A Go method's variant is a method of the same receiver,
// under a name no field or other method of the receiver has.
func (l *lowerer) droppedName(g *check.Func, gd *funcDecl) string {
	if gd.dropped != "" {
		return gd.dropped
	}
	if gd.method != nil {
		taken := map[string]bool{}
		for _, n := range gd.method.fieldName {
			taken[n] = true
		}
		for _, f := range l.methodsOf(gd.method.tn) {
			fd := l.funcDecl(f)
			taken[fd.name] = true
			taken[fd.known] = true
		}
		name := gd.name + "Dropped"
		for taken[name] {
			name += "_"
		}
		gd.dropped = name
	} else {
		gd.dropped = l.pkgName(droppedKey(g), g.Name+"Dropped")
	}
	l.pending = append(l.pending, func() { l.emitBody(gd, false, nil, true) })
	return gd.dropped
}
