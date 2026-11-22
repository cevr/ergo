package lint

import (
	"go/constant"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The vet rules (VET-CHECK r2; owner q253, "fold into check + lint"):
// source that is valid and means something, but is almost never what was
// meant, as Go leaves it to go vet and staticcheck. Each protects no
// guarantee (the checker refuses what would break one: a negative constant
// against an unsigned value overflows, S108), so each is a lint finding,
// never an error: generated Ergo, and code a generator writes on purpose,
// must check (NORTH_STAR tiebreak, composability). They are in the strict
// preset, so a lint run reports them only when asked, and they skip a file
// whose header says it is generated: a whole line matching Go's
// convention, ^// Code generated .* DO NOT EDIT\.$, before the first
// declaration (generatedSource), which `ergo bind go` and `ergo gen` put
// first. A program evalgen writes is never linted at all: its harness
// checks and runs it, and lint is a separate, asked-for run.

// selfAssignment is go vet's assign: `x = x` stores a place's own value.
var selfAssignment = &Rule{
	Name:    "self-assignment",
	Code:    "L159",
	Doc:     "An assignment that stores a place's own value back (x = x, p.X = p.X, xs[0] = xs[0]) does nothing.",
	Instead: "delete it; an arm with nothing to do has no statements (case X:)",
	Default: Warning,
	Check:   checkSelfAssignment,
}

// signDecided is staticcheck SA4003 and SA4024: a value that is never
// negative compared with a constant that its sign alone decides.
var signDecided = &Rule{
	Name:    "sign-decided-comparison",
	Code:    "L160",
	Doc:     "A comparison of a value that is never negative (an unsigned integer, or len(…)) with a constant its sign decides (u >= 0, len(xs) < 0, 0 > u) is always true or always false.",
	Instead: "compare with a value it can take, or delete the comparison",
	Default: Warning,
	Check:   checkSignDecided,
}

// wireKeyQuote is go vet's structtag habit: a field key written as a Go
// struct tag.
var wireKeyQuote = &Rule{
	Name:    "wire-key-quote",
	Code:    "L161",
	Doc:     "A field key that holds a double quote is the Go struct-tag habit (\"json:\\\"name\\\"\"): a field's string is its wire key itself, for every format, so the quotes go on the wire.",
	Instead: "write the key alone: \"name\"",
	Default: Warning,
	Check:   checkWireKeyQuote,
}

// generatedLine is Go's generated-file convention (go help generate),
// matched against a whole line.
var generatedLine = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`) })

// generatedSource reports a source with a line that matches Go's
// generated-file convention before its first declaration: among the
// comment and blank lines that lead the file. Any other comment, one
// that only holds the words, or the line after code, is not the mark.
func generatedSource(src string) bool {
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if generatedLine().MatchString(line) {
			return true
		}
		if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "//") {
			return false
		}
	}
	return false
}

func checkSelfAssignment(p *Pass) {
	if generatedSource(p.File.Src) {
		return
	}
	syntax.Inspect(p.File, func(n syntax.Node) bool {
		s, ok := n.(*syntax.AssignStmt)
		if !ok || s.Tok != syntax.ASSIGN || len(s.Lhs) != 1 || len(s.Rhs) != 1 || !sameStorage(s.Lhs[0], s.Rhs[0]) {
			return true
		}
		text := storageText(s.Lhs[0])
		p.Report(s.Pos(), s.End(), text+" = "+text+" assigns "+text+" its own value, so it does nothing")
		return true
	})
}

// sameStorage reports whether a and b name the same storage with no work
// between: one local, a field of it, or an element at one name or literal
// index. No shadowing (196v rule 2) makes a name one variable in a
// function, so equal spellings are one place. A call, a slice or any other
// form is never the same storage, since it may compute a new place.
func sameStorage(a, b syntax.Expr) bool {
	a, b = unparenExpr(a), unparenExpr(b)
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
		if lx, ok := unparenExpr(x.Indices[0]).(*syntax.BasicLit); ok {
			ly, ok := unparenExpr(y.Indices[0]).(*syntax.BasicLit)
			return ok && lx.Kind == ly.Kind && lx.Value == ly.Value
		}
		return sameStorage(x.Indices[0], y.Indices[0])
	}
	return false
}

func unparenExpr(x syntax.Expr) syntax.Expr {
	for {
		p, ok := x.(*syntax.ParenExpr)
		if !ok {
			return x
		}
		x = p.X
	}
}

// storageText spells a place sameStorage accepts.
func storageText(x syntax.Expr) string {
	switch x := unparenExpr(x).(type) {
	case *syntax.Ident:
		return x.Name
	case *syntax.SelectorExpr:
		return storageText(x.X) + "." + x.Sel.Name
	case *syntax.IndexExpr:
		if lit, ok := unparenExpr(x.Indices[0]).(*syntax.BasicLit); ok {
			return storageText(x.X) + "[" + lit.Value + "]"
		}
		return storageText(x.X) + "[" + storageText(x.Indices[0]) + "]"
	}
	return "it"
}

func checkSignDecided(p *Pass) {
	if generatedSource(p.File.Src) {
		return
	}
	syntax.Inspect(p.File, func(n syntax.Node) bool {
		x, ok := n.(*syntax.BinaryExpr)
		if !ok {
			return true
		}
		if always, why, ok := decidedBySign(p.Info, x); ok {
			p.Report(x.Pos(), x.End(), "this comparison is always "+strconv.FormatBool(always)+": "+why)
		}
		return true
	})
}

// decidedBySign is the verdict of a comparison of a never-negative value
// with an integer constant that the sign alone decides, and why the value
// is never negative; ok is false for any other expression.
func decidedBySign(info *check.Info, x *syntax.BinaryExpr) (always bool, why string, ok bool) {
	v, k, op := x.X, x.Y, x.Op
	if _, isConst := info.Values[unparenExpr(v)]; isConst {
		v, k, op = x.Y, x.X, mirrorOp(x.Op)
	}
	if _, vConst := info.Values[unparenExpr(v)]; vConst {
		return false, "", false
	}
	kv, kConst := info.Values[unparenExpr(k)]
	if !kConst || kv.Kind() != constant.Int {
		return false, "", false
	}
	why = nonNegative(info, v)
	if why == "" {
		return false, "", false
	}
	s := constant.Sign(kv)
	var decided bool
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
	return always, why, decided
}

// nonNegative names why e's value is never negative, or "": its type is an
// unsigned integer, or it is a call of len. A conversion to a signed type
// of an unsigned value (int(u)) is a signed value: it may be negative, so
// it is not; the expression's own final type decides, never an operand's.
func nonNegative(info *check.Info, e syntax.Expr) string {
	e = unparenExpr(e)
	if b, ok := underOf(info.Types[e]).(*check.Basic); ok && b.Kind >= check.Uint && b.Kind <= check.Uintptr {
		return "a value of type " + b.Name + " is never negative"
	}
	if call, ok := e.(*syntax.CallExpr); ok {
		if c := info.Calls[call]; c != nil && c.Kind == check.CallLen {
			return "a length is never negative"
		}
	}
	return ""
}

// mirrorOp is the comparison with its operands swapped: 0 > u is u < 0.
func mirrorOp(op syntax.Token) syntax.Token {
	switch op {
	case syntax.LSS:
		return syntax.GTR
	case syntax.GTR:
		return syntax.LSS
	case syntax.LEQ:
		return syntax.GEQ
	case syntax.GEQ:
		return syntax.LEQ
	}
	return op
}

func checkWireKeyQuote(p *Pass) {
	if generatedSource(p.File.Src) {
		return
	}
	syntax.Inspect(p.File, func(n syntax.Node) bool {
		f, ok := n.(*syntax.Field)
		if !ok || f.Key == nil {
			return true
		}
		key, err := strconv.Unquote(f.Key.Value)
		if err != nil {
			return true
		}
		name, habit := goTagKey(key)
		if !habit {
			return true
		}
		fix := "write the key alone, with no quote in it"
		if name != "" {
			fix = "write " + strconv.Quote(name)
		}
		p.Report(f.Key.Pos(), f.Key.End(), "the key "+strconv.Quote(key)+" holds a quote, as a Go struct tag does; a field's string is its wire key itself, for every format: "+fix+" (types.md, Codecs 2)")
		return true
	})
}

// goTagKey reports a field key that holds a double quote, and the name a
// Go struct tag in it names, `json:"name,omitempty"` → name, else the key
// without its quotes. A colon alone is no sign (`"xml:lang"` is a real
// key), so a key with no quote passes.
func goTagKey(key string) (name string, ok bool) {
	if !strings.Contains(key, "\"") {
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
