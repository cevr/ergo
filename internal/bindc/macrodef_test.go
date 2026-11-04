package bindc

import (
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"testing"
)

// C defines every macro chain of three families by construction, so the
// parser must accept each one on every host. The families decide validity
// with no help from the evaluator under test and no compiler's diagnostics;
// cc then checks the value and type C gives each constant.
//
//   - limits: an operand at a type limit (each in turn), with zero, one or two
//     minus signs, then identity operations (`+ 0`, `- 0`, `| 0`, `* 1`, with
//     operands of other types) and names of earlier macros of the chain. Every
//     value is the operand or its negation: a literal is not negative, so its
//     negation never overflows, and an identity operation keeps the value.
//   - unsigned: operands of type unsigned int or unsigned long (UINT32_C,
//     UINT64_C, hex literals above INT32_MAX or INT64_MAX), the operators
//     `+ - * |`, unary minus, parentheses and earlier names, grouped by C's
//     precedence alone. A shift is `(E << k)` with k a literal below 32. Every
//     operation is unsigned and wraps; no shift count is out of range.
//   - small: operands 0 to 9 of type int or long, the operators `+ - * |`,
//     unary minus, parentheses and earlier names, at most 9 operands after
//     expansion. |a op b|+1 <= (|a|+1)(|b|+1) for each operator, so every
//     value is below 10^9 in magnitude and no operation overflows.

// limitLeaves are the operands at a type limit: decimal literals up to
// INT64_MAX (a larger one has no type), hex literals up to UINT64_MAX, and
// each INTn_C and UINTn_C at 0, its maximum less one and its maximum, in
// decimal and in hex.
func limitLeaves() []string {
	var ls []string
	for _, v := range macroLimits {
		for _, w := range []uint64{v, v - 1} {
			if w <= 1<<63-1 {
				ls = append(ls, fmt.Sprint(w))
			}
			ls = append(ls, fmt.Sprintf("0x%X", w))
		}
	}
	for _, c := range genIntC {
		for _, v := range []uint64{0, c.max - 1, c.max} {
			ls = append(ls, fmt.Sprintf("%s(%d)", c.name, v), fmt.Sprintf("%s(0x%X)", c.name, v))
		}
	}
	return ls
}

type definedGen struct{ r *rand.Rand }

func (g definedGen) pick(xs ...string) string { return xs[g.r.IntN(len(xs))] }

// limitBody is a body of the limits family that starts with leaf, or with
// an earlier name.
func (g definedGen) limitBody(leaf string, earlier []string) string {
	head := leaf
	if len(earlier) > 0 && g.r.IntN(2) == 0 {
		head = earlier[g.r.IntN(len(earlier))]
	}
	body := g.pick("", "", "- ", "- - ") + head
	for n := g.r.IntN(4); n > 0; n-- {
		body += " " + g.pick("+ 0", "- 0", "| 0", "* 1", "+ INT8_C(0)", "- 0x0", "| UINT64_C(0)", "* UINT8_C(1)", "+ INT64_C(0)", "* UINT32_C(1)")
	}
	return body
}

// unsignedValue is a value up to max: small, at the limit, or random.
func (g definedGen) unsignedValue(max uint64) uint64 {
	switch g.r.IntN(4) {
	case 0:
		return uint64(g.r.IntN(10))
	case 1:
		return max - uint64(g.r.IntN(2))
	}
	if max == 1<<64-1 {
		return g.r.Uint64()
	}
	return g.r.Uint64N(max + 1)
}

func (g definedGen) unsignedLeaf() string {
	switch g.r.IntN(4) {
	case 0: // unsigned int
		return fmt.Sprintf("0x%X", 1<<31+g.unsignedValue(1<<31-1))
	case 1: // unsigned long
		return fmt.Sprintf("0x%X", 1<<63+g.unsignedValue(1<<63-1))
	case 2:
		return fmt.Sprintf("UINT32_C(%d)", g.unsignedValue(1<<32-1))
	}
	return fmt.Sprintf("UINT64_C(%d)", g.unsignedValue(1<<64-1))
}

func (g definedGen) unsignedOperand(earlier []string, depth int) string {
	switch k := g.r.IntN(10); {
	case k < 2:
		return "- " + g.unsignedOperand(earlier, depth)
	case k < 3 && depth > 0:
		return "(" + g.unsignedBody(earlier, depth-1) + ")"
	case k < 5 && depth > 0:
		return "(" + g.unsignedBody(earlier, depth-1) + " << " + fmt.Sprint(g.r.IntN(32)) + ")"
	case k < 7 && len(earlier) > 0:
		return earlier[g.r.IntN(len(earlier))]
	}
	return g.unsignedLeaf()
}

func (g definedGen) unsignedBody(earlier []string, depth int) string {
	body := g.unsignedOperand(earlier, depth)
	for n := g.r.IntN(4); n > 0; n-- {
		body += " " + g.pick("+", "-", "*", "|") + " " + g.unsignedOperand(earlier, depth)
	}
	return body
}

// smallMacro is an earlier macro of the small family and the count of
// operands of its expansion.
type smallMacro struct {
	name     string
	operands int
}

func (g definedGen) smallLeaf() string {
	v := g.r.IntN(10)
	switch g.r.IntN(4) {
	case 0:
		return fmt.Sprint(v)
	case 1:
		return fmt.Sprintf("0x%X", v)
	}
	return fmt.Sprintf("%s(%d)", g.pick("INT8_C", "INT16_C", "INT32_C", "INT64_C", "UINT8_C", "UINT16_C"), v)
}

// smallOperand takes its operands from budget, which is above zero.
func (g definedGen) smallOperand(earlier []smallMacro, depth int, budget *int) string {
	switch k := g.r.IntN(10); {
	case k < 2:
		return "- " + g.smallOperand(earlier, depth, budget)
	case k < 4 && depth > 0:
		return "(" + g.smallBody(earlier, depth-1, budget) + ")"
	case k < 7:
		var fit []smallMacro
		for _, m := range earlier {
			if m.operands <= *budget {
				fit = append(fit, m)
			}
		}
		if len(fit) > 0 {
			m := fit[g.r.IntN(len(fit))]
			*budget -= m.operands
			return m.name
		}
	}
	*budget--
	return g.smallLeaf()
}

func (g definedGen) smallBody(earlier []smallMacro, depth int, budget *int) string {
	body := g.smallOperand(earlier, depth, budget)
	for n := g.r.IntN(4); n > 0 && *budget > 0; n-- {
		body += " " + g.pick("+", "-", "*", "|") + " " + g.smallOperand(earlier, depth, budget)
	}
	return body
}

// chain returns the #define lines of case i: family i%3, names D<i>_<k>.
// The limits family starts with leaf.
func (g definedGen) chain(i int, leaf string) (defs []string, named bool) {
	n := 1 + g.r.IntN(5)
	var names []string
	var small []smallMacro
	for k := 0; k < n; k++ {
		name := fmt.Sprintf("D%d_%d", i, k)
		var body string
		switch i % 3 {
		case 0:
			if k > 0 {
				leaf = limitLeavesCache[g.r.IntN(len(limitLeavesCache))]
			}
			body = g.limitBody(leaf, names)
		case 1:
			body = g.unsignedBody(names, 2)
		default:
			budget := 9
			body = g.smallBody(small, 2, &budget)
			small = append(small, smallMacro{name, 9 - budget})
		}
		named = named || strings.Contains(body, fmt.Sprintf("D%d_", i))
		defs = append(defs, "#define "+name+" "+body)
		names = append(names, name)
	}
	return defs, named
}

var limitLeavesCache = limitLeaves()

// TestMacroDefinedProperty: every chain of the three families is accepted,
// and cc agrees with the model on the value and type of every constant. A
// parser that refuses a chain C defines fails here on every host, whatever
// cc is. A mutant control (one value off by one) must fail cc.
func TestMacroDefinedProperty(t *testing.T) {
	cc := requireCC(t)
	dir := t.TempDir()
	g := definedGen{rand.New(rand.NewPCG(*propSeed, 0x646566696e6564))}
	leaves := limitLeavesCache
	cases := *propCases
	if cases < 3*len(leaves) {
		cases = 3 * len(leaves)
	}
	var all, mutant strings.Builder
	accepted, refused, named := 0, 0, 0
	for i := 0; i < cases; i++ {
		defs, n := g.chain(i, leaves[(i/3)%len(leaves)])
		header := fmt.Sprintf("d%d.h", i)
		src := "#include <stdint.h>\n" + strings.Join(defs, "\n") + "\n"
		m, err := Parse(header, []byte(src))
		if err != nil {
			if refused++; refused <= 3 {
				t.Errorf("case %d (family %d): the parser refuses a chain C defines:\n%v\n%s", i, i%3, err, numbered(src))
			}
			continue
		}
		if n {
			named++
		}
		writeFile(t, filepath.Join(dir, header), src)
		all.WriteString(oracleSource(m, header, nil))
		if accepted == 0 {
			mutant.WriteString(oracleSource(m, header, func(c *Model) {
				c.Constants = append([]Constant{}, c.Constants...)
				c.Constants[0].Bits++
			}))
		}
		accepted++
	}
	t.Logf("%d chains in three families (%d limit operands, each first in at least one chain): %d accepted, %d naming an earlier macro, %d refused",
		cases, len(leaves), accepted, named, refused)
	if refused > 0 {
		t.Fatalf("the parser refuses %d chains that C defines", refused)
	}
	if err := runCC(t, cc, dir, "defined.c", all.String()); err != nil {
		lines := strings.Split(err.Error(), "\n")
		if len(lines) > 40 {
			lines = append(lines[:40], "...")
		}
		t.Fatalf("cc refuses the model of a chain C defines (the assertion names the macro; the header is d<case>.h):\n%s", strings.Join(lines, "\n"))
	}
	if err := runCC(t, cc, dir, "mutant.c", mutant.String()); err == nil {
		t.Fatalf("cc accepts a constant off by one: the oracle checks nothing")
	}
}
