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
// type the subset admits: names, []T, pkg.Name and a generic type's
// instance Name[T1, T2], where pkg is the name main.go imports a package
// by. The IR names that package by its import path (the decoder resolves
// the path among main.go's imports), and the lowering keeps the import
// used (keepImport): an IR body is not Go text, and Go refuses an unused
// import.
func (b *irBuilder) spelling(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && isSpellingName(s[j]) {
			j++
		}
		if j == i {
			switch {
			case strings.HasPrefix(s[i:], "[]"):
				out.WriteString("[]")
				i += 2
			case strings.HasPrefix(s[i:], ", "):
				out.WriteString(", ")
				i += 2
			case s[i] == '[' && i > 0 && isSpellingName(s[i-1]), s[i] == ']':
				out.WriteByte(s[i])
				i++
			default:
				irNo("Go type spelling %s", s)
			}
			continue
		}
		out.WriteString(b.qualifiedName(s, s[i:j]))
		i = j
	}
	return out.String()
}

func isSpellingName(c byte) bool {
	return c == '_' || c == '.' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// qualifiedName is the IR spelling of one type name of the Go type s: a
// name of main.go as it is, pkg.Name by pkg's import path.
func (b *irBuilder) qualifiedName(s, n string) string {
	q, name, ok := strings.Cut(n, ".")
	if !ok {
		return n
	}
	if strings.Contains(name, ".") || q == "" || name == "" {
		irNo("Go type spelling %s", s)
	}
	path := b.importPath(q)
	if path == "" {
		irNo("Go type spelling %s", s)
	}
	b.keepImport("var _ " + q + "." + name)
	return path + "." + name
}

// admitArgs admits the type arguments of t, an instance of a generic type
// of the program (Option[int], Pair[string, Row]): the type-kinded ones,
// which its Go type takes (typeArgs; a row argument has no Go type). The
// instance's fields and variants are its declaration's with the arguments
// put in, so they are admitted as any record's or union's.
func (b *irBuilder) admitArgs(t *check.Named, depth int) {
	for _, a := range typeArgs(t) {
		b.admit(a, depth+1)
	}
}

// typeArgs is the Go type argument list of the generic call c, comma and
// space between them, as the emitter's typeArgs writes it in brackets: a
// type argument of each type-kinded parameter (a row or an operations
// parameter has no Go type parameter). "" for a call of a function that
// is not generic.
func (b *irBuilder) typeArgs(c *check.Call, site syntax.Node) string {
	var out []string
	for i, tp := range c.Func.Sig.TParams {
		if tp.Kind != check.KindType {
			continue
		}
		if i >= len(c.TypeArgs) {
			irNo("generic call without its type arguments")
		}
		out = append(out, b.goType(c.TypeArgs[i], site))
	}
	return strings.Join(out, ", ")
}

// valueSource applies the value rule to the operands of the call x, args
// and their IR irArgs, as the emitter's valueSource does (values.go, V3):
// Append's source, unless the call may write it in place, is clipped
// (slices.Clip), so the append copies rather than write past its length
// into an array another value shares.
func (b *irBuilder) valueSource(x *syntax.CallExpr, c *check.Call, args []syntax.Expr, irArgs []*irExpr) {
	if !valueRule() || len(irArgs) == 0 || b.l.valueFacts().inPlace[x] || valueMutant == "in-place" {
		return
	}
	switch {
	case preludeAppend(c.Func):
		if len(c.TypeArgs) != 1 {
			irNo("append form")
		}
		elem := b.goType(c.TypeArgs[0], x)
		if b.goType(b.l.info.Types[args[0]], x) != "[]"+elem {
			// Go assigns a defined slice type to []T; the decoder takes
			// identical types only.
			irNo("append to a defined slice type")
		}
		b.keepImport("var _ = " + b.l.goPkg("slices") + ".Clip[[]" + b.fc.goType(c.TypeArgs[0]) + "]")
		src := irArgs[0]
		irArgs[0] = &irExpr{K: "pcall", Pos: src.Pos, T: "slices", Name: "Clip", V: "[]" + elem + ", " + elem, Args: []*irExpr{src}}
	case preludeMapWrite(c.Func):
		if len(c.TypeArgs) != 2 {
			irNo("Map write form")
		}
		k, v := b.goType(c.TypeArgs[0], x), b.goType(c.TypeArgs[1], x)
		m := "map[" + k + "]" + v
		if b.goType(b.l.info.Types[args[0]], x) != m {
			irNo("Map write to a defined Map type")
		}
		b.keepImport("var _ = " + b.l.goPkg("maps") + ".Clone[map[" + b.fc.goType(c.TypeArgs[0]) + "]" + b.fc.goType(c.TypeArgs[1]) + "]")
		src := irArgs[0]
		irArgs[0] = &irExpr{K: "pcall", Pos: src.Pos, T: "maps", Name: "Clone", V: m + ", " + k + ", " + v, Args: []*irExpr{src}}
	}
}

// admitMap admits a Map, Go's map[K]V: its key and its value. A Map is a
// value (V3): a call that writes one writes a copy unless it may write in
// place (valueSource), and a copy of a local is the value rule's ("value
// copy", which keeps the source body).
func (b *irBuilder) admitMap(t *check.Map, depth int) {
	b.admit(t.Key, depth+1)
	b.admit(t.Elem, depth+1)
}

// goRange reports a for-in domain the emitter lowers to Go's range over
// the value (rangeStmt): a slice, a Map, a string's runes.
func goRange(d check.LoopDomain) bool {
	return d == check.LoopSlice || d == check.LoopMap || d == check.LoopString
}

// own is the type whose Go type fu's helpers spell: the instance, for a
// generic union's copy.
func (fu *flatUnion) own() check.Type {
	if fu.inst != nil {
		return fu.inst
	}
	return fu.tn.Type
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
