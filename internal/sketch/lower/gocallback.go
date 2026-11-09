package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// goCallbacks adapts each callback argument of a Go call of import go,
// vals in sig's parameter order, to Go's own function type (IG6;
// docs/spec/interop.md, Callbacks), and reports whether one may fail: then
// the call's error may carry an Ergo failure through Go (v1.GoTunnel), and
// goUntunnel reads it.
//
// The binding's callback type decides Go's shape (whether Go's callback
// returns an error); the argument's type, whose parameters and result are
// the binding's after the call's type arguments, gives the Go types.
func (fc *funcCtx) goCallbacks(sig *check.Signature, args []syntax.Expr, vals []string, site *syntax.CallExpr) (tunnels bool) {
	for i, p := range sig.Params {
		if i >= len(vals) || i >= len(args) {
			break
		}
		cb, ok := p.Type.(*check.Signature)
		if !ok {
			continue
		}
		tunnels = tunnels || cb.Fallible
		vals[i] = fc.spill(fc.goCallback(fc.bindFunc(vals[i]), fc.argSig(args[i], site), cb.Fallible, site))
	}
	return tunnels
}

// goCallback adapts fv, an Ergo function value of the uniform shape
// (typeCtx.funcType), to the Go function type the binding's callback type
// cb stands for: Go's parameters and result (go.Error is Go's error), and
// an error last when fails, the binding's callback type has a fails row
// (gobind writes one exactly when Go's callback returns an error). The adapter runs fv in the environment
// of the code that makes the call, captured here, as hostCallback does: a
// synchronous callback runs on its caller's task, with the capabilities
// its needs row takes from the caller. Go's arguments become Ergo values
// (a Go error the go.Error that keeps it, a slice or a Map a copy Go no
// longer writes); fv's result becomes Go's (a go.Error its native error, a
// slice or a Map a copy); fv's failure becomes Go's error
// (v1.GoCallbackError: a go.Error Go's own error, so fs.SkipDir skips; any
// other failure tunnelled).
//
// bound are arguments fv takes after the environment and before Go's: the
// receiver of a Go interface's method (gointerface.go).
func (fc *funcCtx) goCallback(fv string, cb *check.Signature, fails bool, site syntax.Node, bound ...string) string {
	if len(cb.Results) > 1 {
		ice(site, "a Go callback with more than one result")
	}
	return fc.envClosure(capturedEnv, "e", site, func(env string) (params []string, results, body string) {
		ind := strings.Repeat("\t", fc.depth+1)
		args := append([]string{env}, bound...)
		for _, p := range cb.Params {
			name := fc.scope.alloc("p")
			params = append(params, name+" "+fc.goCallbackType(p.Type))
			args = append(args, fc.goCallbackIn(p.Type, name, site))
		}
		call := fv + "(" + strings.Join(args, ", ") + ")"
		var goResults []string
		r, e := "_", "_"
		if len(cb.Results) == 1 {
			goResults = append(goResults, fc.goCallbackType(cb.Results[0]))
			r = fc.scope.alloc("r")
		}
		if fails {
			goResults = append(goResults, "error")
			e = fc.scope.alloc("err")
		}
		switch len(goResults) {
		case 0:
			return params, "", ind + "_, _ = " + call + "\n"
		case 1:
			results = " " + goResults[0]
		default:
			results = " (" + strings.Join(goResults, ", ") + ")"
		}
		body = ind + r + ", " + e + " := " + call + "\n"
		var out []string
		if r != "_" {
			out = append(out, fc.goCallbackOut(cb.Results[0], r, site))
		}
		if e != "_" {
			out = append(out, fc.l.rt()+".GoCallbackError("+e+")")
		}
		return params, results, body + ind + "return " + strings.Join(out, ", ") + "\n"
	})
}

// goCallbackType is the Go type of a callback's parameter or result of the
// Ergo type t: Go's error for go.Error, else t's Go type.
func (fc *funcCtx) goCallbackType(t check.Type) string {
	if n, ok := t.(*check.Named); ok && check.IsGoError(n) {
		return "error"
	}
	return fc.goType(t)
}

// goCallbackIn is the Ergo value of the Go argument p of a callback.
func (fc *funcCtx) goCallbackIn(t check.Type, p string, site syntax.Node) string {
	if n, ok := t.(*check.Named); ok && check.IsGoError(n) {
		return fc.l.goErrorFrom() + "(" + p + ")"
	}
	if fc.l.holdsValue(t) && valueRule() {
		return fc.cloneValue(t, p, site)
	}
	return p
}

// goCallbackOut is the Go value of a callback's Ergo result r.
func (fc *funcCtx) goCallbackOut(t check.Type, r string, site syntax.Node) string {
	if n, ok := t.(*check.Named); ok && check.IsGoError(n) {
		return "(" + r + ").Native()"
	}
	if fc.l.holdsValue(t) && valueRule() {
		return fc.cloneValue(t, r, site)
	}
	return r
}

// goUntunnel writes the conversion of the error e of the Go call x that
// took a callback that may fail: the Ergo failure a callback tunnelled
// through Go, typed, else the go.Error (v1.GoUntunnel). A plain catch with
// its error unread tests e against nil alone (goErrorOf).
func (fc *funcCtx) goUntunnel(x syntax.Expr, e string) {
	if fc.rawErr != nil && fc.rawErr == x {
		fc.rawErr = nil
		return
	}
	f := ".GoUntunnel"
	if goErrorEager {
		f = ".GoUntunnelEager"
	}
	fc.line(e + " = " + fc.l.rt() + f + "(" + e + ")")
}
