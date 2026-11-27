package syntax

// A seeded generator of Ergo programs for the decoder property
// (TestDecodeAgreesWithView) and the round trip (TestFormatParseRoundTrip). It writes declarations from the grammar's forms with
// random expressions, types, patterns and statements inside, and now and
// then a stray token, so both valid trees and diagnostics are reached.

import (
	"math/rand"
	"strings"
)

type progGen struct {
	r     *rand.Rand
	b     strings.Builder
	depth int
}

func (g *progGen) w(s ...string) {
	for _, x := range s {
		g.b.WriteString(x)
	}
}

func (g *progGen) pick(xs ...string) string { return xs[g.r.Intn(len(xs))] }

func (g *progGen) name() string {
	return g.pick("x", "y", "f", "Point", "Map", "any", "own", "pure", "where", "derive", "entry", "implements", "lent", "replace", "initial", "step", "_", "error", "when", "reaches", "given", "gives", "keeps")
}

// stray is now and then a token that does not belong.
func (g *progGen) stray() {
	if g.r.Intn(60) == 0 {
		g.w(g.pick(" ", "(", ")", "{", "}", ",", ":", "=", ":=", "...", "++", "|", "\n", "var ", "range ", ";", "<-", "go ", "type ", "case ", "default:", "map[", "chan ", "struct", "[", "]", "..", "try ", "catch ", "needs ", "fails "))
	}
}

func (g *progGen) typ() {
	g.stray()
	if g.depth > 4 {
		g.w(g.pick("int", "string", "T"))
		return
	}
	g.depth++
	defer func() { g.depth-- }()
	switch g.r.Intn(14) {
	case 0:
		g.w("[]")
		g.typ()
	case 1:
		g.w("[3]")
		g.typ()
	case 2:
		g.w("*")
		g.typ()
	case 3:
		g.w("Map[")
		g.typ()
		g.w(", ")
		g.typ()
		if g.r.Intn(3) == 0 {
			// The Hashing provider, Map[K, V, H] (CONSTRAINTS C5).
			g.w(", ")
			g.typ()
		}
		g.w("]")
	case 4:
		g.w("func(")
		g.params()
		g.w(")")
		if g.r.Intn(2) == 0 {
			g.w(" ")
			g.typ()
		}
		if g.r.Intn(4) == 0 {
			g.w(" fails E | F")
		}
		if g.r.Intn(4) == 0 {
			g.w(" needs io.Stdout | A")
		}
	case 5:
		g.w("(")
		g.typ()
		g.w(", ")
		g.typ()
		g.w(")")
	case 6:
		g.w("Option[")
		g.typ()
		g.w("]")
	case 7:
		g.w(g.pick("func()", "entry func(int)", "go.Foreign", "pkg.T", "Row[A | B]"))
	case 8:
		g.w("(")
		g.typ()
		g.w(")")
	default:
		g.w(g.pick("int", "string", "T", "bool", "byte", "error"))
	}
}

func (g *progGen) params() {
	n := g.r.Intn(4)
	named := g.r.Intn(3) != 0
	for i := range n {
		if i > 0 {
			g.w(", ")
		}
		g.stray()
		if named {
			g.w(g.pick("a", "b", "own", "x", "m"), " ")
			if g.r.Intn(6) == 0 {
				g.w(g.pick("pure ", "own "))
			}
		} else if g.r.Intn(6) == 0 {
			g.w("own ")
		}
		g.typ()
		if named && g.r.Intn(10) == 0 {
			g.w(" where it > 0")
		}
	}
}

func (g *progGen) expr() {
	g.stray()
	if g.depth > 5 {
		g.w(g.pick("x", "1", "\"s\"", "y", "`two\nlines`"))
		return
	}
	g.depth++
	defer func() { g.depth-- }()
	switch g.r.Intn(30) {
	case 0, 1:
		g.expr()
		g.w(" ", g.pick("+", "-", "*", "/", "==", "!=", "<", ">=", "&&", "||", "|", "&", "<<", "%", "&^", ".."), " ")
		g.expr()
	case 2:
		g.w(g.pick("-", "!", "^", "&", "*", "try "))
		g.expr()
	case 3:
		g.expr()
		g.w("(")
		for i := range g.r.Intn(3) {
			if i > 0 {
				g.w(", ")
			}
			g.expr()
		}
		g.w(")")
		if g.r.Intn(8) == 0 {
			g.w(" ")
			g.block()
		}
	case 4:
		g.expr()
		g.w(".", g.name())
	case 5:
		g.expr()
		g.w("[")
		g.expr()
		if g.r.Intn(3) == 0 {
			g.w(":")
			g.expr()
		}
		g.w("]")
	case 6:
		g.w(g.pick("Point", "pkg.T", "Map[string, int]", "[]int", "T[int]"), "{")
		for i := range g.r.Intn(3) {
			if i > 0 {
				g.w(", ")
			}
			if g.r.Intn(2) == 0 {
				g.w(g.name(), ": ")
			}
			g.expr()
		}
		g.w("}")
	case 7:
		g.w("(")
		g.expr()
		if g.r.Intn(2) == 0 {
			g.w(", ")
			g.expr()
		}
		g.w(")")
	case 8:
		g.w("func(")
		g.params()
		g.w(") ")
		g.block()
	case 9:
		g.w("if ")
		g.expr()
		g.w(" ")
		g.block()
		if g.r.Intn(2) == 0 {
			g.w(" else ")
			g.block()
		}
	case 10:
		g.w("match ")
		g.expr()
		g.w(" {\n")
		for range 1 + g.r.Intn(3) {
			g.w("case ")
			g.pattern()
			if g.r.Intn(3) == 0 {
				g.w(", ")
				g.pattern()
			}
			g.w(":\n")
			g.stmts()
		}
		g.w("}")
	case 11:
		g.expr()
		g.w(" catch {\ncase ", g.pick("E", "F, G", "_", "E as e"), ":\n")
		g.stmts()
		g.w("}")
	case 12:
		g.w("\"a \\(")
		g.expr()
		g.w(") b\"")
	case 13:
		g.w("with ")
		g.expr()
		g.w(" ")
		g.block()
	case 14:
		g.w("context ", g.pick("", "fails E ", "fails E needs C "), "{ A: x, B, C: y }")
	case 15:
		g.w(g.pick("1", "2.5", "500ms", "'c'", "`raw`", "`raw\nlines`", "\"s\"", "0x1F"))
	case 16:
		g.w("T{..b, F: 1}")
	default:
		g.w(g.pick("x", "y", "f", "go.Error", "pkg.F", "it", "true"))
	}
}

func (g *progGen) pattern() {
	g.stray()
	switch g.r.Intn(9) {
	case 0:
		g.w("(", g.pick("A | B", "x", "_"), ", ", g.pick("C", "1", "_"), ")")
	case 1:
		g.w("Point{X: ", g.pick("x", "1", "A | B"), ", Y: _}")
	case 2:
		g.w(g.pick("-1", "\"s\"", "2.5", "'c'"))
	case 3:
		g.w("Some[int]{Value: v} as s")
	case 4:
		g.w("(x)")
	default:
		g.w(g.pick("_", "x", "A", "pkg.B", "1", "None"))
	}
}

func (g *progGen) block() {
	g.w("{\n")
	g.stmts()
	g.w("}")
}

func (g *progGen) stmts() {
	if g.depth > 5 {
		return
	}
	g.depth++
	defer func() { g.depth-- }()
	for range g.r.Intn(4) {
		g.stray()
		switch g.r.Intn(12) {
		case 0:
			g.w("let ", g.pick("x", "mut y", "(a, b)", "a, b", "Point{X: x}", "x int"), " = ")
			g.expr()
		case 1:
			g.w("return")
			if g.r.Intn(2) == 0 {
				g.w(" ")
				g.expr()
			}
		case 2:
			g.w(g.pick("break", "continue"))
		case 3:
			g.w("for ", g.pick("", "x in xs ", "i, x in 0..n ", "x < 3 "))
			g.block()
		case 4:
			g.expr()
			g.w(" ", g.pick("=", "+=", "-=", "|="), " ")
			g.expr()
		case 5:
			switch g.r.Intn(6) {
			case 0:
				g.w("skip ", g.pick(`"no host"`, "`raw`", `"a \(x) b"`, `"日本語"`))
			case 1:
				g.w(g.pick("skip(x)", "let skip = 1", "skip.f", "skip\n\"s\"", "x = skip"))
			default:
				g.w("assert ")
				g.expr()
			}
		default:
			g.expr()
		}
		g.w(g.pick("\n", "\n", "; ", "\n\n"))
	}
}

func (g *progGen) decl() {
	g.stray()
	if g.r.Intn(4) == 0 {
		g.w(g.pick("// doc of the next declaration\n", "/* a block\n   comment */\n"))
	}
	switch g.r.Intn(16) {
	case 0:
		g.w("func ", g.pick("F", "G", "f[T ordered]", "h[A, B any, K comparable]"), "(")
		g.params()
		g.w(")")
		if g.r.Intn(2) == 0 {
			g.w(" ")
			g.typ()
		}
		if g.r.Intn(3) == 0 {
			g.w(" fails E")
		}
		if g.r.Intn(3) == 0 {
			g.w(" needs A | B")
		}
		if g.r.Intn(6) == 0 {
			g.w(" takes a")
		}
		if g.r.Intn(4) == 0 {
			g.w(" ", g.pick("given ok(x)", "gives it > 0", "given ok(x) gives it > 0", "keeps Live(x)", "keeps Live(*a, x) when x != r"))
		}
		g.w(" ")
		g.block()
	case 1:
		g.w("type T {\n")
		for range g.r.Intn(4) {
			g.w("\t", g.pick("a", "b, c", "own"), " ")
			g.typ()
			g.w(g.pick(",\n", ",\n", " \"key\",\n"))
		}
		g.w("}")
		if g.r.Intn(4) == 0 {
			g.w(" where len(it.a) >= 0")
		}
	case 2:
		g.w("type Op {\n")
		// The type-set member (CONSTRAINTS C1), before, among or after the
		// operations: `~T` and bare terms, one or more.
		set := g.r.Intn(5)
		n := g.r.Intn(4)
		for i := range n + 1 {
			if i == set {
				g.w("\t", g.pick("~int | ~int64", "int | string", "comparable", "~pkg.T | T | ~[]byte", "K", "~Map[K, V]"), ",\n")
			}
			if i < n {
				g.w("\t", g.pick("Get(k string) int", "Put(K, V)", "own Close()", "Send(m Msg) fails E needs A", "mut Bump()"), ",\n")
			}
		}
		if g.r.Intn(2) == 0 {
			g.w("\tlaw L(x int) { x == x },\n")
		}
		g.w("}")
	case 3:
		g.w("type U = ", g.pick("A | B", "| A{x int} | B", "Some[T]{Value T} | None", "A\n\t| B"))
	case 4:
		g.w("type P int where 1 <= it && it <= 9")
	case 5:
		g.w("type M machine[S, E] {\n\tinitial: x,\n\tstep: f,\n", g.pick("", "\tafter: g,\n"), g.pick("", "\tlaw R reaches Done,\n", "\tlaw N never reaches Bad,\n", "\tlaw P path [A, B],\n", "\tlaw I always ok,\n"), "}")
	case 6:
		g.w("let ", g.pick("x", "Ctx", "mut y"), " = ")
		if g.r.Intn(3) == 0 {
			g.w("context ", g.pick("provides A | B needs C", "fails E", "provides A fails E | F needs C", "fails E needs C", ""), " { A: x, B: y }")
		} else {
			g.expr()
		}
	case 7:
		g.w("test ", g.pick("T", "P(x int)"), " ")
		g.block()
	case 8:
		g.w("law L(x int) ")
		g.block()
	case 9:
		g.w("extern go ", g.pick("strings.ToUpper func ToUpper(s string) string", "math.Pi let Pi float64", "net/url.URL type URL {}", "len func Len[T](xs []T) int"))
	case 10:
		g.w("type D derive ", g.pick("Plan(1)", "Provider(Of[S](), Of[F](), Of[N]())"))
	case 11:
		g.w("type Q[T] { x T } implements A, B")
	case 12:
		// Where a type set and a field group meet (grammar.md,
		// Declarations 4), and the type sets the parser refuses.
		g.w(g.pick(
			"type S { X, Y (int64, int64) }",
			"type K { comparable, Label() string }",
			"type K { T, own Close() }",
			"type K { pkg.T | ~int, law L(x int) { x == x } }",
			"type R { x int, T }",
			"type R { x int, ~int }",
			"type Two { ~int, ~string }",
			"type V = A{~int} | B",
		))
	default:
		g.w("func main() ")
		g.block()
	}
	g.w(g.pick("\n", "\n\n", "; ", "\n"))
}

// generatedProgram is a seeded program from the generator.
func generatedProgram(seed int64) string {
	g := &progGen{r: rand.New(rand.NewSource(seed))}
	if g.r.Intn(3) == 0 {
		g.w("import ", g.pick("fs", "go strings", "a/b", "x a/b", "go x strings"), "\n")
	}
	for range 1 + g.r.Intn(4) {
		g.decl()
	}
	return g.b.String()
}
