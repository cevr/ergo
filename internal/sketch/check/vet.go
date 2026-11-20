package check

import (
	"go/constant"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The vet class (VET-CHECK): source that builds and is always wrong, which
// Go leaves to go vet and staticcheck. Each rule here is decided by facts
// the checker has, so it is an error, not a lint finding (findings.md of
// VET-GAP; diagnostics.md).

// sameStorage reports whether a and b name the same storage with no work
// between: one local, a field of it, or an element at one name or literal
// index. No shadowing (196v rule 2) makes a name one variable in a
// function, so equal spellings are one place. A call, a slice or any other
// form is never the same storage, since it may compute a new place.
func sameStorage(a, b syntax.Expr) bool {
	a, b = unparen(a), unparen(b)
	switch x := a.(type) {
	case *syntax.Ident:
		y, ok := b.(*syntax.Ident)
		return ok && x.Name == y.Name && x.Name != "_"
	case *syntax.SelectorExpr:
		y, ok := b.(*syntax.SelectorExpr)
		return ok && x.Sel.Name == y.Sel.Name && sameStorage(x.X, y.X)
	case *syntax.IndexExpr:
		y, ok := b.(*syntax.IndexExpr)
		if !ok || len(x.Indices) != 1 || len(y.Indices) != 1 || !sameStorage(x.X, y.X) {
			return false
		}
		if lx, ok := unparen(x.Indices[0]).(*syntax.BasicLit); ok {
			ly, ok := unparen(y.Indices[0]).(*syntax.BasicLit)
			return ok && lx.Kind == ly.Kind && lx.Value == ly.Value
		}
		return sameStorage(x.Indices[0], y.Indices[0])
	}
	return false
}

// selfAssignment refuses `x = x` (S110; go vet's assign): it stores a
// place's own value back, so it does nothing. An arm with nothing to do is
// written with no statements.
func (c *checker) selfAssignment(s *syntax.AssignStmt) bool {
	if s.Tok != syntax.ASSIGN || !sameStorage(s.Lhs[0], s.Rhs[0]) {
		return false
	}
	text := storageText(s.Lhs[0])
	c.errorf(s, CodeUnused, "%s = %s assigns %s its own value, so it does nothing; delete it (an arm with nothing to do has no statements: case X:)", text, text, text)
	return true
}

// storageText spells a place sameStorage accepts: a name, a field or an
// element at a name or a literal.
func storageText(x syntax.Expr) string {
	switch x := unparen(x).(type) {
	case *syntax.SelectorExpr:
		return storageText(x.X) + "." + x.Sel.Name
	case *syntax.IndexExpr:
		if lit, ok := unparen(x.Indices[0]).(*syntax.BasicLit); ok {
			return storageText(x.X) + "[" + lit.Value + "]"
		}
		return storageText(x.X) + "[" + storageText(x.Indices[0]) + "]"
	}
	return exprText(x)
}

// nonNegative names why e's value is never negative, or "": an unsigned
// integer, or a call of len (a prelude name, so never another function,
// grammar Reserved names 2).
func nonNegative(e syntax.Expr, t Type) string {
	if isUnsigned(t) {
		return "a value of type " + t.String() + " is never negative"
	}
	if call, ok := unparen(e).(*syntax.CallExpr); ok {
		if id, ok := unparen(call.Fun).(*syntax.Ident); ok && id.Name == "len" {
			return "a length is never negative"
		}
	}
	return ""
}

// decidedBySign refuses a comparison of a never-negative value with a
// constant that the sign alone decides: u >= 0, len(xs) < 0, 0 > u, n == -1
// for a length (S108; staticcheck SA4003, SA4024). It reports whether it did.
func (c *checker) decidedBySign(x *syntax.BinaryExpr, l, r operand) bool {
	v, vop, k, op := x.X, l, r, x.Op
	if l.mode == modeConst {
		v, vop, k, op = x.Y, r, l, mirror(x.Op)
	}
	if vop.mode == modeConst || k.mode != modeConst || k.val.Kind() != constant.Int {
		return false
	}
	why := nonNegative(v, vop.typ)
	if why == "" {
		return false
	}
	s := constant.Sign(k.val)
	var always, decided bool
	switch op {
	case syntax.GEQ:
		always, decided = true, s <= 0
	case syntax.GTR, syntax.NEQ:
		always, decided = true, s < 0
	case syntax.LSS:
		always, decided = false, s <= 0
	case syntax.LEQ, syntax.EQL:
		always, decided = false, s < 0
	}
	if !decided {
		return false
	}
	c.errorf(x, CodeType, "this comparison is always %t: %s; compare with a value it can take, or delete the comparison", always, why)
	return true
}

// goTagKey reports a field key that holds a double quote, and the name a
// Go struct tag in it names, `json:"name,omitempty"` → name, else the key
// without its quotes. A field's string is its wire key itself, shared by
// every format (types.md, Codecs 2), and no format's key is meant with a
// quote in it: it is the Go tag habit, which json.Encode would write as
// the key, `{"json:\"name\"": …}`. A colon alone is no sign (`"xml:lang"`
// is a real key), so a key with no quote passes.
func goTagKey(key string) (name string, ok bool) {
	if !strings.ContainsAny(key, "\"") {
		return "", false
	}
	if i := strings.Index(key, ":\""); i > 0 {
		name = key[i+2:]
		if j := strings.IndexAny(name, ",\""); j >= 0 {
			name = name[:j]
		}
		return name, true
	}
	return strings.Trim(key, "\" "), true
}

// wireKeyHabit refuses a field key that holds a double quote (S108; go vet
// structtag). It reports whether it did.
func (c *checker) wireKeyHabit(field *syntax.Field, key string) bool {
	name, ok := goTagKey(key)
	if !ok {
		return false
	}
	fix := "write the key alone, with no quote in it"
	if name != "" {
		fix = "write " + strconv.Quote(name)
	}
	c.errorf(field.Key, CodeType, "the key %s holds a quote, as a Go struct tag does; a field's string is its wire key itself, for every format: %s (types.md, Codecs 2)", strconv.Quote(key), fix)
	return true
}
