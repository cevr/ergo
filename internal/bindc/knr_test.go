package bindc

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// knrGen draws K&R constructs: definitions with an identifier list, a
// declaration list (types inside and outside the subset, several
// declarators, pointers, arrays, function pointers) and a body of nested
// statements; definitions with no declaration list or an empty identifier
// list; and K&R declarations. `@` marks the function name, where the one
// diagnostic must point.
type knrGen struct {
	r    *rand.Rand
	next *int
}

func (g knrGen) fresh(prefix string) string {
	*g.next++
	return fmt.Sprintf("%s%d", prefix, *g.next)
}

func (g knrGen) pick(xs ...string) string { return xs[g.r.IntN(len(xs))] }

// declarator declares name for a declaration list.
func (g knrGen) declarator(name string) string {
	switch g.r.IntN(6) {
	case 0:
		return "*" + name
	case 1:
		return name + "[4]"
	case 2:
		return "(*" + name + ")(" + g.pick("void", "int", "uint32_t a, uint8_t *b", "") + ")"
	case 3:
		return "* const " + name
	}
	return name
}

// declarations writes a declaration list that declares each name once.
func (g knrGen) declarations(names []string) string {
	var b strings.Builder
	order := g.r.Perm(len(names))
	for i := 0; i < len(order); {
		n := 1 + g.r.IntN(len(order)-i)
		ds := make([]string, n)
		for k := range ds {
			ds[k] = g.declarator(names[order[i+k]])
		}
		i += n
		typ := g.pick("int", "uint32_t", "const uint8_t", "register long", "float", "double", "struct s", "unsigned", "char", "g_unknown")
		fmt.Fprintf(&b, "%s%s %s;", g.space(), typ, strings.Join(ds, ", "))
	}
	return b.String()
}

func (g knrGen) space() string { return g.pick(" ", "\n", "\n    ", " /* k */ ", "\t") }

// statement is one statement of a body, with nested braces up to depth.
func (g knrGen) statement(names []string, depth int) string {
	x := "0"
	if len(names) > 0 {
		x = names[g.r.IntN(len(names))]
	}
	switch k := g.r.IntN(9); {
	case k == 0 && depth > 0:
		return "{" + g.body(names, depth-1) + "}"
	case k == 1 && depth > 0:
		return "if (" + x + " > 1) {" + g.body(names, depth-1) + "} else {" + g.body(names, depth-1) + "}"
	case k == 2 && depth > 0:
		return "for (int i = 0; i < 3; i++) {" + g.body(names, depth-1) + "}"
	case k == 3:
		return "while (" + x + ") " + x + "--;"
	case k == 4:
		return "uint32_t t = (" + x + ") * 2, u[2] = {1, 2};"
	case k == 5:
		return "return " + x + ";"
	case k == 6:
		return ";"
	case k == 7:
		return "s = \"};{\"; c = '}';"
	}
	return x + " = (" + x + " + 1);"
}

func (g knrGen) body(names []string, depth int) string {
	var b strings.Builder
	for n := g.r.IntN(4); n > 0; n-- {
		b.WriteString(g.space() + g.statement(names, depth))
	}
	return b.String() + g.space()
}

// construct returns one K&R construct with its `@` marker; `$` is a fresh
// name for the function.
func (g knrGen) construct() string {
	result := g.pick("uint32_t", "void", "uint8_t *", "double", "const uint8_t *")
	var names []string
	for n := 1 + g.r.IntN(4); n > 0; n-- {
		names = append(names, g.fresh("k"))
	}
	ids := strings.Join(names, ", ")
	switch g.r.IntN(5) {
	case 0: // a K&R declaration
		return result + " @$(" + g.pick(ids, "") + ");"
	case 1: // a definition with an empty identifier list
		return result + " @$()" + g.space() + "{" + g.body(nil, 2) + "}"
	case 2: // a definition with no declaration list
		return result + " @$(" + ids + ")" + g.space() + "{" + g.body(names, 2) + "}"
	}
	return result + " @$(" + ids + ")" + g.declarations(names) + g.space() + "{" + g.body(names, 2) + "}"
}

// TestKAndRProperty: a K&R declaration or definition, put into a generated
// valid header between two declarations, gives exactly one diagnostic, BC022
// at the function name, and no model; parsing resumes at the next
// declaration, so the header gives no second diagnostic.
func TestKAndRProperty(t *testing.T) {
	definitions := 0
	for i := 0; i < *propCases; i++ {
		seed := *propSeed + uint64(i)
		g := newGen(seed)
		h := g.header(2 + i%20)
		next := 0
		kg := knrGen{g.r, &next}
		text, at := expand(kg.construct(), func() string { return kg.fresh("zz_knr") })
		if strings.HasSuffix(text, "}") {
			definitions++
		}
		place := h.top[g.r.IntN(len(h.top))]
		src := h.src[:place] + text + "\n" + h.src[place:]
		line, col := lineCol(src, place+at)
		m, err := Parse("gen.h", []byte(src))
		var ds Diagnostics
		errors.As(err, &ds)
		want := fmt.Sprintf("gen.h:%d:%d: %s", line, col, CodeKAndR)
		if m != nil || len(ds) != 1 || !strings.HasPrefix(ds[0].String(), want+" ") {
			t.Fatalf("seed %d: want one diagnostic %s and no model; got model %v and:\n%v\n--- source:\n%s",
				seed, want, m != nil, err, numbered(src))
		}
	}
	t.Logf("%d cases, %d definitions", *propCases, definitions)
}

// TestKAndRDefinition is the round 1 counterexample (finding M3).
func TestKAndRDefinition(t *testing.T) {
	src := "#include <stdint.h>\nuint32_t f(x) uint32_t x; { return x; }\nuint32_t g(void);\n"
	m, err := Parse("probe.h", []byte(src))
	var ds Diagnostics
	errors.As(err, &ds)
	if m != nil || len(ds) != 1 || !strings.HasPrefix(ds[0].String(), "probe.h:2:10: "+string(CodeKAndR)+" ") {
		t.Fatalf("want one diagnostic probe.h:2:10: %s and no model; got model %v and:\n%v", CodeKAndR, m != nil, err)
	}
}
