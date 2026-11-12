package lower

import (
	"fmt"
	"go/constant"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The rewrite-law property (types.md, Laws 14). Each generated operation
// type is a machine over a two-bit state, and its provider keeps the state
// in a sync.Ref and is built by its constructor:
//
//	Add(n)  s = (s + n*C) & 3     Inc()   s = (s + D) & 3
//	Halve() s = s / 2             Clear() s = s &^ 1
//	Read()  s & 1                 Even()  s & 1 == 0
//
// so Read and Even see the low bit only, and a difference in the high bit
// shows after a Halve. The reference model decides a rewrite law by
// exhaustive search: the observational equivalence of the four states
// (partition refinement over every operation), every state (Add reaches
// each from the fresh one), every residue of its parameters mod 4 (Add
// sees n*C mod 4), and each injected site instance.

// rwOp is an operation of a generated law's side; arg is Add's argument,
// an expression over the law's parameters.
type rwOp struct {
	name string
	arg  rwExpr
}

// rwExpr is Add's argument: the sum of the named parameters, plus add, plus
// spot(k) when spot names k.
type rwExpr struct {
	params []string
	add    int64
	spot   string
}

func (e rwExpr) text() string {
	var parts []string
	parts = append(parts, e.params...)
	if e.spot != "" {
		parts = append(parts, "spot("+e.spot+")")
	}
	if e.add != 0 || len(parts) == 0 {
		parts = append(parts, strconv.FormatInt(e.add, 10))
	}
	return strings.Join(parts, " + ")
}

func (e rwExpr) eval(vals map[string]int64) int64 {
	v := e.add
	for _, p := range e.params {
		v += vals[p]
	}
	if e.spot != "" && vals[e.spot] == rwSpot {
		v++
	}
	return v
}

// rwSpot is the one argument at which spot is 1: no drawn case reaches it,
// so only an injected site instance refutes a law that uses it.
const rwSpot = 7700001

type rwLaw struct {
	name     string
	params   []string
	guard    string // "", "true" or "false": when Even() == guard
	lhs, rhs []rwOp
	instance bool // the test injects the site instance k = rwSpot
}

type rwType struct {
	name, prov string
	c, d       int64
	laws       []rwLaw
	// ctx is a named context that binds the type to the provider of the
	// type at index under (types.md Laws 10): each law of the type runs
	// under it too, on that provider. entry is the entry's form
	// (entryText).
	ctx          string
	under, entry int
}

func (o rwOp) text() string {
	if o.name == "Add" {
		return "Add(" + o.arg.text() + ")"
	}
	return o.name + "()"
}

func sideText(ops []rwOp) string {
	if len(ops) == 0 {
		return "{}"
	}
	parts := make([]string, len(ops))
	for i, o := range ops {
		parts[i] = o.text()
	}
	return "{ " + strings.Join(parts, "; ") + " }"
}

func (l rwLaw) text() string {
	ps := make([]string, len(l.params))
	for i, p := range l.params {
		ps[i] = p + " int"
	}
	s := "law " + l.name + "(" + strings.Join(ps, ", ") + ") "
	if l.guard != "" {
		s += "when Even() == " + l.guard + " "
	}
	return s + sideText(l.lhs) + " == " + sideText(l.rhs)
}

func (t rwType) source() string {
	var b strings.Builder
	fmt.Fprintf(&b, "type %s {\n\tAdd(n int),\n\tInc(),\n\tHalve(),\n\tClear(),\n\tRead() int,\n\tEven() bool,\n\n", t.name)
	for _, l := range t.laws {
		b.WriteString("\t" + l.text() + ",\n")
	}
	b.WriteString("}\n\n")
	p := t.prov
	fmt.Fprintf(&b, "type %s { s sync.Ref[int] }\n\n", p)
	fmt.Fprintf(&b, "func New%s() %s { %s{s: sync.NewRef[int](0)} }\n\n", p, p, p)
	fmt.Fprintf(&b, "func Add(p %s, n int) { p.s.Set((p.s.Get() + n*%d) & 3) }\n\n", p, t.c)
	fmt.Fprintf(&b, "func Inc(p %s) { p.s.Set((p.s.Get() + %d) & 3) }\n\n", p, t.d)
	fmt.Fprintf(&b, "func Halve(p %s) { p.s.Set(p.s.Get() / 2) }\n\n", p)
	fmt.Fprintf(&b, "func Clear(p %s) { p.s.Set(p.s.Get() &^ 1) }\n\n", p)
	fmt.Fprintf(&b, "func Read(p %s) int { p.s.Get() & 1 }\n\n", p)
	fmt.Fprintf(&b, "func Even(p %s) bool { p.s.Get()&1 == 0 }\n\n", p)
	return b.String()
}

// step is the reference transition of one operation.
func (t rwType) step(s int64, o rwOp, vals map[string]int64) int64 {
	switch o.name {
	case "Add":
		return (s + o.arg.eval(vals)*t.c) & 3
	case "Inc":
		return (s + t.d) & 3
	case "Halve":
		return s / 2
	case "Clear":
		return s &^ 1
	}
	return s
}

// equivalent is the observational equivalence of the four states: equal
// low bits after every sequence of operations.
func (t rwType) equivalent() [4][4]bool {
	var eq [4][4]bool
	for a := range 4 {
		for b := range 4 {
			eq[a][b] = a&1 == b&1
		}
	}
	moves := []rwOp{{name: "Inc"}, {name: "Halve"}, {name: "Clear"}}
	for k := range int64(4) {
		moves = append(moves, rwOp{name: "Add", arg: rwExpr{add: k}})
	}
	for changed := true; changed; {
		changed = false
		for a := range int64(4) {
			for b := range int64(4) {
				if !eq[a][b] {
					continue
				}
				for _, m := range moves {
					if !eq[t.step(a, m, nil)][t.step(b, m, nil)] {
						eq[a][b], changed = false, true
						break
					}
				}
			}
		}
	}
	return eq
}

// verdict is the reference verdict of the law l: "refuted" when a state
// the guard admits, a residue of each parameter (or the injected
// instance) and a continuation tell its sides apart, else "tested"; and
// the share of the searched points that refute it.
func (t rwType) verdict(l rwLaw) (string, float64) {
	eq := t.equivalent()
	var points []map[string]int64
	var rec func(i int, cur map[string]int64)
	rec = func(i int, cur map[string]int64) {
		if i == len(l.params) {
			c := map[string]int64{}
			for k, v := range cur {
				c[k] = v
			}
			points = append(points, c)
			return
		}
		for r := range int64(4) {
			cur[l.params[i]] = r + 4000
			rec(i+1, cur)
		}
	}
	rec(0, map[string]int64{})
	bad, total := 0, 0
	run := func(s int64, ops []rwOp, vals map[string]int64) int64 {
		for _, o := range ops {
			s = t.step(s, o, vals)
		}
		return s
	}
	for _, vals := range points {
		for s := range int64(4) {
			if l.guard != "" && (s&1 == 0) != (l.guard == "true") {
				continue
			}
			total++
			if !eq[run(s, l.lhs, vals)][run(s, l.rhs, vals)] {
				bad++
			}
		}
	}
	refuted := bad > 0
	if l.instance {
		vals := map[string]int64{"k": rwSpot}
		for s := range int64(4) {
			if !eq[run(s, l.lhs, vals)][run(s, l.rhs, vals)] {
				refuted = true
			}
		}
	}
	share := 0.0
	if total > 0 {
		share = float64(bad) / float64(total)
	}
	if refuted {
		return "refuted", share
	}
	return "tested", share
}

// simulate estimates, by 4000 simulated cases drawn as the runner draws
// them (a prefix and a continuation of zero to four of the six
// operations, residues for Add's arguments and the parameters), the share
// of cases the guard admits, and whether the runner's cases tell the
// sides apart often enough to refute the law surely: a share of at least
// a tenth of the drawn cases (0.9^100 is under 3e-5), or of a quarter of
// the site instance's cases (0.75^40 is under 1e-5).
func (t rwType) simulate(l rwLaw, rng *rand.Rand) (admit float64, detect bool) {
	steps := []string{"Add", "Inc", "Halve", "Clear", "Read", "Even"}
	seq := func(bound int) []rwOp {
		var out []rwOp
		for range rng.Intn(bound) {
			o := rwOp{name: steps[rng.Intn(len(steps))]}
			if o.name == "Add" {
				o.arg = rwExpr{add: int64(rng.Intn(4))}
			}
			out = append(out, o)
		}
		return out
	}
	differs := func(vals map[string]int64) (bool, bool) {
		pre, cont := seq(rewritePrefix), seq(rewriteSteps)
		s := int64(0)
		for _, o := range pre {
			s = t.step(s, o, nil)
		}
		if l.guard != "" && (s&1 == 0) != (l.guard == "true") {
			return false, false
		}
		a, b := s, s
		for _, o := range l.lhs {
			a = t.step(a, o, vals)
		}
		for _, o := range l.rhs {
			b = t.step(b, o, vals)
		}
		for _, o := range cont {
			if (o.name == "Read" || o.name == "Even") && a&1 != b&1 {
				return true, true
			}
			a, b = t.step(a, o, nil), t.step(b, o, nil)
		}
		return true, a&1 != b&1
	}
	const n = 4000
	admitted, drawn, spot := 0, 0, 0
	for range n {
		vals := map[string]int64{}
		for _, p := range l.params {
			vals[p] = int64(rng.Intn(4)) + 4000
		}
		ok, d := differs(vals)
		if ok {
			admitted++
		}
		if d {
			drawn++
		}
		if l.instance {
			if _, d := differs(map[string]int64{"k": rwSpot}); d {
				spot++
			}
		}
	}
	return float64(admitted) / n, float64(drawn)/n >= 0.1 || float64(spot)/n >= 0.25
}

// rwName is a fresh name: the runner reads no name (identity-blind).
func rwName(rng *rand.Rand, prefix string) string {
	const letters = "bcdfghjklmnpqrstvwxz"
	b := []byte(prefix)
	for range 5 {
		b = append(b, letters[rng.Intn(len(letters))])
	}
	return string(b)
}

// randomLaw draws a law of one to three left calls whose right side is
// drawn too; an unguarded law of one left call unfolds only into
// operations after its own in the order Add, Inc, Halve, Clear, so no
// unfold cycles.
func randomLaw(rng *rand.Rand, name string) rwLaw {
	order := []string{"Add", "Inc", "Halve", "Clear"}
	l := rwLaw{name: name}
	if rng.Intn(3) == 0 {
		l.guard = []string{"true", "false"}[rng.Intn(2)]
	}
	n := 1 + rng.Intn(3)
	for range n {
		o := rwOp{name: order[rng.Intn(4)]}
		if o.name == "Add" {
			p := string(rune('a' + len(l.params)))
			l.params = append(l.params, p)
			o.arg = rwExpr{params: []string{p}}
		}
		l.lhs = append(l.lhs, o)
	}
	lowest := 0
	if n == 1 && l.guard == "" {
		for i, o := range order {
			if o == l.lhs[0].name {
				lowest = i + 1
			}
		}
	}
	bare := map[string]bool{}
	for range rng.Intn(4) {
		if lowest >= len(order) {
			break
		}
		o := rwOp{name: order[lowest+rng.Intn(len(order)-lowest)]}
		if o.name == "Add" {
			// A computed argument: some of the parameters (each
			// then a constant at a site) and a constant.
			for _, p := range l.params {
				if rng.Intn(2) == 0 {
					o.arg.params = append(o.arg.params, p)
				}
			}
			o.arg.add = int64(rng.Intn(3))
			if len(o.arg.params) == 1 && o.arg.add == 0 {
				// A bare parameter passes through, once (linear):
				// a second is computed, + 4 adding nothing mod 4.
				if p := o.arg.params[0]; bare[p] {
					o.arg.add = 4
				} else {
					bare[p] = true
				}
			}
		}
		l.rhs = append(l.rhs, o)
	}
	return l
}

// genRwTypes draws the program's operation types: each has the anchor
// laws that pin the runner's rules, then random laws.
func genRwTypes(rng *rand.Rand) []rwType {
	var out []rwType
	for range 6 {
		out = append(out, rwType{name: rwName(rng, "T"), prov: rwName(rng, "P"), c: 1 + int64(rng.Intn(3)), d: 1 + int64(rng.Intn(3))})
	}
	for i := range out {
		t := &out[i]
		late := int64(2)
		if t.c%2 == 0 {
			late = 1
		}
		add := func(ps ...string) rwOp { return rwOp{name: "Add", arg: rwExpr{params: ps}} }
		t.laws = []rwLaw{
			// True: Add joins.
			{name: rwName(rng, "J"), params: []string{"a", "b"}, lhs: []rwOp{add("a"), add("b")}, rhs: []rwOp{{name: "Add", arg: rwExpr{params: []string{"a", "b"}}}}},
			// False only in the high bit: a continuation's Halve shows it.
			{name: rwName(rng, "L"), params: []string{"a", "b"}, lhs: []rwOp{add("a"), add("b")}, rhs: []rwOp{{name: "Add", arg: rwExpr{params: []string{"a", "b"}, add: late}}}},
			// True only under its guard: Clear is the identity on even states.
			{name: rwName(rng, "G"), guard: "true", lhs: []rwOp{{name: "Clear"}}},
			// False, but true at the fresh state 0: a prefix shows it at 2.
			{name: rwName(rng, "F"), guard: "true", lhs: []rwOp{{name: "Halve"}}},
			// False at the one constant k = rwSpot: only a site instance
			// refutes it, and half the types inject one.
			{name: rwName(rng, "S"), params: []string{"k"}, lhs: []rwOp{add("k"), {name: "Inc"}}, rhs: []rwOp{{name: "Add", arg: rwExpr{params: []string{"k"}, spot: "k"}}, {name: "Inc"}}, instance: i%2 == 0},
			// A parameter computed once is a constant, so it may pass
			// bare more than once: the checker admits the law.
			{name: rwName(rng, "C"), params: []string{"a"}, lhs: []rwOp{add("a"), {name: "Inc"}}, rhs: []rwOp{{name: "Add", arg: rwExpr{params: []string{"a"}, add: 4}}, add("a"), add("a"), {name: "Inc"}}},
		}
		for range 2 {
			t.laws = append(t.laws, randomLaw(rng, rwName(rng, "R")))
		}
	}
	for i := range out {
		out[i].ctx, out[i].under, out[i].entry = rwName(rng, "X"), rng.Intn(len(out)), i%2
	}
	return out
}

// entryText is the context's entry for t: a call of the provider's
// constructor, or a package let that holds one provider for every build.
// The second does not run: its two sides would share the provider
// (rw-context-shared-runs).
func (t rwType) entryText(types []rwType) string {
	if t.entry == 1 {
		return "shared" + t.ctx
	}
	return "New" + types[t.under].prov + "()"
}

func rwProgram(types []rwType) string {
	var b strings.Builder
	b.WriteString("import sync\n\nfunc spot(k int) int {\n\tif k == " + strconv.Itoa(rwSpot) + " {\n\t\treturn 1\n\t}\n\t0\n}\n\n")
	for _, t := range types {
		b.WriteString(t.source())
	}
	for _, t := range types {
		if t.entry == 1 {
			fmt.Fprintf(&b, "let shared%s = New%s()\n\n", t.ctx, types[t.under].prov)
		}
		fmt.Fprintf(&b, "let %s = context { %s: %s }\n\n", t.ctx, t.name, t.entryText(types))
	}
	b.WriteString("func main() needs Console {\n\tConsole.Println(\"x\")\n}\n")
	return b.String()
}

// TestRewriteLawVerdicts: the runner's verdict on each generated rewrite
// law equals the reference model's: tested when its sides read alike from
// every state the guard admits, refuted otherwise, and refuted by an
// injected site instance at the one constant where it is false. The anchor
// laws of each type pin the continuation, the drawn prefix, the guard and
// the site instances; the random laws reach the rest. Each type has a
// named context that binds it to a drawn provider, so each law also runs
// under the context, and its verdict there is that provider's
// (mutant rw-context-calls-law-func: a rewrite law under a context calls
// a law function that does not exist, and the build fails), unless the
// context's entry is a let that holds one provider for both sides, which
// is a skip (rw-context-shared-runs: the sides share it, and a true law is
// refuted).
// ERGO_REWRITE_SEED replays a seed.
//
// It builds Go because a rewrite law runs only in the built test binary:
// its providers keep their state in a sync.Ref, a Go body that check-time
// evaluation does not run.
func TestRewriteLawVerdicts(t *testing.T) {
	needsBuild(t)
	seed := int64(1)
	if s := os.Getenv("ERGO_REWRITE_SEED"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = v
	}
	rng := rand.New(rand.NewSource(seed))
	types := genRwTypes(rng)
	src := rwProgram(types)
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("seed %d: %v\n%s", seed, err, src)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("seed %d: %v\n%s", seed, diags, src)
	}
	var insts []RewriteInstance
	for _, law := range pkg.Laws {
		for _, ty := range types {
			for _, l := range ty.laws {
				if l.instance && law.Owner != nil && law.Owner.Name == ty.name && law.Name == l.name {
					insts = append(insts, RewriteInstance{Law: law, Args: []constant.Value{constant.MakeInt64(rwSpot)}})
				}
			}
		}
	}
	u := Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}, Instances: insts}
	files, err := LowerTests(u)
	if err != nil {
		t.Fatalf("seed %d: %v\n%s", seed, err, src)
	}
	exes := buildAll(t, []*lowered{{name: "rewrite_laws", src: src, file: file, pkg: pkg, files: files}}, true)
	if exes == nil {
		return
	}
	stdout, stderr, _ := runExeArgs(t, exes["rewrite_laws"], "--seed", "7")
	got := map[string]string{}
	for _, line := range verdicts(stdout) {
		for _, kind := range []struct{ prefix, verdict string }{{"ok law ", "tested"}, {"FAIL law ", "refuted"}, {"skip law ", "skipped"}} {
			if rest, ok := strings.CutPrefix(line, kind.prefix); ok {
				sep := " for "
				if !strings.Contains(strings.SplitN(rest, " (", 2)[0], sep) {
					sep = " under "
				}
				title, prov, _ := strings.Cut(rest, sep)
				prov, _, _ = strings.Cut(prov, " ")
				prov, _, _ = strings.Cut(prov, ":")
				got[title+sep+prov] = kind.verdict
			}
		}
	}
	// Every provider implements every type, so each law runs against
	// each: the provider's constants decide the reference verdict.
	// Each law also runs under its type's named context, on the provider
	// the context binds: the run's verdict is that provider's.
	for _, ty := range types {
		for _, l := range ty.laws {
			type run struct {
				title string
				p     rwType
			}
			var runs []run
			for _, p := range types {
				runs = append(runs, run{ty.name + "." + l.name + " for " + p.prov, p})
			}
			runs = append(runs, run{ty.name + "." + l.name + " under " + ty.ctx, types[ty.under]})
			for _, run := range runs {
				p := run.p
				prov := rwType{c: p.c, d: p.d}
				want, _ := prov.verdict(l)
				title := run.title
				if strings.Contains(title, " under ") && ty.entry == 1 {
					want = "skipped"
				}
				// A false law the runner's cases tell apart too rarely
				// (detects) may be tested; a guard its cases rarely admit
				// may be a skip. Never may a true law be refuted.
				admit, detect := prov.simulate(l, rng)
				fine := got[title] == want
				switch {
				case want == "tested" && admit < 0.1:
					fine = got[title] == "tested" || got[title] == "skipped"
				case want == "refuted" && !detect:
					fine = got[title] == "tested" || got[title] == "refuted" || got[title] == "skipped"
				}
				if !fine {
					t.Errorf("seed %d: law %s: the runner says %q, the reference %q\n  %s (provider C=%d D=%d)\nreplay: ERGO_REWRITE_SEED=%d go test -run TestRewriteLawVerdicts ./internal/sketch/lower\nsource:\n%s\nstdout:\n%s\nstderr:\n%s",
						seed, title, got[title], want, l.text(), p.c, p.d, seed, src, stdout, firstLines(stderr, 8))
					return
				}
			}
		}
	}
}
