package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"
)

var tabSeed = flag.Uint64("tab.seed", 0, "run TestTabulatedAgree on this seed alone")

// tabDecl matches the flags of a table of a generated function gJ.
var tabFlags = regexp.MustCompile(`(?m)^var tabg([0-9]+)K `)

// tabGen writes one TestTabulatedAgree program: enums E0..E2, generated
// functions gJ of one or two parameters (an enum, a bool, a byte, or an
// int, which no table takes) whose bodies decide (a match, an if) or do
// not, with results of each kind (an enum, an int, a record, a string,
// or a slice, which no table holds), some calling an earlier gM; and a
// main that calls every gJ twice on every argument of its domain.
type tabGen struct {
	r        *rand.Rand
	nv       []int
	b        strings.Builder
	params   [][]string
	results  []string
	eligible []bool
	reached  map[string]bool
}

var tabParamKinds = []string{"E0", "E1", "E2", "bool", "byte", "int"}
var tabResultKinds = []string{"E0", "E1", "E2", "int", "R", "str", "slice"}

func (g *tabGen) goType(k string) string {
	switch k {
	case "str":
		return "string"
	case "slice":
		return "[]int"
	}
	return k
}

// leaf is a value of kind k: a constant, or a call of an earlier gM of the
// same parameters and result.
func (g *tabGen) leaf(j int, k string) string {
	if g.r.IntN(4) == 0 {
		for m := j - 1; m >= 0; m-- {
			if g.results[m] == k && strings.Join(g.params[m], ",") == strings.Join(g.params[j], ",") {
				args := make([]string, len(g.params[j]))
				for i := range args {
					args[i] = fmt.Sprintf("p%d", i)
				}
				g.reached["call of an earlier function"] = true
				return fmt.Sprintf("g%d(%s)", m, strings.Join(args, ", "))
			}
		}
	}
	n := g.r.IntN(50)
	switch k {
	case "E0", "E1", "E2":
		e := int(k[1] - '0')
		return fmt.Sprintf("V%dx%d", e, g.r.IntN(g.nv[e]))
	case "int":
		return fmt.Sprint(n)
	case "R":
		return fmt.Sprintf("R{e: V0x%d, n: %d, s: \"r%d\"}", g.r.IntN(g.nv[0]), n, n%7)
	case "str":
		return fmt.Sprintf("\"w%d\"", n)
	}
	return fmt.Sprintf("[]int{%d, %d}", n, n+1)
}

// decide is a decision on parameter i, then on the next, down to leaves.
func (g *tabGen) decide(j, i int, k, ind string) string {
	if i >= len(g.params[j]) {
		return ind + g.leaf(j, k) + "\n"
	}
	p := fmt.Sprintf("p%d", i)
	var b strings.Builder
	switch kind := g.params[j][i]; kind {
	case "E0", "E1", "E2":
		e := int(kind[1] - '0')
		b.WriteString(ind + "match " + p + " {\n")
		split := 1 + g.r.IntN(g.nv[e]-1)
		var first, rest []string
		for v := range g.nv[e] {
			name := fmt.Sprintf("V%dx%d", e, v)
			if v < split {
				first = append(first, name)
			} else {
				rest = append(rest, name)
			}
		}
		b.WriteString(ind + "case " + strings.Join(first, ", ") + ":\n" + g.decide(j, i+1, k, ind+"\t"))
		b.WriteString(ind + "case " + strings.Join(rest, ", ") + ":\n" + g.decide(j, i+1, k, ind+"\t"))
		b.WriteString(ind + "}\n")
	case "bool":
		b.WriteString(ind + "if " + p + " {\n" + g.decide(j, i+1, k, ind+"\t") + ind + "} else {\n" + g.decide(j, i+1, k, ind+"\t") + ind + "}\n")
	case "byte":
		b.WriteString(fmt.Sprintf("%sif %s < %d {\n", ind, p, 1+g.r.IntN(200)) + g.decide(j, i+1, k, ind+"\t"))
		b.WriteString(fmt.Sprintf("%s} else if %s == %d {\n", ind, p, g.r.IntN(256)) + g.decide(j, i+1, k, ind+"\t"))
		b.WriteString(ind + "} else {\n" + g.decide(j, i+1, k, ind+"\t") + ind + "}\n")
	default:
		b.WriteString(fmt.Sprintf("%sif %s > %d {\n", ind, p, g.r.IntN(3)) + g.decide(j, i+1, k, ind+"\t") + ind + "} else {\n" + g.decide(j, i+1, k, ind+"\t") + ind + "}\n")
	}
	return b.String()
}

func (g *tabGen) function(j int) {
	np := 1 + g.r.IntN(2)
	var ps []string
	finite := true
	for range np {
		k := tabParamKinds[g.r.IntN(len(tabParamKinds))]
		if k == "byte" && len(ps) > 0 && ps[0] == "byte" {
			k = "bool"
		}
		finite = finite && k != "int"
		ps = append(ps, k)
	}
	res := tabResultKinds[g.r.IntN(len(tabResultKinds))]
	g.params = append(g.params, ps)
	g.results = append(g.results, res)
	decides := g.r.IntN(6) != 0
	var sig []string
	for i, k := range ps {
		sig = append(sig, fmt.Sprintf("p%d %s", i, g.goType(k)))
	}
	fmt.Fprintf(&g.b, "func g%d(%s) %s {\n", j, strings.Join(sig, ", "), g.goType(res))
	if decides {
		g.b.WriteString(g.decide(j, 0, res, "\t"))
	} else {
		// No decision: every parameter is read, in a let, and the result
		// is a constant.
		for i := range ps {
			fmt.Fprintf(&g.b, "\tlet _ = p%d\n", i)
		}
		g.b.WriteString("\t" + g.leaf(j, res) + "\n")
		g.reached["no decision"] = true
	}
	g.b.WriteString("}\n\n")
	g.eligible = append(g.eligible, finite && decides && res != "slice")
	if !finite {
		g.reached["int parameter"] = true
	}
	if res == "slice" {
		g.reached["slice result"] = true
	}
}

// code is the Go-free digest of a value v of kind k.
func tabCode(k, v string) string {
	switch k {
	case "E0", "E1", "E2":
		return "code" + k + "(" + v + ")"
	case "int":
		return v
	case "R":
		return "codeE0(" + v + ".e) + " + v + ".n * 7 + len(" + v + ".s)"
	case "str":
		return "len(" + v + ") * 3"
	}
	return v
}

// tabProgram is the program of seed, which functions a table should take
// (by index), and whether it ends in a defect.
func tabProgram(seed uint64, reached map[string]bool) (src string, eligible []bool, defect bool) {
	g := &tabGen{r: rand.New(rand.NewPCG(seed, 0x746162)), reached: reached}
	var s strings.Builder
	for e := range 3 {
		n := 2 + g.r.IntN(4)
		g.nv = append(g.nv, n)
		var vs, lits []string
		for v := range n {
			vs = append(vs, fmt.Sprintf("V%dx%d{}", e, v))
			lits = append(lits, fmt.Sprintf("V%dx%d", e, v))
		}
		fmt.Fprintf(&s, "type E%d = %s\n\n", e, strings.Join(vs, " | "))
		fmt.Fprintf(&s, "func allE%d() []E%d { []E%d{%s} }\n\n", e, e, e, strings.Join(lits, ", "))
		fmt.Fprintf(&s, "func codeE%d(v E%d) int {\n\tmatch v {\n", e, e)
		for v := range n {
			fmt.Fprintf(&s, "\tcase V%dx%d:\n\t\t%d\n", e, v, v+1)
		}
		s.WriteString("\t}\n}\n\n")
	}
	s.WriteString("type R { e E0, n int, s string }\n\n")
	s.WriteString("func allbool() []bool { []bool{false, true} }\n\n")
	s.WriteString("func allbyte() []byte {\n\tlet mut out = []byte{}\n\tfor i in 0..256 {\n\t\tout = out.Append(byte(i))\n\t}\n\tout\n}\n\n")
	s.WriteString("func allint() []int { []int{0, 1, 2, 3} }\n\n")
	nf := 6 + g.r.IntN(5)
	for j := range nf {
		g.function(j)
	}
	s.WriteString(g.b.String())
	var m strings.Builder
	m.WriteString("func main() needs Console {\n")
	for j := range nf {
		ps := g.params[j]
		fmt.Fprintf(&m, "\tlet mut acc%d = 0\n", j)
		ind := "\t"
		var args []string
		for i, k := range ps {
			fmt.Fprintf(&m, "%sfor x%d in all%s() {\n", ind, i, k)
			ind += "\t"
			args = append(args, fmt.Sprintf("x%d", i))
		}
		call := fmt.Sprintf("g%d(%s)", j, strings.Join(args, ", "))
		for rep := range 2 {
			if g.results[j] == "slice" {
				// A caller writes the slice it was given: a table would
				// share it with the next call.
				fmt.Fprintf(&m, "%slet mut xs%d = %s\n%sxs%d[0] = xs%d[0] + 5\n%sacc%d = (acc%d * 31 + xs%d[0] + xs%d[1]) %% 1000003\n", ind, rep, call, ind, rep, rep, ind, j, j, rep, rep)
			} else {
				fmt.Fprintf(&m, "%slet v%d = %s\n%sacc%d = (acc%d * 31 + %s) %% 1000003\n", ind, rep, call, ind, j, j, tabCode(g.results[j], fmt.Sprintf("v%d", rep)))
			}
		}
		for range ps {
			ind = ind[1:]
			fmt.Fprintf(&m, "%s}\n", ind)
		}
		fmt.Fprintf(&m, "\tConsole.Println(\"g%d \\(acc%d)\")\n", j, j)
	}
	defect = seed%2 == 0
	if defect {
		// A defect in a tabulated function leaves its entry empty: the
		// call before fills another entry, the defect's call raises it.
		s.WriteString("func boom(x E0) int {\n\tlet z = []int{1}\n\tmatch x {\n\tcase V0x0:\n\t\tz[3]\n\tcase _:\n\t\t1\n\t}\n}\n\n")
		m.WriteString("\tConsole.Println(\"boom \\(boom(V0x1))\")\n\tConsole.Println(\"boom \\(boom(V0x0))\")\n")
		reached["defect"] = true
	}
	m.WriteString("}\n")
	s.WriteString(m.String())
	return s.String(), g.eligible, defect
}

// TestTabulatedAgree is the seeded property of tabulated decisions
// (tabulate.go): each seed writes functions of one or two parameters over
// enums, bools, bytes and ints, deciding or not, with results of every
// kind, and a main that calls each twice on each argument of its domain
// (a slice result is written by its caller between the calls), and on
// even seeds ends in a defect raised inside a tabulated function. The
// lowering tables only functions the generator's model admits (finite
// parameters, a decision, a result with no reference), at least one per
// seed, knownStats.Tabulated counts them, the reference (tabLowering
// "call") tables none, and the two programs print the same, exit alike
// and report a defect alike. A failure prints the seed, the Ergo source
// and both outputs.
// Replay: go test -run '^TestTabulatedAgree$' ./internal/sketch/lower -args -tab.seed=N
//
// It builds Go because the question is what the tables return at run
// time (the first call's fill, the second's read, the defect); no
// in-process check observes it, and Ergo has no reference interpreter yet
// (test-speed T8).
func TestTabulatedAgree(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4, 5, 6}
	if *tabSeed != 0 {
		seeds = []uint64{*tabSeed}
	}
	reached := map[string]bool{}
	var programs []*lowered
	sources := map[uint64]string{}
	defects := map[uint64]bool{}
	for _, seed := range seeds {
		src, eligible, defect := tabProgram(seed, reached)
		sources[seed], defects[seed] = src, defect
		for _, mode := range []string{"", "call"} {
			var made int
			saved, savedHook := tabLowering, statsHook
			tabLowering = mode
			statsHook = func(s knownStats) { made = s.Tabulated }
			p := mustLower(t, fmt.Sprintf("tabulated%d%s", seed, mode), src)
			tabLowering, statsHook = saved, savedHook
			var tabled []int
			var all int
			for _, f := range p.files {
				all += strings.Count(string(f.Data), ".MemoFlag\n")
				for _, m := range tabFlags.FindAllSubmatch(f.Data, -1) {
					var j int
					fmt.Sscan(string(m[1]), &j)
					tabled = append(tabled, j)
				}
			}
			if made != all {
				t.Errorf("seed %d, lowering %q: knownStats.Tabulated %d, %d tables written", seed, mode, made, all)
			}
			if mode == "call" {
				if all != 0 {
					t.Errorf("seed %d: the reference writes %d tables", seed, all)
				}
			} else {
				if len(tabled) == 0 {
					t.Errorf("seed %d: no generated function is tabulated\n%s", seed, src)
				}
				for _, j := range tabled {
					if !eligible[j] {
						t.Errorf("seed %d: g%d is tabulated, which the model refuses\n%s", seed, j, src)
						continue
					}
					for _, k := range strings.Split(strings.Join(tabParamsOf(src, j), ","), ",") {
						reached["tabulated "+k] = true
					}
				}
			}
			programs = append(programs, p)
		}
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the tabulated programs do not build (seeds %v)", seeds)
	}
	for _, seed := range seeds {
		on, onErr, onCode := runExe(t, exes[fmt.Sprintf("tabulated%d", seed)])
		off, offErr, offCode := runExe(t, exes[fmt.Sprintf("tabulated%dcall", seed)])
		ended := (onCode != 0) == defects[seed] && strings.Contains(on, "g0 ")
		if on != off || onErr != offErr || onCode != offCode || !ended {
			t.Errorf("seed %d: the tables and the reference differ (or the program did not end as written)\nReplay: go test -run '^TestTabulatedAgree$' ./internal/sketch/lower -args -tab.seed=%d\n%s\n--- tables (exit %d)\n%s%s\n--- reference (exit %d)\n%s%s", seed, seed, sources[seed], onCode, on, onErr, offCode, off, offErr)
		}
	}
	if *tabSeed == 0 {
		var missing []string
		for _, k := range []string{"tabulated E0", "tabulated bool", "tabulated byte", "no decision", "int parameter", "slice result", "call of an earlier function", "defect"} {
			if !reached[k] {
				missing = append(missing, k)
			}
		}
		if len(missing) > 0 {
			t.Errorf("the fixed seeds do not reach %v", missing)
		}
	}
}

// tabParamsOf is gJ's parameter kinds, read from its declaration in src.
func tabParamsOf(src string, j int) []string {
	head := fmt.Sprintf("func g%d(", j)
	i := strings.Index(src, head)
	if i < 0 {
		return nil
	}
	rest := src[i+len(head):]
	rest = rest[:strings.Index(rest, ")")]
	var out []string
	for _, p := range strings.Split(rest, ", ") {
		if f := strings.Fields(p); len(f) == 2 {
			out = append(out, f[1])
		}
	}
	return out
}
