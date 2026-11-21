package lower

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The seeded property of comparison chains (chainswitch.go). Each seed
// writes a function f(c byte, e byte, d int) int whose statements are runs
// of `if c == K { … leaves … }` broken by every guard of the rule: an arm
// on the other value e, an arm on a let mut copy of c, an arm that falls
// through (no leave), an arm on a non-constant (c == e, or a local let),
// a duplicated K, an arm with an else, a condition that calls a function,
// and, inside a loop, an arm whose body breaks. Arms reach K == c, a
// package let as K, continue and a leaving if/else. A model of the rule
// (chainModel) counts the runs that become a switch.
//
// main calls f on every byte value of c (the law: exhaustive over the
// 256 bytes), with e and d derived from it, and prints each result. The
// program is lowered by the default lowering and by the reference
// (chainLowering "if"); both must print the same lines. The default
// lowering's Go of f must hold as many switches on c or e as the model
// counts, the reference's none, and f's IR body (the generator writes
// only what the IR builder lowers) as many "switch" statements. A
// failing seed prints the Ergo source and its replay command.

var chainSeed = flag.Uint64("chain.seed", 0, "run TestChainSwitchAgrees on this seed alone")

// chainGuards are the generator's guard kinds; the fixed seeds reach each.
var chainGuards = []string{"other-value", "mut-copy", "falls-through", "non-constant", "duplicate", "else", "call", "break", "reversed", "continue", "nested-leave", "package-let", "local-let"}

// chainStmt is one generated statement, as the model sees it.
type chainStmt struct {
	subject string // "c" or "e" for an arm on an immutable byte; "" for no arm
	key     int
	arm     bool // leaves, no else, no break: an arm of a run
}

type chainGen struct {
	r       *rand.Rand
	b       strings.Builder
	stmts   []chainStmt // the statements of one list, in order
	lists   [][]chainStmt
	reached map[string]bool
	n       int
}

func (g *chainGen) line(depth int, s string) {
	g.b.WriteString(strings.Repeat("\t", depth) + s + "\n")
}

// key is a byte constant: from a small pool, so duplicates happen.
func (g *chainGen) key() int {
	return []int{0, 9, 10, 32, 40, 46, 48, 61, 97, 122, 200, 255}[g.r.IntN(12)]
}

// value is an arm's result, distinct per arm.
func (g *chainGen) value(k int) string {
	g.n++
	return fmt.Sprintf("%d*%d + d + acc", k, g.n)
}

// list writes one statement list of segments at depth; inLoop allows
// continue and break arms.
func (g *chainGen) list(depth int, inLoop bool) {
	saved := g.stmts
	g.stmts = nil
	for range 3 + g.r.IntN(4) {
		// A run of plain arms.
		for range 1 + g.r.IntN(4) {
			g.arm(depth, inLoop)
		}
		g.guard(depth, inLoop)
	}
	g.lists = append(g.lists, g.stmts)
	g.stmts = saved
}

// arm writes a leaving arm on c.
func (g *chainGen) arm(depth int, inLoop bool) {
	k := g.key()
	cond := fmt.Sprintf("c == %d", k)
	switch g.r.IntN(8) {
	case 0, 1:
		cond = fmt.Sprintf("%d == c", k)
		g.reached["reversed"] = true
	case 2:
		k, cond = 46, "c == dot"
		g.reached["package-let"] = true
	}
	g.line(depth, "if "+cond+" {")
	switch {
	case inLoop && g.r.IntN(3) == 0:
		g.line(depth+1, "acc += "+fmt.Sprint(k+1))
		g.line(depth+1, "continue")
		g.reached["continue"] = true
	case g.r.IntN(4) == 0:
		g.line(depth+1, "if d > 300 {")
		g.line(depth+2, "return "+g.value(k))
		g.line(depth+1, "} else {")
		g.line(depth+2, "return "+g.value(k))
		g.line(depth+1, "}")
		g.reached["nested-leave"] = true
	default:
		g.line(depth+1, "return "+g.value(k))
	}
	g.line(depth, "}")
	g.stmts = append(g.stmts, chainStmt{subject: "c", key: k, arm: true})
}

// guard writes one statement that breaks a run, or none.
func (g *chainGen) guard(depth int, inLoop bool) {
	k := g.key()
	kinds := []string{"other-value", "mut-copy", "falls-through", "non-constant", "local-let", "duplicate", "else", "call", "none"}
	if inLoop {
		kinds = append(kinds, "break")
	}
	kind := kinds[g.r.IntN(len(kinds))]
	g.reached[kind] = true
	switch kind {
	case "other-value":
		g.line(depth, fmt.Sprintf("if e == %d {", k))
		g.line(depth+1, "return "+g.value(k))
		g.line(depth, "}")
		g.stmts = append(g.stmts, chainStmt{subject: "e", key: k, arm: true})
	case "mut-copy":
		// Two arms on m: they would make a run of their own if a let mut
		// subject joined.
		for _, k := range []int{k, (k + 1) % 256} {
			g.line(depth, fmt.Sprintf("if m == %d {", k))
			g.line(depth+1, "return "+g.value(k))
			g.line(depth, "}")
			g.stmts = append(g.stmts, chainStmt{})
		}
	case "falls-through":
		g.line(depth, fmt.Sprintf("if c == %d {", k))
		g.line(depth+1, "acc += 3")
		g.line(depth, "}")
		g.stmts = append(g.stmts, chainStmt{})
	case "non-constant":
		g.line(depth, "if c == e {")
		g.line(depth+1, "return "+g.value(1))
		g.line(depth, "}")
		g.stmts = append(g.stmts, chainStmt{})
	case "local-let":
		// A local let is a Go variable, not a constant (only a package
		// let lowers as a Go const, forms.go): it ends the run.
		g.line(depth, "if colon == c {")
		g.line(depth+1, "return "+g.value(58))
		g.line(depth, "}")
		g.stmts = append(g.stmts, chainStmt{})
	case "duplicate":
		// The key of the last arm on c, again: it ends that run and may
		// start the next.
		for i := len(g.stmts) - 1; i >= 0; i-- {
			if g.stmts[i].subject == "c" {
				k = g.stmts[i].key
				break
			}
		}
		g.line(depth, fmt.Sprintf("if c == %d {", k))
		g.line(depth+1, "return "+g.value(k))
		g.line(depth, "}")
		g.stmts = append(g.stmts, chainStmt{subject: "c", key: k, arm: true})
	case "else":
		g.line(depth, fmt.Sprintf("if c == %d {", k))
		g.line(depth+1, "return "+g.value(k))
		g.line(depth, "} else {")
		g.line(depth+1, "acc += 1")
		g.line(depth, "}")
		g.stmts = append(g.stmts, chainStmt{})
	case "call":
		g.line(depth, fmt.Sprintf("if same(c) == %d {", k))
		g.line(depth+1, "return "+g.value(k))
		g.line(depth, "}")
		g.stmts = append(g.stmts, chainStmt{})
	case "break":
		g.line(depth, fmt.Sprintf("if c == %d {", k))
		g.line(depth+1, "if d > 100 {")
		g.line(depth+2, "break")
		g.line(depth+1, "}")
		g.line(depth+1, "return "+g.value(k))
		g.line(depth, "}")
		g.stmts = append(g.stmts, chainStmt{})
	}
}

// chainCount is what a lowering of f holds: its chain switches and their
// cases.
type chainCount struct{ switches, cases int }

// chainModel is the switches the rule makes of one list, and their cases:
// from each statement, the longest run of arms on one subject with
// distinct keys; a run of fewer than chainMinArms arms moves on by one.
func chainModel(list []chainStmt) chainCount {
	var runs chainCount
	for i := 0; i < len(list); {
		n := 0
		seen := map[int]bool{}
		for j := i; j < len(list); j++ {
			s := list[j]
			if !s.arm || s.subject != list[i].subject || seen[s.key] {
				break
			}
			seen[s.key] = true
			n++
		}
		if n >= chainMinArms {
			runs.switches++
			runs.cases += n
			i += n
		} else {
			i++
		}
	}
	return runs
}

// chainProgram is one seed's program and the model's count of switches.
func chainProgram(seed uint64, reached map[string]bool) (string, chainCount) {
	g := &chainGen{r: rand.New(rand.NewPCG(seed, 0x43484e)), reached: reached}
	g.line(1, "let colon byte = 58")
	g.line(1, "let mut acc = 0")
	g.line(1, "let mut m = c")
	g.line(1, "m = m + 0")
	g.list(1, false)
	g.line(1, "let mut i = 0")
	g.line(1, "for i < 2 {")
	g.line(2, "i += 1")
	g.line(2, "acc += i")
	g.list(2, true)
	g.line(1, "}")
	g.list(1, false)
	g.line(1, "acc + d")
	var want chainCount
	for _, l := range g.lists {
		m := chainModel(l)
		want.switches += m.switches
		want.cases += m.cases
	}
	src := "let dot byte = 46\n\nfunc same(c byte) byte { c }\n\nfunc f(c byte, e byte, d int) int {\n" + g.b.String() + "}\n\n" +
		"func main() needs Console {\n\tfor v in 0..256 {\n\t\tlet c = byte(v)\n\t\tlet e = byte((v * 7) % 256)\n" +
		"\t\tConsole.Println(\"\\(v) \\(f(c, e, v)) \\(f(c, c, 400)) \\(f(c, e, 150))\")\n\t}\n}\n"
	return src, want
}

// chainLower lowers src by the default lowering or the reference.
func chainLower(t *testing.T, name, src string, ref bool) *lowered {
	t.Helper()
	saved := chainLowering
	defer func() { chainLowering = saved }()
	chainLowering = ""
	if ref {
		chainLowering = "if"
	}
	return mustLower(t, name, src)
}

// chainSwitches counts the Go switches in f's Go body and their cases (f
// has no switch but a chain's).
func chainSwitches(t *testing.T, p *lowered) chainCount {
	body := goFunc(t, p.mainGo(), "f")
	return chainCount{
		switches: len(regexp.MustCompile(`(?m)^\s*switch `).FindAllString(body, -1)),
		cases:    len(regexp.MustCompile(`(?m)^\s*case `).FindAllString(body, -1)),
	}
}

// chainIRSwitches counts the "switch" statements in f's IR body and their
// cases; switches is -1 when f keeps its source body.
func chainIRSwitches(t *testing.T, src string) chainCount {
	t.Helper()
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("%v", diags)
	}
	saved := chainLowering
	defer func() { chainLowering = saved }()
	chainLowering = ""
	files, _, err := LowerIR(Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Path != IRFileName {
			continue
		}
		var ir irFile
		if err := json.Unmarshal(f.Data, &ir); err != nil {
			t.Fatal(err)
		}
		for _, fn := range ir.Funcs {
			if fn.Name == "f" {
				var n chainCount
				irCountSwitches(fn.Body, &n)
				return n
			}
		}
	}
	return chainCount{switches: -1}
}

func irCountSwitches(list []*irStmt, n *chainCount) {
	for _, s := range list {
		if s == nil {
			continue
		}
		if s.K == "switch" {
			n.switches++
			n.cases += len(s.Cases)
		}
		irCountSwitches(s.Then, n)
		irCountSwitches(s.Else, n)
		irCountSwitches(s.Body, n)
		for _, c := range s.Cases {
			irCountSwitches(c.Body, n)
		}
	}
}

func chainSeeds() []uint64 {
	if *chainSeed != 0 {
		return []uint64{*chainSeed}
	}
	return []uint64{1, 2, 3, 4, 5, 6, 7, 8}
}

// TestChainSwitchForms is the property's in-process half: on each seed,
// the default lowering's Go of f has the model's switches and cases and
// the reference's none; its Go type-checks (typecheckGo); and f's IR body
// has the model's "switch" statements and cases.
//
// Mutants (lower ledger): chain-else (an arm with an else joins),
// chain-falls-through (an arm that does not leave joins), chain-duplicate
// (a duplicated key joins: Go refuses the switch), chain-mut (a let mut
// subject joins), chain-break (an arm that breaks joins), chain-subject
// (an arm on another value joins the run), chain-min (a run of one arm is
// a switch), chain-package-let (a package let is no constant), chain-ir
// (the IR keeps the chain).
func TestChainSwitchForms(t *testing.T) {
	reached := map[string]bool{}
	var programs []*lowered
	for _, seed := range chainSeeds() {
		src, want := chainProgram(seed, reached)
		replay := fmt.Sprintf("Replay: go test -run '^TestChainSwitch' ./internal/sketch/lower -args -chain.seed=%d\n%s", seed, src)
		p := chainLower(t, fmt.Sprintf("ch%d", seed), src, false)
		ref := chainLower(t, fmt.Sprintf("ch%dref", seed), src, true)
		if got := chainSwitches(t, p); got != want {
			t.Errorf("seed %d: f's Go has %+v, the model %+v\n%s", seed, got, want, replay)
		}
		if got := chainSwitches(t, ref); got != (chainCount{}) {
			t.Errorf("seed %d: the reference's f has %+v\n%s", seed, got, replay)
		}
		if got := chainIRSwitches(t, src); got != want {
			t.Errorf("seed %d: f's IR has %+v, the model %+v\n%s", seed, got, want, replay)
		}
		programs = append(programs, p, ref)
	}
	typecheckGo(t, programs)
	if *chainSeed == 0 {
		for _, k := range chainGuards {
			if !reached[k] {
				t.Errorf("the fixed seeds never reach %q", k)
			}
		}
	}
}

// TestChainSwitchAgrees is the property's run: each seed's program under
// the default lowering and the reference prints the same line for every
// byte value of c.
//
// It builds Go because the question is what the switch computes against
// the chain, which only a run of both shows; go/types says only that each
// compiles.
func TestChainSwitchAgrees(t *testing.T) {
	needsBuild(t)
	var programs []*lowered
	for _, seed := range chainSeeds() {
		src, _ := chainProgram(seed, map[string]bool{})
		programs = append(programs, chainLower(t, fmt.Sprintf("ch%d", seed), src, false), chainLower(t, fmt.Sprintf("ch%dref", seed), src, true))
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatal("the chain programs do not build")
	}
	for _, seed := range chainSeeds() {
		name := fmt.Sprintf("ch%d", seed)
		out, stderr, code := runExe(t, exes[name])
		refOut, refErr, refCode := runExe(t, exes[name+"ref"])
		if out != refOut || stderr != refErr || code != refCode || strings.Count(out, "\n") != 256 {
			src, _ := chainProgram(seed, map[string]bool{})
			t.Errorf("seed %d: the switch and the chain differ\nReplay: go test -run '^TestChainSwitchAgrees$' ./internal/sketch/lower -args -chain.seed=%d\n%s\n--- switch (exit %d)\n%s%s\n--- chain (exit %d)\n%s%s", seed, seed, src, code, out, stderr, refCode, refOut, refErr)
		}
	}
}
