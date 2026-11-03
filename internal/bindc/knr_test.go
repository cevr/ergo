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
	// nbraced counts the braced types drawn in declaration lists.
	nbraced *int
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
		if g.r.IntN(2) == 0 {
			typ = g.pick("", "const ", "register ") + g.braced(2)
			*g.nbraced++
		}
		fmt.Fprintf(&b, "%s%s %s;", g.space(), typ, strings.Join(ds, ", "))
	}
	return b.String()
}

// attribute is an attribute, or nothing.
func (g knrGen) attribute() string {
	return g.pick("", "", "", " __attribute__((packed))", " __attribute__((aligned(8), unused))", " [[deprecated(\"{\")]]")
}

// braced is a struct, union or enum type with its braces: a tag or none,
// attributes before and after the braces, and fields of nested braced types
// up to depth, so a `{` and a `;` inside the type stand before the
// declarators.
func (g knrGen) braced(depth int) string {
	kind := g.pick("struct", "union", "enum")
	head := kind + g.attribute()
	if g.r.IntN(2) == 0 {
		head += " " + g.fresh("tag")
	}
	var members []string
	if kind == "enum" {
		for n := 1 + g.r.IntN(4); n > 0; n-- {
			e := g.fresh("E")
			switch g.r.IntN(5) {
			case 0:
				e += " = " + fmt.Sprint(g.r.IntN(100))
			case 1:
				e += " = (1 + 2) * 3"
			case 2:
				e += " = sizeof(struct { int q; char c[2]; })"
			}
			members = append(members, e)
		}
		tail := ""
		if g.r.IntN(2) == 0 {
			tail = ","
		}
		return head + " {" + g.space() + strings.Join(members, ","+g.space()) + tail + g.space() + "}" + g.attribute()
	}
	for n := 1 + g.r.IntN(3); n > 0; n-- {
		members = append(members, g.member(depth))
	}
	return head + " {" + g.space() + strings.Join(members, g.space()) + g.space() + "}" + g.attribute()
}

// member is one member declaration of a braced struct or union.
func (g knrGen) member(depth int) string {
	f := g.fresh("m")
	switch k := g.r.IntN(8); {
	case k == 0:
		return "uint8_t *" + f + ", " + g.fresh("m") + "[3];"
	case k == 1:
		return "unsigned " + f + " : 3;"
	case k == 2:
		return "void (*" + f + ")(int, struct { int z; } *);"
	case k == 3:
		return "_Static_assert(1, \"};{\");"
	case k == 4 && depth > 0:
		return g.braced(depth-1) + " " + f + ";"
	case k == 5 && depth > 0:
		// an anonymous struct or union member
		return g.pick("struct", "union") + " {" + g.space() + g.member(depth-1) + g.space() + "};"
	}
	return "uint32_t " + f + ";"
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

// afterRefusals are the top-level refused constructs that can follow a K&R
// construct: every one but the lexical refusals, which stop the parse
// before the K&R construct is read.
func afterRefusals() []refused {
	var rs []refused
	for _, rc := range refusedConstructs {
		if rc.where == "top" && rc.code != CodeLexical {
			rs = append(rs, rc)
		}
	}
	return rs
}

// TestKAndRProperty: a K&R declaration or definition, put into a generated
// valid header between two declarations, gives exactly one diagnostic, BC022
// at the function name, and no model. Parsing resumes at the next
// declaration: in three cases of four a refused construct follows the K&R
// construct at once, and its own diagnostic must be the second and last, so
// recovery that stops early or skips too far fails.
func TestKAndRProperty(t *testing.T) {
	after := afterRefusals()
	definitions, braced, followed := 0, 0, 0
	for i := 0; i < *propCases; i++ {
		seed := *propSeed + uint64(i)
		g := newGen(seed)
		h := g.header(2 + i%20)
		next := 0
		nbraced := 0
		kg := knrGen{g.r, &next, &nbraced}
		text, at := expand(kg.construct(), func() string { return kg.fresh("zz_knr") })
		if strings.HasSuffix(text, "}") {
			definitions++
		}
		if nbraced > 0 {
			braced++
		}
		place := h.top[g.r.IntN(len(h.top))]
		src := h.src[:place] + text + "\n"
		line, col := lineCol(src, place+at)
		wants := []string{fmt.Sprintf("gen.h:%d:%d: %s", line, col, CodeKAndR)}
		if i%4 != 0 {
			rc := after[g.r.IntN(len(after))]
			atext, aat := expand(rc.text, func() string { return kg.fresh("zz_after") })
			line, col := lineCol(src+atext, len(src)+aat)
			wants = append(wants, fmt.Sprintf("gen.h:%d:%d: %s", line, col, rc.code))
			src += atext + "\n"
			followed++
		}
		src += h.src[place:]
		m, err := Parse("gen.h", []byte(src))
		var ds Diagnostics
		errors.As(err, &ds)
		ok := m == nil && len(ds) == len(wants)
		for k := 0; ok && k < len(wants); k++ {
			ok = strings.HasPrefix(ds[k].String(), wants[k]+" ")
		}
		if !ok {
			t.Fatalf("seed %d: want the diagnostics %q and no model; got model %v and:\n%v\n--- source:\n%s",
				seed, wants, m != nil, err, numbered(src))
		}
	}
	t.Logf("%d cases, %d definitions, %d with a braced type, %d followed by a refused construct", *propCases, definitions, braced, followed)
	if braced < *propCases/10 || followed < *propCases/2 {
		t.Fatalf("the generator is off balance: %d braced, %d followed", braced, followed)
	}
}

// knrCases are fixed K&R definitions, each valid C11 (cc checks it): the
// round 1 counterexample (finding M3), the round 2 counterexamples with a
// braced parameter type (finding M1 of round 2), and their kin. `@` marks the
// one K&R diagnostic; a second `@` marks the diagnostic of the refused
// construct after it, which proves that recovery reaches it.
var knrCases = []string{
	"uint32_t @f(x) uint32_t x; { return x; }\nuint32_t g(void);\n",
	"uint32_t @f(x) struct { uint32_t a; } x; { return x.a; }\nuint32_t g(void);\n",
	"uint32_t @f(x) enum { A = 1, B = 2 } x; { return x; }\nuint32_t g(void);\n",
	"uint32_t @f(x) union u { uint32_t a; float b; } x; { return x.a; }\nuint32_t g(void);\n",
	"uint32_t @f(x, y) struct s { struct { uint32_t b; } in; enum { C = 3 } e; } x; const struct s *y; { return x.in.b + y->e; }\nuint32_t g(void);\n",
	"uint32_t @f(x) struct { uint32_t a; } x; { return x.a; }\n@int g(void);\n",
	"uint32_t @f(x) enum { A = 1, B = 2 } x; { return x; }\nuint32_t g(void) @{ return 0; }\n",
}

func TestKAndRDefinition(t *testing.T) {
	cc := requireCC(t)
	dir := t.TempDir()
	for _, c := range knrCases {
		src := "#include <stdint.h>\n"
		var wants []string
		for k, part := range strings.Split(c, "@") {
			if k > 0 {
				line, col := lineCol(src, len(src))
				code := CodeKAndR
				if k > 1 {
					code = CodeImplicitWidth
					if strings.HasPrefix(part, "{") {
						code = CodeStorageClass
					}
				}
				wants = append(wants, fmt.Sprintf("probe.h:%d:%d: %s", line, col, code))
			}
			src += part
		}
		if err := runCC(t, cc, dir, "valid.c", src); err != nil {
			t.Errorf("the case is not valid C11:\n%s\n%v", src, err)
		}
		m, err := Parse("probe.h", []byte(src))
		var ds Diagnostics
		errors.As(err, &ds)
		ok := m == nil && len(ds) == len(wants)
		for k := 0; ok && k < len(wants); k++ {
			ok = strings.HasPrefix(ds[k].String(), wants[k]+" ")
		}
		if !ok {
			t.Errorf("want the diagnostics %q and no model; got model %v and:\n%v\n--- source:\n%s", wants, m != nil, err, numbered(src))
		}
	}
}
