package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// Pure functions (spec D5, the zero-overhead pure-call contract; lower-v1
// stage 5, D-S5-10). A pure function is emitted as an ordinary Go function
// with no environment parameter, and every direct call of it passes none:
// the benchmark's Fold(n int) int is Go's Fold(n int) int.
//
// A function with an Ergo body, of the program's package, of an imported
// user package or of std (strings.Cut), is pure when
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
//     parameter), is a built-in operation (len, a conversion), a call of a
//     pure std body (pureStdBody), or a call of a pure function of any
//     package.
//
// Each rule reads the facts of the function's own package (its Info). The
// last rule is a greatest fixed point over the call graph across packages
// (isPure), so plain, recursive and mutually recursive functions are pure
// together, and a function of another package is pure only when its own
// body is. A pure std
// body is a std extern whose runtime/v1 Go body v1.BodyOf reports Pure
// (stdlib.md, Pure bodies): it takes no hidden argument and no function,
// so its call (stdCall) is a plain Go call that reads no environment, and
// the checker's footprint already counts it as nothing. A call of any
// other extern (a user extern, a std body that is not pure, an intrinsic),
// a with, an operation and a function value is never a pure call. A
// library function (std Ergo source, such as strings.Cut, or an imported
// user package's) is emitted pure by the same rules. A function value of a pure function keeps the one Go shape
// of every function value: its closure takes an environment and discards
// it (funcValue). The body of a pure function binds no environment, so any
// lowering that would read one is an internal compiler error with its
// source site (envExpr), never an unbound name.

// isPure reports whether f is a pure function. l.pure records every
// function decided so far, true or false. An undecided f is decided with
// every undecided function its body calls, at any depth and in any
// package: a function of the program's package, of an imported user
// package or of a std package with an Ergo body (strings.Cut). Each is
// read with its own package's Info (Footprints, Calls and TaskResults are
// keyed by its package's nodes), so a callee of another package is pure
// only when its own body is. The set is the greatest fixed point over that
// call graph: a function stays a candidate until a callee of it is known
// impure, and that fact is passed to each caller in turn (a worklist), so
// a chain of packages of any length is decided. Imports form no cycle, so
// the graph of other packages' callees is the import graph, imports first.
func (l *lowerer) isPure(f *check.Func) bool {
	if l.pure == nil {
		l.pure = map[*check.Func]bool{}
	}
	if p, ok := l.pure[f]; ok {
		return p
	}
	// Collect the undecided closure of f in a fixed order: f first, then
	// each candidate's callees in body order. The order matters only to
	// meetsHostProtocol, which may name a record's Go type.
	var order []*check.Func
	callees := map[*check.Func][]*check.Func{}
	cand := map[*check.Func]bool{}
	seen := map[*check.Func]bool{f: true}
	for queue := []*check.Func{f}; len(queue) > 0; {
		g := queue[0]
		queue = queue[1:]
		order = append(order, g)
		if g.Pkg == nil || g.Pkg.Info == nil {
			continue
		}
		cs, ok := pureCandidate(g.Pkg.Info, g)
		if !ok || l.meetsHostProtocol(g) {
			continue
		}
		cand[g], callees[g] = true, cs
		for _, h := range cs {
			if _, done := l.pure[h]; !done && !seen[h] {
				seen[h] = true
				queue = append(queue, h)
			}
		}
	}
	// A function is impure when a callee is: a decided impure one, or one
	// of the closure that is no candidate or is found impure here.
	callers := map[*check.Func][]*check.Func{}
	var impure []*check.Func
	for _, g := range order {
		if !cand[g] {
			impure = append(impure, g)
			continue
		}
		for _, h := range callees[g] {
			callers[h] = append(callers[h], g)
			if p, done := l.pure[h]; done && !p {
				impure = append(impure, g)
			}
		}
	}
	for len(impure) > 0 {
		g := impure[0]
		impure = impure[1:]
		if _, done := l.pure[g]; done {
			continue // found impure already
		}
		l.pure[g] = false
		impure = append(impure, callers[g]...)
	}
	for _, g := range order {
		if _, done := l.pure[g]; !done {
			l.pure[g] = true
		}
	}
	return l.pure[f]
}

// pureCandidate reports whether f meets every rule of a pure function but
// the last's fixed point, with the Ergo-bodied functions its body calls,
// of any package; info is f's own package's Info.
func pureCandidate(info *check.Info, f *check.Func) ([]*check.Func, bool) {
	d := f.Decl
	if d == nil || d.Body == nil || d.Extern != nil || f.Name == "main" || isClose(f) {
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
			case c.Kind == check.CallFunc && !c.TaskResult && pureStdBody(c.Func):
			case c.Kind == check.CallFunc && c.Func != nil && c.Func.Decl != nil && c.Func.Decl.Body != nil && c.Func.Decl.Extern == nil && !c.TaskResult:
				callees = append(callees, c.Func)
			default:
				ok = false
			}
		}
		return ok
	})
	return callees, ok
}

// pureStdBody reports a std extern function whose runtime/v1 Go body
// v1.BodyOf reports Pure: the fact the checker's footprint reads
// (check/footprint.go, pureStd). A body BodyOf refuses is not pure here;
// its call stops at stdBody with BodyOf's error. No prelude intrinsic
// (Append, Get, Delete) is marked pure; if one were, its call would keep
// the environment and stop at envExpr with an internal compiler error.
func pureStdBody(f *check.Func) bool {
	if f == nil || !f.Pkg.Std() || f.Decl == nil || f.Decl.Extern == nil {
		return false
	}
	path, name := f.Decl.Extern.Split()
	if path != runtimeImport {
		return false
	}
	body, err := v1.BodyOf(name)
	return err == nil && body.Pure
}

// Host protocols (Q-S5-6). Go joins an interface by the method set, so the
// lowering decides from the Go method it emits, never from a source name:
//   - (a) a pure function that lowers to a Go method whose name and Go
//     signature meet a host protocol exactly keeps the environment
//     (meetsHostProtocol), so its type never joins the protocol by
//     accident, and so does each function that calls it;
//   - (b) a pure function that lowers to a Go method whose name is a host
//     protocol's but whose signature is not gets a Go name outside the
//     table (protocolFreeName), so go vet's stdmethods check and Go's fmt
//     and errors never read it;
//   - (c) every other function, a plain function named String included, is
//     pure by D-S5-10 alone.

// hostProtocols are the Go method protocols: error, fmt.Stringer and
// fmt.GoStringer (no parameter, a string result), and every canonical
// method of go vet's stdmethods check (Go 1.27; Flush is not in vet's
// table), each with the Go spellings of its parameters and results. A "="
// marks a parameter go vet keys on; an exact match ignores the mark.
var hostProtocols = map[string]struct{ args, results []string }{
	"Error":         {nil, []string{"string"}},
	"String":        {nil, []string{"string"}},
	"GoString":      {nil, []string{"string"}},
	"As":            {[]string{"any"}, []string{"bool"}},
	"Format":        {[]string{"=fmt.State", "rune"}, nil},
	"GobDecode":     {[]string{"[]byte"}, []string{"error"}},
	"GobEncode":     {nil, []string{"[]byte", "error"}},
	"Is":            {[]string{"error"}, []string{"bool"}},
	"MarshalJSON":   {nil, []string{"[]byte", "error"}},
	"MarshalXML":    {[]string{"*xml.Encoder", "xml.StartElement"}, []string{"error"}},
	"ReadByte":      {nil, []string{"byte", "error"}},
	"ReadFrom":      {[]string{"=io.Reader"}, []string{"int64", "error"}},
	"ReadRune":      {nil, []string{"rune", "int", "error"}},
	"Scan":          {[]string{"=fmt.ScanState", "rune"}, []string{"error"}},
	"Seek":          {[]string{"=int64", "int"}, []string{"int64", "error"}},
	"UnmarshalJSON": {[]string{"[]byte"}, []string{"error"}},
	"UnmarshalXML":  {[]string{"*xml.Decoder", "xml.StartElement"}, []string{"error"}},
	"UnreadByte":    {nil, []string{"error"}},
	"UnreadRune":    {nil, []string{"error"}},
	"Unwrap":        {nil, []string{"error"}},
	"WriteByte":     {[]string{"byte"}, []string{"error"}},
	"WriteTo":       {[]string{"=io.Writer"}, []string{"int64", "error"}},
}

// meetsHostProtocol reports whether f lowers to a Go method whose name and
// Go signature, without an environment, are a host protocol's exactly
// (case a). Only a function with a protocol's name is asked, so the
// receiver of any other function is not resolved here.
func (l *lowerer) meetsHostProtocol(f *check.Func) bool {
	p, ok := hostProtocols[memberName(f.Name)]
	if !ok {
		return false
	}
	if td, _ := l.methodReceiver(f); td == nil {
		return false
	}
	params := f.Sig.Params[1:]
	if len(params) != len(p.args) || len(f.Sig.Results) != len(p.results) {
		return false
	}
	for i, q := range params {
		if goBasicName(q.Type) != strings.TrimPrefix(p.args[i], "=") {
			return false
		}
	}
	for i, r := range f.Sig.Results {
		if goBasicName(r) != p.results[i] {
			return false
		}
	}
	return true
}

// goBasicName is the Go spelling of a basic type, as go vet prints it, or
// "" for any other type: no host protocol's type is another Ergo type's
// lowering (Ergo has no any, error or byte alias; byte is uint8).
func goBasicName(t check.Type) string {
	if b, ok := t.(*check.Basic); ok && b.Kind != check.Duration && b.Kind != check.Invalid {
		return b.Name
	}
	return ""
}

// protocolFreeName is the Go method name of a pure function f that lowers
// to a method of td under name (case b): name itself unless it is a host
// protocol's, else the name with an underscore appended until it is free
// among td's fields and the functions on td, as a field renamed for Error
// is (typeDecl).
func (l *lowerer) protocolFreeName(f *check.Func, td *typeDecl, name string) string {
	if _, ok := hostProtocols[name]; !ok {
		return name
	}
	taken := map[string]bool{}
	for _, goName := range td.fieldName {
		taken[goName] = true
	}
	for _, g := range l.canon.funcs(f.Pkg) {
		if g == f || len(g.Sig.Params) == 0 {
			continue
		}
		first := g.Sig.Params[0].Type
		if p, ok := first.(*check.Pointer); ok {
			first = p.Elem
		}
		if n, ok := first.(*check.Named); ok && (n.Obj == td.tn || n.Orig != nil && n.Orig.Obj == td.tn) {
			taken[memberName(g.Name)] = true
		}
	}
	name += "_"
	for taken[name] {
		name += "_"
	}
	return name
}

// envLead is the leading environment argument of a direct call of fd from
// code whose environment is env: none for a pure function.
func (fd *funcDecl) envLead(env string) []string {
	if fd.pure {
		return nil
	}
	return []string{env}
}
