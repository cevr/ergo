package lower

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os/exec"
	"strings"
	"testing"
)

// TestStaticReleaseAgrees is P-L8's equality property (owner rule
// 2026-11-08: a speed lowering keeps the old one selectable and proves the
// two equal). Each seed of B1's generator (genOwnRun: acquisitions,
// lends, moves, explicit Close, a taking call, reassignment, temporaries,
// if/else, loops with break and continue, a failing try, defects in the
// body and in a Close, a stop at a generated point) is lowered twice: in
// the static release form (static.go) and in the list form (B1,
// releaseLowering "scope"). Both programs run, and their outputs and
// exits equal each other and the reference model's. The batch must reach
// each branch of the static form: a static frame, a value returned while
// owned (ReturnStatic, the kept set), a release inside the frame (a
// block's or an iteration's end) and a hand-off (HandStatic); a branch
// not reached fails the test by name, not by count. A failing seed prints
// the Ergo source, both outputs and the replay command (ERGO_SEED=n).
//
// It builds Go because its subject is the emitted release code on the
// return, failure, defect and stop edges of both forms: go/types cannot
// run them, and Ergo has no reference interpreter yet (test-speed T8).
//
// Mutants it catches that nothing else does (internal/sketch/lower
// mutants.json, P-L8): one per helper of the static form.
func TestStaticReleaseAgrees(t *testing.T) {
	needsBuild(t)
	first, n := hrSeeds(t, 40)
	type pair struct {
		seed               int
		src, want          string
		static, list, name string
	}
	var cases []pair
	var programs []*lowered
	reached := map[string]bool{}
	for seed := first; seed < first+n; seed++ {
		src, want := genOwnRun(seed)
		static := mustLower(t, fmt.Sprintf("static_%d", seed), src)
		prev := SetReleaseLoweringForTest("scope")
		list := mustLower(t, fmt.Sprintf("list_%d", seed), src)
		SetReleaseLoweringForTest(prev)
		if strings.Contains(list.mainGo(), "StaticBegin(") {
			t.Fatalf("seed %d: the list form wrote a static frame", seed)
		}
		g := static.mainGo()
		for _, branch := range []string{"StaticBegin(", "ReturnStatic(", "HandStatic(", "&^= 1"} {
			if strings.Contains(g, branch) {
				reached[branch] = true
			}
		}
		cases = append(cases, pair{seed: seed, src: src, want: want, static: static.name, list: list.name})
		programs = append(programs, static, list)
	}
	// IG9's generator adds nested blocks whose values end inside the
	// frame, over a Go handle (host_resource_test.go).
	for seed := first; seed < first+n; seed++ {
		g := &hrGen{r: rand.New(rand.NewPCG(uint64(seed), 197))}
		body := g.block(0, nil)
		src := hrSource(body, nil)
		want, _ := (&hrModel{keepAt: 3}).run(body)
		lower := func(name string) *lowered {
			o := lowerSourceWith(name, src, true, newHresImporter())
			if o.err != nil || len(o.diags) > 0 || o.panicked != nil || len(o.refused) > 0 {
				t.Fatalf("seed %d (ERGO_SEED=%d replays): does not lower: %v %v %v %+v\n%s", seed, seed, o.err, o.diags, o.panicked, o.refused, numbered(src))
			}
			o.extra = hresFiles()
			return o
		}
		static := lower(fmt.Sprintf("hstatic_%d", seed))
		prev := SetReleaseLoweringForTest("scope")
		list := lower(fmt.Sprintf("hlist_%d", seed))
		SetReleaseLoweringForTest(prev)
		g2 := static.mainGo()
		for _, branch := range []string{"StaticBegin(", "ReturnStatic(", "HandStatic(", "&^= 1"} {
			if strings.Contains(g2, branch) {
				reached[branch] = true
			}
		}
		cases = append(cases, pair{seed: seed, src: src, want: want, static: static.name, list: list.name})
		programs = append(programs, static, list)
	}
	// Smallest missed behavior found by the kept-return mutants: a
	// sibling's Close fails after the return value was handed off. Generate
	// the sibling stack and defects; the failed exit must close the kept
	// result too, rather than leaking it or returning it to the caller.
	for seed := first; seed < first+n; seed++ {
		r := rand.New(rand.NewPCG(uint64(seed), 198))
		var src, want strings.Builder
		src.WriteString("type R { ID int, Bad bool }\nfunc Close(r R) needs Console { Console.Println(\"close \\(r.ID)\"); if r.Bad { Unreachable(\"close failed\") } }\nfunc give() R needs Console {\nlet result = R{ID: 0, Bad: false}\n")
		count := 1 + r.IntN(5)
		failed := false
		for id := 1; id <= count; id++ {
			bad := r.IntN(2) == 0
			failed = failed || bad
			fmt.Fprintf(&src, "let cleanup%d = R{ID: %d, Bad: %t}\n", id, id, bad)
		}
		src.WriteString("return result\n}\nfunc main() needs Console { let value = give(); Console.Println(\"received\") }\n")
		for id := count; id > 0; id-- {
			fmt.Fprintf(&want, "close %d\n", id)
		}
		if !failed {
			want.WriteString("received\n")
		}
		want.WriteString("close 0\n")
		if failed {
			want.WriteString("defect\n")
		}
		static := mustLower(t, fmt.Sprintf("keptstatic_%d", seed), src.String())
		prev := SetReleaseLoweringForTest("scope")
		list := mustLower(t, fmt.Sprintf("keptlist_%d", seed), src.String())
		SetReleaseLoweringForTest(prev)
		cases = append(cases, pair{seed: seed, src: src.String(), want: want.String(), static: static.name, list: list.name})
		programs = append(programs, static, list)
	}
	for _, branch := range []string{"StaticBegin(", "ReturnStatic(", "HandStatic(", "&^= 1"} {
		if !reached[branch] && n > 1 {
			t.Errorf("the generated programs reach no %q of the static form; widen the seeds", branch)
		}
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		return
	}
	run := func(name string) string {
		cmd := exec.Command(exes[name])
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		got := stdout.String()
		if err := runProgram(t, cmd); err != nil {
			got = stdout.String() + "defect\n"
		} else {
			got = stdout.String()
		}
		return got
	}
	for _, c := range cases {
		s, l := run(c.static), run(c.list)
		if s != l || s != c.want {
			t.Errorf("seed %d (ERGO_SEED=%d replays)\n%s\nstatic:\n%s\nlist (B1):\n%s\nmodel:\n%s", c.seed, c.seed, numbered(c.src), s, l, c.want)
		}
	}
}
