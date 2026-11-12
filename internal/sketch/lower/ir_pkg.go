package lower

// IR builder: what lies outside the function's own package (IRG4). See
// ir.go.
//
//   - A function of another Ergo package of the program is a function of
//     the one Go package the program lowers to: a "call" by its Go name
//     (ir_call.go).
//   - A type of an imported Go package (time.Duration, the runtime's
//     ergo.Malformed) is spelled by the package's import path, which the
//     decoder resolves among main.go's imports (spelling).
//   - A record type that holds itself is admitted once (admitOnce), at any
//     depth.

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// admitOnce reports whether the record or union named by tn was admitted
// already in this body, and marks it admitted: a type that holds itself
// (through a slice, a union) is admitted once.
func (b *irBuilder) admitOnce(tn *check.TypeName) bool {
	if b.admitted[tn] {
		return true
	}
	if b.admitted == nil {
		b.admitted = map[*check.TypeName]bool{}
	}
	b.admitted[tn] = true
	return false
}

// spelling is the IR spelling of the Go type s, the lowered spelling of a
// type the subset admits: names, []T and pkg.Name, where pkg is the name
// main.go imports a package by. The IR names that package by its import
// path (the decoder resolves the path among main.go's imports), and the
// lowering keeps the import used (keepImport): an IR body is not Go text,
// and Go refuses an unused import.
func (b *irBuilder) spelling(s string) string {
	elem := strings.TrimLeft(s, "[]")
	for _, r := range elem {
		if !(r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			irNo("Go type spelling %s", s)
		}
	}
	q, name, ok := strings.Cut(elem, ".")
	if !ok {
		return s
	}
	if strings.Contains(name, ".") {
		irNo("Go type spelling %s", s)
	}
	path := b.importPath(q)
	if path == "" {
		irNo("Go type spelling %s", s)
	}
	b.keepImport("var _ " + q + "." + name)
	return s[:len(s)-len(elem)] + path + "." + name
}

// stdCall is the call x of the std extern fd: a call of its runtime/v1
// body by the runtime's import path ("pcall"), as the emitter's stdCall
// writes it when the body takes no hidden argument, no function and no
// type argument, and returns its Go form plainly. Every other shape keeps
// the source body.
//
// The builder declares nothing for the call: the lowering's funcDecl would
// declare a std record the function takes first as a Go type of main.go
// (methodReceiver), where the source path, which may inline the call,
// declares none; a std extern's call needs only its target.
func (b *irBuilder) stdCall(x *syntax.CallExpr, c *check.Call, f *check.Func, p irPos) ([]*irStmt, *irExpr, bool) {
	fd := b.l.funcs[f]
	if fd == nil {
		if f.Decl.Extern.Target == nil {
			irNo("call of a std intrinsic")
		}
		fd = &funcDecl{fn: f, library: true, extern: strings.Trim(f.Decl.Extern.Target.Value, "\"`")}
	}
	body, name, ok := b.l.stdBody(fd)
	switch {
	case !ok:
		irNo("call of a std intrinsic")
	case c.Recv != nil:
		irNo("dot call of a std function")
	case b.fc.refSiteOf(x) != nil:
		irNo("std call into an owning literal")
	case len(body.Hidden) > 0:
		irNo("std call with hidden arguments")
	case len(body.TypeParams) > 0:
		irNo("generic or task call")
	case defersCall(body) || commaOk(body):
		irNo("std call with an adapted result")
	case len(body.Params) != len(x.Args):
		irNo("std call with another arity")
	case len(b.l.ownedParams(fd.fn)) > 0:
		irNo("call that passes owned values")
	case c.Site != nil:
		irNo("call with a call site")
	case len(b.fc.captureOwners(x)) > 0:
		irNo("call that captures owners")
	}
	if _, bound := runtimeBindings[name]; !bound {
		irNo("std call with no runtime binding")
	}
	if _, tuple := b.l.info.Types[x].(*check.Tuple); tuple {
		irNo("std call with an adapted result")
	}
	// The decoder takes an argument and a result of the runtime's Go type
	// itself (types2.Identical), where Go text would also assign a
	// defined type to its underlying one: each must be spelled alike.
	for i, q := range body.Params {
		if q.Func != nil || q.Funcs != nil {
			irNo("std call that adapts a function")
		}
		if !b.sameGo(b.l.info.Types[x.Args[i]], q.Type) {
			irNo("std call with a converted operand")
		}
	}
	if goFallible(body) != failsHere(c) {
		irNo("fallible call with an empty row at its site")
	}
	if goValue(body) && !b.sameGo(b.l.info.Types[x], body.Results[0]) {
		irNo("std call with a converted result")
	}
	pre, args := b.operands(x.Args)
	b.keepImport("var _ = " + b.l.rt() + "." + name)
	return pre, &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: name, Args: args}, !body.Pure
}

// sameGo reports whether the Ergo type t lowers to the Go type a
// runtime/v1 body spells goType, a runtime type unqualified.
func (b *irBuilder) sameGo(t check.Type, goType string) bool {
	if t == nil {
		return false
	}
	s := b.fc.goType(t)
	return s == goType || s == b.l.rt()+"."+goType
}

// importPath is the import path of the package main.go imports by the
// name q, or "".
func (b *irBuilder) importPath(q string) string {
	for k, n := range b.l.pkgNames {
		if path, ok := k.obj.(string); ok && k.kind == "import" && n == q {
			return path
		}
	}
	return ""
}
