package bindc

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// A macro body is replacement tokens, not a value: C substitutes the body of
// an earlier macro where its name appears and parses the result. These tests
// hold the model to that meaning.

// macroCases are fixed counterexamples: the name of an earlier macro whose
// body has a lower precedence than the operator around the name. want is the
// value and type C gives the constant name; size, when not zero, is the size
// C gives the struct S of the header.
var macroCases = []struct {
	src  string
	name string
	want int64
	typ  Scalar
	size int64
}{
	// round 1 findings, M1
	{"#define A 1 + 2\n#define B A * 3\ntypedef struct { uint8_t bytes[B]; } S;\n", "B", 7, Int32, 7},
	{"#define A 1 + 2\n#define B UINT32_C(4) - A\n", "B", 5, Uint32, 0},
	// one per operator pair that atomic substitution gets wrong
	{"#define A 1 << 2\n#define B A + 1\n", "B", 8, Int32, 0},
	{"#define A 2 | 1\n#define B A << 1\n", "B", 2, Int32, 0},
	{"#define A 3 - 1\n#define B 0 - A\n", "B", -4, Int32, 0},
	{"#define A 2 + 3\n#define B - A\n", "B", 1, Int32, 0},
	{"#define A 1 + 1\n#define B A * A\n#define C B * B\n", "C", 1 + 1*1 + 1*1 + 1*1 + 1, Int32, 0},
	{"#define A 4 | 1\n#define B (A) << 1\n", "B", 10, Int32, 0},
	{"#define A UINT32_C(1) - 2\n#define B A * 0\n", "B", 1, Uint32, 0},
}

func TestMacroReplacement(t *testing.T) {
	for _, c := range macroCases {
		src := "#include <stdint.h>\n" + c.src
		m, err := Parse("macro.h", []byte(src))
		if err != nil {
			t.Errorf("%q: %v", c.src, err)
			continue
		}
		var got *Constant
		for i := range m.Constants {
			if m.Constants[i].Name == c.name {
				got = &m.Constants[i]
			}
		}
		if got == nil || got.Type != c.typ || got.Int64() != c.want {
			t.Errorf("%q: %s = %v, C gives %d of type %s", c.src, c.name, got, c.want, c.typ)
		}
		for _, d := range m.Types {
			if d.Name == "S" && d.Size != c.size {
				t.Errorf("%q: sizeof(S) = %d, C gives %d", c.src, d.Size, c.size)
			}
		}
	}
	// cc agrees with the fixed values above and with the model.
	cc := requireCC(t)
	dir := t.TempDir()
	for i, c := range macroCases {
		header := fmt.Sprintf("case%d.h", i)
		src := "#include <stdint.h>\n" + c.src
		writeFile(t, filepath.Join(dir, header), src)
		m, err := Parse(header, []byte(src))
		if err != nil {
			continue // reported above
		}
		check := oracleSource(m, header, nil) + fmt.Sprintf("_Static_assert((%s) == %dLL, \"C value of %s\");\n", c.name, c.want, c.name)
		if err := runCC(t, cc, dir, "case.c", check); err != nil {
			t.Errorf("%q: cc refuses the model:\n%v", c.src, err)
		}
	}
}

func writeFile(t *testing.T, path, src string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ccIsGCC reports whether cc is GCC. The refusal half of the macro property
// relies on GCC's -pedantic-errors diagnostics for undefined behavior in a
// constant expression (measured on GCC 13), so it runs with GCC only.
// TestMacroDefinedProperty checks false refusals on every host without them.
func ccIsGCC(cc string) bool {
	out, err := exec.Command(cc, "-dM", "-E", "-x", "c", os.DevNull).Output()
	return err == nil && strings.Contains(string(out), "#define __GNUC__ ") && !strings.Contains(string(out), "__clang__")
}

// macroGen draws macro chains over the whole constant subset with no help
// from the evaluator under test: literals of every C type (decimal and hex,
// small and at the type limits), INTn_C and UINTn_C with an argument in
// range, unary minus, parentheses, the five operators with no forced
// parentheses, and names of earlier macros of the chain.
type macroGen struct{ r *rand.Rand }

var macroLimits = []uint64{
	1<<7 - 1, 1 << 7, 1<<8 - 1, 1<<15 - 1, 1<<16 - 1,
	1<<31 - 1, 1 << 31, 1<<32 - 1, 1 << 32, 1<<63 - 1, 1 << 63, 1<<64 - 1,
}

func (g macroGen) literal() string {
	var v uint64
	switch k := g.r.IntN(20); {
	case k < 11:
		v = uint64(g.r.IntN(10))
	case k < 16:
		v = g.r.Uint64N(1000)
	case k < 19:
		v = macroLimits[g.r.IntN(len(macroLimits))] - uint64(g.r.IntN(2))
	default:
		v = g.r.Uint64()
	}
	if g.r.IntN(3) == 0 {
		return fmt.Sprintf("0x%X", v)
	}
	if v > 1<<63-1 && g.r.IntN(4) != 0 {
		v >>= 1 // a decimal literal above INT64_MAX has no type: keep it rare
	}
	return fmt.Sprint(v)
}

func (g macroGen) leaf(earlier []string) []string {
	switch k := g.r.IntN(10); {
	case k < 4 && len(earlier) > 0:
		return []string{earlier[g.r.IntN(len(earlier))]}
	case k < 7:
		return []string{g.literal()}
	default:
		c := genIntC[g.r.IntN(len(genIntC))]
		var v uint64
		switch g.r.IntN(3) {
		case 0:
			v = uint64(g.r.IntN(10))
		case 1:
			v = c.max
		default:
			v = g.r.Uint64()
			if c.max != 1<<64-1 {
				v %= c.max + 1
			}
		}
		lit := fmt.Sprint(v)
		if g.r.IntN(3) == 0 {
			lit = fmt.Sprintf("0x%X", v)
		}
		return []string{c.name, "(", lit, ")"}
	}
}

// operand is a unary expression: minus signs and a primary.
func (g macroGen) operand(earlier []string, depth int) []string {
	switch k := g.r.IntN(10); {
	case k < 2:
		return append([]string{"-"}, g.operand(earlier, depth)...)
	case k < 4 && depth > 0:
		return append(append([]string{"("}, g.body(earlier, depth-1)...), ")")
	}
	return g.leaf(earlier)
}

// body is operands joined by binary operators, without parentheses, so the
// precedence of C decides the grouping.
func (g macroGen) body(earlier []string, depth int) []string {
	toks := g.operand(earlier, depth)
	for n := g.r.IntN(4); n > 0; n-- {
		// + - | twice as often as * and <<, which overflow sooner
		op := []string{"*", "+", "-", "<<", "|", "+", "-", "|"}[g.r.IntN(8)]
		toks = append(append(toks, op), g.operand(earlier, depth)...)
	}
	return toks
}

// chain returns n #define lines with names prefix0, prefix1, ...
func (g macroGen) chain(prefix string, n int) (defs, names []string) {
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("%s%d", prefix, i)
		defs = append(defs, "#define "+name+" "+strings.Join(g.body(names, 2), " "))
		names = append(names, name)
	}
	return defs, names
}

var ccFileError = regexp.MustCompile(`(?m)^([^:\s]+\.c):\d+:\d+: error`)

// maxRefusedCC bounds the refused chains cc checks in one run (one file
// each, about 8 ms each with GCC 13, measured); every refused chain is
// still checked for its diagnostic, and another seed checks other chains.
const maxRefusedCC = 64

// TestMacroExpansionProperty: for generated macro chains, the model means
// what C means. A chain the parser accepts must agree with cc on the value
// and type class of every constant and on the layout of a struct whose
// arrays take constants as lengths; cc checks the whole batch in one run
// with -pedantic-errors, so undefined behavior the parser misses fails too.
// A chain the parser refuses must be refused by its first diagnostic
// (BC025: undefined behavior or no type), and with GCC, cc must refuse the
// refused macro too. A mutant control (one value off by one) must fail cc.
func TestMacroExpansionProperty(t *testing.T) {
	cc := requireCC(t)
	gcc := ccIsGCC(cc)
	dir := t.TempDir()
	g := macroGen{rand.New(rand.NewPCG(*propSeed, 0x6d6163726f))}
	var all, mutant strings.Builder
	var refusedFiles []string
	accepted, refused, structs, chained, wrong := 0, 0, 0, 0, 0
	accept := func(i int, defs []string) {
		header := fmt.Sprintf("m%d.h", i)
		src := "#include <stdint.h>\n" + strings.Join(defs, "\n") + "\n"
		m, err := Parse(header, []byte(src))
		if err != nil {
			t.Fatalf("case %d: a prefix of an accepted chain is refused:\n%v\n%s", i, err, src)
		}
		// A struct with the constants C sizes as array lengths.
		var fields []string
		for k, c := range m.Constants {
			small := c.Uint64() >= 1 && c.Uint64() <= 64
			if c.Signed() {
				small = c.Int64() >= 1 && c.Int64() <= 64
			}
			if small && len(fields) < 3 {
				fields = append(fields, fmt.Sprintf("%s f%d[%s];", []string{"uint8_t", "uint16_t", "uint64_t"}[k%3], k, c.Name))
			}
		}
		if len(fields) > 0 {
			src += fmt.Sprintf("typedef struct { uint8_t lead; %s uint32_t tail; } m%d_s;\n", strings.Join(fields, " "), i)
			if m, err = Parse(header, []byte(src)); err != nil {
				t.Fatalf("case %d: the struct over accepted constants is refused:\n%v\n%s", i, err, src)
			}
			structs++
		}
		for _, d := range defs {
			// A body names an earlier macro: only names have an `M`.
			if strings.Contains(strings.SplitN(d, " ", 3)[2], "M") {
				chained++
				break
			}
		}
		writeFile(t, filepath.Join(dir, header), src)
		all.WriteString(oracleSource(m, header, nil))
		if accepted == 0 {
			mutant.WriteString(oracleSource(m, header, func(c *Model) {
				c.Constants = append([]Constant{}, c.Constants...)
				c.Constants[0].Bits++
			}))
		} else {
			mutant.WriteString(oracleSource(m, header, nil))
		}
		accepted++
	}
	for i := 0; i < *propCases; i++ {
		defs, names := g.chain(fmt.Sprintf("M%d_", i), 1+g.r.IntN(6))
		src := "#include <stdint.h>\n" + strings.Join(defs, "\n") + "\n"
		_, err := Parse("m.h", []byte(src))
		if err == nil {
			accept(i, defs)
			continue
		}
		var ds Diagnostics
		if !errors.As(err, &ds) || len(ds) == 0 {
			t.Fatalf("case %d: %v", i, err)
		}
		// The chain is in the subset, so the only refusal is a value C
		// does not define; the first diagnostic names its macro (line k+2).
		k := ds[0].Pos.Line - 2
		if ds[0].Code != CodeConstantRange || k < 0 || k >= len(defs) {
			if wrong++; wrong <= 3 {
				t.Errorf("case %d: a chain of the subset gives %v\n%s", i, err, numbered(src))
			}
			continue
		}
		refused++
		if k > 0 {
			accept(i, defs[:k])
		}
		if gcc && len(refusedFiles) < maxRefusedCC {
			name := filepath.Join(dir, fmt.Sprintf("r%d.c", i))
			writeFile(t, name, "#include <stdint.h>\n"+strings.Join(defs[:k+1], "\n")+
				fmt.Sprintf("\n_Static_assert((%s) == (%s), \"refused\");\n", names[k], names[k]))
			refusedFiles = append(refusedFiles, name)
		}
	}
	t.Logf("%d chains: %d accepted (%d with a struct, %d naming an earlier macro), %d refused (%d checked with cc), %d refused wrongly",
		*propCases, accepted, structs, chained, refused, len(refusedFiles), wrong)
	if accepted < *propCases/2 || refused == 0 {
		t.Fatalf("the generator is off balance: %d accepted, %d refused", accepted, refused)
	}
	if err := runCC(t, cc, dir, "accepted.c", all.String()); err != nil {
		lines := strings.Split(err.Error(), "\n")
		if len(lines) > 40 {
			lines = append(lines[:40], "...")
		}
		t.Fatalf("cc refuses the model of an accepted chain (the assertion names the macro; the header is m<case>.h):\n%s", strings.Join(lines, "\n"))
	}
	if err := runCC(t, cc, dir, "mutant.c", mutant.String()); err == nil {
		t.Fatalf("cc accepts a constant off by one: the oracle checks nothing")
	}
	if !gcc {
		// TestMacroDefinedProperty checks false refusals on every host.
		t.Logf("cc is not GCC: the refused chains are not checked with cc here")
		return
	}
	args := append(append([]string{}, ccArgs...), refusedFiles...)
	out, _ := exec.Command(cc, args...).CombinedOutput()
	failed := map[string]bool{}
	for _, m := range ccFileError.FindAllStringSubmatch(string(out), -1) {
		failed[filepath.Base(m[1])] = true
	}
	var accepts []string
	for _, f := range refusedFiles {
		if !failed[filepath.Base(f)] {
			src, _ := os.ReadFile(f)
			accepts = append(accepts, fmt.Sprintf("%s:\n%s", filepath.Base(f), src))
		}
	}
	sort.Strings(accepts)
	if len(accepts) > 0 {
		t.Fatalf("the parser refuses %d chains that cc accepts; the first:\n%s", len(accepts), accepts[0])
	}
}
