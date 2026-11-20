package irspike

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// The seeded property of the sink rule (CONSTRAINTS C4; lower/ir_sink.go):
// an argument that a guarded-return callee reads on one branch only is
// evaluated on that branch when it is pure, total and cannot stop. Each
// seed writes one program of callers, one per site, each a call of a
// guarded-return helper (pick reads next on its final value, keep reads
// its second argument on its guarded return and its third on both, mix
// its second on both and its third on its guarded return) whose
// arguments are drawn with and without each fact: a total call, a basic
// operation, a call that loops, an index that may be out of range, a
// division that may be by zero, a call with an effect (it prints), a call
// that can fail (under try) and a field read through a pointer. Some sites
// raise a defect when every argument runs. The program is built by the IR
// with the rule, by the IR without it (sinkLowering "off") and by the
// source path; all three print the same and exit alike, so no defect,
// print or failure moves or disappears. The IR is checked too: a site
// whose sunk argument and every later one are admitted has no call of
// the helper left, and any other site still calls it; across the seeds
// both are reached. A failing seed prints the Ergo source, the outputs
// and its replay command.
//
// It builds Go because the question is what the built program does when
// an argument is evaluated later or not at all; no in-process check runs
// it.
//
// Mutants it alone catches (lower testdata/mutants.json):
// c4-sink-impure-arg (a call with an effect is sunk), c4-sink-defect-arg
// (an index is sunk), c4-sink-both-branches (mix's c, read on both
// branches, is sunk).

func TestSunkArgumentsAgree(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	reached := map[string]bool{}
	for seed := first; seed < first+count; seed++ {
		text, sunk := generateSinks(seed)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestSunkArgumentsAgree ./internal/experimental/irspike"
		dir := t.TempDir()
		file := filepath.Join(dir, "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("the program does not check: %v\n%s", err, text)
		}
		outs := map[string]outcome{}
		for _, on := range []bool{true, false} {
			name := map[bool]string{true: "sink", false: "eager"}[on]
			restore := lower.SetSinkLowering(on)
			d := filepath.Join(dir, name)
			_, err := Emit(u, IR, d)
			restore()
			if err != nil {
				t.Fatalf("seed %d %s: %v\n%s", seed, name, err, text)
			}
			if on {
				for site, want := range sinkSites(t, filepath.Join(d, lower.IRFileName), len(sunk)) {
					if want != sunk[site] {
						t.Errorf("seed %d: site%d sunk %v, want %v\n%s\n%s", seed, site, want, sunk[site], replay, text)
					}
					reached[fmt.Sprint(want)] = true
				}
			}
			if _, err := Build(ir, IR, d, d+".bin", BuildOptions{}); err != nil {
				t.Fatalf("seed %d %s: %v\n%s", seed, name, err, text)
			}
			outs[name] = run(t, d+".bin")
		}
		d := filepath.Join(dir, "source")
		if _, err := Emit(u, Source, d); err != nil {
			t.Fatal(err)
		}
		if _, err := Build(src, Source, d, d+".bin", BuildOptions{}); err != nil {
			t.Fatalf("seed %d source: %v\n%s", seed, err, text)
		}
		outs["source"] = run(t, d+".bin")
		for _, m := range []string{"eager", "source"} {
			if outs[m] != outs["sink"] {
				t.Errorf("seed %d: the sunk build and %s differ\nsink: %+v\n%s: %+v\n%s\n%s", seed, m, outs["sink"], m, outs[m], replay, text)
			}
		}
	}
	if count >= 4 && (!reached["true"] || !reached["false"]) {
		t.Errorf("the seeds reached sunk sites %v and kept sites %v, want both", reached["true"], reached["false"])
	}
}

// sinkSites reports, per site function site0…site{n-1} of the IR file,
// whether its call of the helper is gone (sunk).
func sinkSites(t *testing.T, file string, n int) map[int]bool {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Funcs []struct {
			Name string `json:"name"`
			Body any    `json:"body"`
		} `json:"funcs"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	// A site the IR does not take (its Go text is the emitter's, eager)
	// has no entry.
	out := map[int]bool{}
	for _, f := range v.Funcs {
		var i int
		if _, err := fmt.Sscanf(f.Name, "site%d", &i); err != nil || i >= n || f.Name != "site"+fmt.Sprint(i) {
			continue
		}
		out[i] = !callsNamed(f.Body, "pick") && !callsNamed(f.Body, "keep") && !callsNamed(f.Body, "mix")
	}
	return out
}

// sinkArg is one argument form and whether the rule may sink it.
type sinkArg struct {
	text string
	ok   bool
}

// generateSinks is the program of seed (TestSunkArgumentsAgree) and, per
// site, whether the rule sinks its argument.
func generateSinks(seed uint64) (string, []bool) {
	r := rand.New(rand.NewPCG(seed, 0x51c4))
	var b strings.Builder
	b.WriteString(`type Oops {}

type Box { V int }

func pick(first int, next int) int {
	if first != 0 {
		return first
	}
	next
}

// keep reads b on its guarded return and c on both branches.
func keep(a int, b int, c int) int {
	if a > 0 {
		return b + c
	}
	c - a
}

// mix reads c on both branches and b on its guarded return; c comes
// first.
func mix(a int, c int, b int) int {
	if a < 0 {
		return c - b
	}
	c + a
}

func sign(a int, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	0
}

func spin(n int) int {
	let mut s = 0
	for i in 0..n {
		s += i
	}
	s
}

func at(i int) int { []int{1, 2}[i] }

func div(a int, b int) int { a / b }

func loud(n int) int needs Console {
	Console.Println("loud \(n)")
	n
}

func risky(n int) int fails Oops {
	if n < 0 {
		return Oops
	}
	n
}

`)
	args := func(x, y int) []sinkArg {
		return []sinkArg{
			{fmt.Sprintf("sign(%d, %d)", x, y), true},
			{fmt.Sprintf("x + (%d)", y), true},
			{fmt.Sprintf("spin(%d)", y), false},
			{fmt.Sprintf("at(x + (%d))", y), false},
			{fmt.Sprintf("div(%d, %d)", x, y%3), false},
			{fmt.Sprintf("loud(%d)", y), false},
			{fmt.Sprintf("try risky(%d)", y-2), false},
			{"p.V", false},
		}
	}
	var sunk []bool
	var main strings.Builder
	n := 6 + r.IntN(5)
	for i := range n {
		x, y := r.IntN(5)-2, r.IntN(5)-2
		as := args(x, y)
		first := fmt.Sprintf("sign(x, %d)", r.IntN(3)-1)
		var call string
		var ok bool
		switch r.IntN(4) {
		case 0, 1:
			a := as[r.IntN(len(as))]
			call, ok = "pick("+first+", "+a.text+")", a.ok
		case 2:
			// keep: b (second) sinks only when c (third) is admitted too.
			a, c := as[r.IntN(len(as))], as[r.IntN(len(as))]
			call, ok = "keep(x, "+a.text+", "+c.text+")", a.ok && c.ok
		default:
			// mix: c (second, read on both branches) stays; b (third) sinks.
			c, a := as[r.IntN(len(as))], as[r.IntN(len(as))]
			call, ok = "mix(x, "+c.text+", "+a.text+")", a.ok
		}
		sunk = append(sunk, ok)
		fmt.Fprintf(&b, "func site%d(x int, p *Box) int fails Oops needs Console { %s }\n\n", i, call)
		fmt.Fprintf(&main, "\tlet v%d = try site%d(%d, &box)\n\tConsole.Println(\"site%d \\(v%d)\")\n", i, i, x, i, i)
	}
	b.WriteString("func run() fails Oops needs Console {\n\tlet mut box = Box{V: 3}\n")
	b.WriteString(main.String())
	b.WriteString("}\n\nfunc main() needs Console {\n\trun() catch {\n\tcase Oops:\n\t\tConsole.Println(\"oops\")\n\t}\n}\n")
	return b.String(), sunk
}
