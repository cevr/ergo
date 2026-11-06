package lower

import (
	"strings"

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
	for _, f := range l.canon.funcs(l.pkg) {
		if cs, ok := pureCandidate(l.pkg.Info, f); ok && !l.meetsHostProtocol(f) {
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
