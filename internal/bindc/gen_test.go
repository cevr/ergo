package bindc

import (
	"fmt"
	"math/big"
	"math/rand/v2"
	"strings"
)

// A generator of valid headers of the subset, for the properties. It writes
// source text, with random spacing and comments, and records the places
// where a refused construct can go: between top-level declarations, before
// a struct field, and before a parameter.

type genHeader struct {
	src string
	// top, field and param are byte offsets in src: the start of a line
	// between declarations, the start of a field, the start of a parameter.
	top, field, param []int
}

type gen struct {
	r     *rand.Rand
	next  int
	b     strings.Builder
	top   []int
	field []int
	param []int

	stdbool   bool
	constants []genConst
	// sized are the type names that have a size (scalar typedefs, structs,
	// callbacks); all are every typedef name (opaque included).
	sized, structs, all []string
}

type genConst struct {
	name string
	v    cval
}

func newGen(seed uint64) *gen {
	return &gen{r: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))}
}

func (g *gen) name(prefix string) string {
	g.next++
	return fmt.Sprintf("%s%d", prefix, g.next)
}

func (g *gen) pick(xs []string) string { return xs[g.r.IntN(len(xs))] }

// space is the text between two tokens. Inside a directive it never holds a
// line break.
func (g *gen) space(directive bool) string {
	switch n := g.r.IntN(20); {
	case n < 12:
		return " "
	case n < 14:
		return "  "
	case n < 15:
		return "\t"
	case n < 17:
		return " /* c */ "
	case directive:
		return " "
	case n < 19:
		return "\n    "
	default:
		return " // c\n"
	}
}

// join writes tokens with random spacing. A token that starts with "\x00"
// is glued to the one before it.
func (g *gen) join(toks []string, directive bool) string {
	var b strings.Builder
	for i, t := range toks {
		if strings.HasPrefix(t, "\x00") {
			b.WriteString(t[1:])
			continue
		}
		if i > 0 {
			b.WriteString(g.space(directive))
		}
		b.WriteString(t)
	}
	return b.String()
}

var genScalars = []string{"int8_t", "int16_t", "int32_t", "int64_t", "uint8_t", "uint16_t", "uint32_t", "uint64_t", "float", "double"}

func (g *gen) scalar() string {
	if g.stdbool && g.r.IntN(12) == 0 {
		return "bool"
	}
	return g.pick(genScalars)
}

// Header generates one header with n declarations.
func (g *gen) header(n int) genHeader {
	guard := g.r.IntN(3) != 0
	cpp := g.r.IntN(2) == 0
	g.stdbool = g.r.IntN(2) == 0
	incs := []string{"stdint.h"}
	if g.stdbool {
		incs = append(incs, "stdbool.h")
	}
	if g.r.IntN(2) == 0 {
		incs = append(incs, "stddef.h")
	}
	g.r.Shuffle(len(incs), func(i, j int) { incs[i], incs[j] = incs[j], incs[i] })
	if g.r.IntN(3) == 0 {
		g.b.WriteString("/* generated header */\n")
	}
	if guard {
		fmt.Fprintf(&g.b, "#ifndef GEN_H\n#define GEN_H\n\n")
	}
	for _, inc := range incs {
		fmt.Fprintf(&g.b, "#include <%s>\n", inc)
	}
	if cpp {
		g.b.WriteString("\n#ifdef __cplusplus\nextern \"C\" {\n#endif\n")
	}
	g.b.WriteString("\n")
	// The first items are a struct and a function with a parameter, so a
	// field and a parameter place always exist.
	g.top = append(g.top, g.b.Len())
	g.structDecl()
	g.top = append(g.top, g.b.Len())
	g.function(true)
	for i := 0; i < n; i++ {
		g.top = append(g.top, g.b.Len())
		switch k := g.r.IntN(10); {
		case k < 3:
			g.constant()
		case k < 4:
			g.scalarTypedef()
		case k < 5:
			g.opaque()
		case k < 7:
			g.structDecl()
		case k < 8:
			g.callback()
		default:
			g.function(false)
		}
	}
	g.top = append(g.top, g.b.Len())
	if cpp {
		g.b.WriteString("\n#ifdef __cplusplus\n}\n#endif\n")
	}
	if guard {
		g.b.WriteString("\n#endif /* GEN_H */\n")
	}
	return genHeader{src: g.b.String(), top: g.top, field: g.field, param: g.param}
}

// constant writes a #define with a body that evaluates without undefined
// behavior: a candidate expression that the evaluator refuses is replaced
// by a literal.
func (g *gen) constant() {
	name := strings.ToUpper(g.name("G_C"))
	var text string
	var v cval
	for try := 0; ; try++ {
		body := g.expr(3)
		if try > 4 {
			body = []string{fmt.Sprint(g.r.IntN(100))}
		}
		text = g.join(body, true)
		d := &diags{}
		toks := lex("expr", text, d)
		if len(d.list) > 0 {
			panic("gen: " + d.sorted().Error())
		}
		_, val, err := parseConstExpr(toks[:len(toks)-1], Pos{}, g.lookup, func(Pos) bool { return true })
		if err == nil {
			v = val
			break
		}
	}
	g.b.WriteString("#" + g.pick([]string{"", " "}) + "define " + name + " " + text + "\n")
	g.constants = append(g.constants, genConst{name, v})
}

func (g *gen) lookup(name string) (cval, bool) {
	for _, c := range g.constants {
		if c.name == name {
			return c.v, true
		}
	}
	return cval{}, false
}

var genIntC = []struct {
	name string
	max  uint64
}{
	{"INT8_C", 127}, {"INT16_C", 32767}, {"INT32_C", 1<<31 - 1}, {"INT64_C", 1<<63 - 1},
	{"UINT8_C", 255}, {"UINT16_C", 65535}, {"UINT32_C", 1<<32 - 1}, {"UINT64_C", 1<<64 - 1},
}

func (g *gen) literal(max uint64) string {
	var v uint64
	switch g.r.IntN(4) {
	case 0:
		v = uint64(g.r.IntN(10))
	case 1:
		v = g.r.Uint64N(1000)
	default:
		v = g.r.Uint64()
		if max != 1<<64-1 {
			v %= max + 1
		}
	}
	if g.r.IntN(3) == 0 {
		return fmt.Sprintf("0x%X", v)
	}
	return fmt.Sprint(v)
}

func (g *gen) expr(depth int) []string {
	if depth == 0 || g.r.IntN(3) == 0 {
		switch k := g.r.IntN(4); {
		case k == 0 && len(g.constants) > 0:
			return []string{g.constants[g.r.IntN(len(g.constants))].name}
		case k == 1:
			return []string{g.literal(1<<31 - 1)}
		default:
			c := genIntC[g.r.IntN(len(genIntC))]
			return []string{c.name, "\x00(" + g.literal(c.max) + ")"}
		}
	}
	switch g.r.IntN(4) {
	case 0:
		return append([]string{"-"}, g.expr(depth-1)...)
	case 1:
		return append(append([]string{"("}, g.expr(depth-1)...), ")")
	default:
		x, y := g.expr(depth-1), g.expr(depth-1)
		op := g.pick([]string{"*", "+", "-", "<<", "|"})
		return append(append(append([]string{"("}, x...), op), append(y, ")")...)
	}
}

func (g *gen) scalarTypedef() {
	name := g.name("g_s")
	g.b.WriteString(g.join([]string{"typedef", g.scalar(), name, "\x00;"}, false) + "\n")
	g.sized = append(g.sized, name)
	g.all = append(g.all, name)
}

func (g *gen) opaque() {
	name := g.name("g_o")
	tag := name
	if g.r.IntN(2) == 0 {
		tag = g.name("g_tag")
	}
	g.b.WriteString(g.join([]string{"typedef", "struct", tag, name, "\x00;"}, false) + "\n")
	g.all = append(g.all, name)
}

// valueType is a type with a size, as a list of tokens.
func (g *gen) valueType() []string {
	if len(g.sized) > 0 && g.r.IntN(3) == 0 {
		return []string{g.pick(g.sized)}
	}
	return []string{g.scalar()}
}

// pointerType is `[const] T *` or `T **`.
func (g *gen) pointerType() []string {
	var base []string
	switch k := g.r.IntN(4); {
	case k == 0:
		base = []string{"void"}
	case k == 1 && len(g.all) > 0:
		base = []string{g.pick(g.all)}
	default:
		base = g.valueType()
	}
	if g.r.IntN(2) == 0 {
		base = append([]string{"const"}, base...)
	}
	base = append(base, "*")
	if g.r.IntN(5) == 0 {
		base = append(base, "*")
	}
	return base
}

// arrayLen is a literal or the name of an earlier constant with a small
// positive value.
func (g *gen) arrayLen() string {
	var names []string
	for _, c := range g.constants {
		if c.v.v.Sign() > 0 && c.v.v.Cmp(big.NewInt(64)) <= 0 {
			names = append(names, c.name)
		}
	}
	if len(names) > 0 && g.r.IntN(2) == 0 {
		return g.pick(names)
	}
	return fmt.Sprint(1 + g.r.IntN(16))
}

func (g *gen) structDecl() {
	name := g.name("g_t")
	head := []string{"typedef", "struct"}
	switch g.r.IntN(3) {
	case 0:
		head = append(head, name)
	case 1:
		head = append(head, g.name("g_tag"))
	}
	g.b.WriteString(g.join(append(head, "{"), false) + "\n")
	nf := 1 + g.r.IntN(6)
	for i := 0; i < nf; i++ {
		g.field = append(g.field, g.b.Len())
		var toks []string
		ptr := g.r.IntN(3) == 0
		if ptr {
			toks = g.pointerType()
		} else {
			toks = g.valueType()
		}
		f := fmt.Sprintf("f%d", i)
		toks = append(toks, f)
		if g.r.IntN(4) == 0 {
			toks = append(toks, "\x00["+g.arrayLen()+"]")
		}
		if g.r.IntN(5) == 0 && !ptr {
			// a second declarator of the same base type
			toks = append(toks, "\x00,", fmt.Sprintf("f%db", i))
		}
		g.b.WriteString("    " + g.join(append(toks, "\x00;"), false) + "\n")
	}
	g.b.WriteString(g.join([]string{"}", name, "\x00;"}, false) + "\n")
	g.sized = append(g.sized, name)
	g.structs = append(g.structs, name)
	g.all = append(g.all, name)
}

func (g *gen) resultType() []string {
	switch g.r.IntN(4) {
	case 0:
		return []string{"void"}
	case 1:
		return g.pointerType()
	default:
		return g.valueType()
	}
}

// params writes a parameter list; record says whether to record the places.
func (g *gen) params(prefix string, record, atLeastOne bool) {
	n := g.r.IntN(5)
	if atLeastOne && n == 0 {
		n = 1
	}
	if n == 0 {
		g.b.WriteString(g.join([]string{"(", "\x00void", "\x00)"}, false))
		return
	}
	g.b.WriteString("(")
	for i := 0; i < n; i++ {
		if i > 0 {
			g.b.WriteString(", ")
		}
		if record {
			g.param = append(g.param, g.b.Len())
		}
		var toks []string
		if g.r.IntN(2) == 0 {
			toks = g.pointerType()
		} else {
			toks = g.valueType()
		}
		if g.r.IntN(4) != 0 {
			toks = append(toks, fmt.Sprintf("%s%d", prefix, i))
		}
		if g.r.IntN(8) == 0 {
			toks = append(toks, "\x00["+g.arrayLen()+"]")
		}
		g.b.WriteString(g.join(toks, false))
	}
	g.b.WriteString(")")
}

func (g *gen) callback() {
	name := g.name("g_cb")
	g.b.WriteString(g.join(append(append([]string{"typedef"}, g.resultType()...), "(", "\x00*"+name, "\x00)"), false))
	g.params("a", false, false)
	g.b.WriteString(";\n")
	g.sized = append(g.sized, name)
	g.all = append(g.all, name)
}

func (g *gen) function(atLeastOne bool) {
	name := g.name("g_f")
	g.b.WriteString(g.join(append(g.resultType(), name), false))
	g.params("p", true, atLeastOne)
	g.b.WriteString(g.pick([]string{";", " ;", ";  /* end */"}) + "\n")
}
