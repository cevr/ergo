package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Pure functions (spec D5, the zero-overhead pure-call contract; lower-v1
// stage 5, D-S5-10). A pure function is emitted as an ordinary Go function
// with no environment parameter, and every direct call of it passes none:
// the benchmark's Fold(n int) int is Go's Fold(n int) int.
//
// A function of the program's package is pure when
//   - it has an Ergo body (no extern target), it is not the program's main
//     (the generated Go main runs it in the root task's environment), and
//     it is not a checked Close (a release masks its own body in its task,
//     release.go);
//   - its checked signature has no error row, no capability row, no row
//     or witnessed type parameter, and no Task in its result;
//   - its frame's footprint is empty (Info.Footprints: no capability, no
//     task start, no opaque call), so it has no interruption point
//     (runtime.md, I1); and
//   - every call in its body, outside the bodies of function literals
//     (each a frame of its own that takes its caller's environment as its
//     parameter), is a built-in operation (len, a conversion) or a call of
//     a pure function.
//
// The last rule is a greatest fixed point over the call graph, so plain,
// recursive and mutually recursive functions are pure together. A call of
// a std or extern function, a with, an operation and a function value are
// never pure calls. A library function (std Ergo source) keeps its
// environment. A function value of a pure function keeps the one Go shape
// of every function value: its closure takes an environment and discards
// it (funcValue). The body of a pure function binds no environment, so any
// lowering that would read one is an internal compiler error with its
// source site (envExpr), never an unbound name.

// pureFuncs is the set of pure functions of the program's package,
// computed once from the package's own Info (a library body swaps l.info
// while it is emitted).
func (l *lowerer) pureFuncs() map[*check.Func]bool {
	if l.pure != nil {
		return l.pure
	}
	pure := map[*check.Func]bool{}
	callees := map[*check.Func][]*check.Func{}
	for _, f := range l.pkg.Funcs {
		if cs, ok := pureCandidate(l.pkg.Info, f); ok {
			pure[f], callees[f] = true, cs
		}
	}
	for changed := true; changed; {
		changed = false
		for f := range pure {
			for _, g := range callees[f] {
				if !pure[g] {
					delete(pure, f)
					changed = true
					break
				}
			}
		}
	}
	l.pure = pure
	return pure
}

// pureCandidate reports whether f meets every rule of a pure function but
// the last's fixed point, with the package functions its body calls.
func pureCandidate(info *check.Info, f *check.Func) ([]*check.Func, bool) {
	d := f.Decl
	if d == nil || d.Body == nil || d.Extern != nil || f.Name == "main" || isClose(f) || hostProtocolNames[f.Name] {
		return nil, false
	}
	s := f.Sig
	if len(rowElems(s.Errors)) > 0 || len(rowElems(s.Uses)) > 0 || s.ResultIsTask() || info.TaskResults[f] {
		return nil, false
	}
	for _, tp := range s.TParams {
		if tp.Kind != check.KindType {
			return nil, false
		}
	}
	if len(witnessParams(s)) > 0 {
		return nil, false
	}
	if fp, ok := info.Footprints[d]; !ok || !fp.Empty() || fp.EntryPoll {
		return nil, false
	}
	var callees []*check.Func
	ok := true
	syntax.Inspect(d.Body, func(n syntax.Node) bool {
		if !ok {
			return false
		}
		switch n := n.(type) {
		case *syntax.FuncLit:
			return false // a frame of its own, with its caller's environment
		case *syntax.CallExpr:
			c := info.Calls[n]
			switch {
			case c == nil:
				ok = false
			case c.Kind == check.CallLen || c.Kind == check.CallConversion:
			case c.Kind == check.CallFunc && c.Func != nil && c.Func.Pkg == f.Pkg && c.Func.Decl != nil && c.Func.Decl.Extern == nil && !c.TaskResult:
				callees = append(callees, c.Func)
			default:
				ok = false
			}
		}
		return ok
	})
	return callees, ok
}

// hostProtocolNames are the method names Go's fmt and errors read from a
// method set with no parameter and a string result (error, fmt.Stringer,
// fmt.GoStringer), and that go vet's unusedresult check reads at a call. A
// function with such a name may lower as a Go method; without its
// environment parameter it would join a host protocol by accident, so it
// keeps the environment, and so does each function that calls it.
var hostProtocolNames = map[string]bool{"Error": true, "String": true, "GoString": true}

// envLead is the leading environment argument of a direct call of fd from
// code whose environment is env: none for a pure function.
func (fd *funcDecl) envLead(env string) []string {
	if fd.pure {
		return nil
	}
	return []string{env}
}
