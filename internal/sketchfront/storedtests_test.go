package sketchfront

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lawcache"
	"ergo.local/prototype/internal/stage"
)

// TestStoredVerdictsAgree is FT2's model (TESTSPEED plan §6 row 3): for a
// seeded program and a seeded sequence of edits (a callee's body, a
// caller's body, a test's body, a comment, the seed, the compiler),
// `ergo test`'s tier 1 with the verdict store gives the output and exit
// status of a run that evaluates every obligation (no store), once the
// marks are cut. After an edit that changes no token (a comment), every
// passed obligation is read from the store and only the failed ones run
// again; after a compiler edit (another compiler identity over the same
// store root), none is read. A failing seed prints its steps and both
// answers; replay with ERGO_STORED_SEED=N.
//
// It runs in process: the store is a temporary directory and the
// evaluator needs no build. Mutants only it kills (testdata/mutants.json):
// ft-reach-drops-callee, ft-key-omits-seed, ft-test-verdict-never-stored,
// ft-stored-law-form-dropped and ft-key-omits-compiler.
func TestStoredVerdictsAgree(t *testing.T) {
	first, last := uint64(1), uint64(6)
	if s := os.Getenv("ERGO_STORED_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, last = n, n
	}
	reached := map[string]bool{}
	for seed := first; seed <= last; seed++ {
		r := rand.New(rand.NewPCG(seed, 0xf72))
		p := storedProgram{k0: r.IntN(5), k1: 1 + r.IntN(3), e0: r.IntN(5), v: r.IntN(256) - 128, seed: 1 + uint64(r.IntN(3))}
		// On disk, so a compiler edit finds the old compiler's files.
		root, compilers := t.TempDir(), 0
		compiler := func() string { return fmt.Sprintf("sha256:stored-test-%d-%d", seed, compilers) }
		store := lawcache.Open(root, compiler())
		var steps []string
		for step := 0; step < 14; step++ {
			edit := "start"
			if step > 0 {
				switch r.IntN(6) {
				case 0:
					p.k0, edit = r.IntN(5), "the callee's body"
				case 1:
					p.k1, edit = 1+r.IntN(3), "the caller's body"
				case 2:
					p.e0, edit = r.IntN(5), "a test's body"
				case 3:
					p.comments, edit = p.comments+1, "a comment"
				case 4:
					p.seed, edit = 1+uint64(r.IntN(3)), "the seed"
				default:
					// Another ergo (its lowering, evaluator, std or runtime):
					// no verdict of the old one is read.
					compilers++
					store, edit = lawcache.Open(root, compiler()), "the compiler"
				}
			}
			steps = append(steps, edit)
			src := p.source()
			a := Check(src)
			if len(a.Diagnostics) > 0 {
				t.Fatalf("seed %d: %v\n%s", seed, a.Diagnostics, src)
			}
			want, wantOK := a.EvaluateTests(p.seed, 0, "/work/main.ergo", "main.ergo", nil, "")
			got, gotOK := a.EvaluateTests(p.seed, 0, "/work/main.ergo", "main.ergo", store, "")
			cut := func(s string) string {
				return strings.ReplaceAll(strings.ReplaceAll(s, EvaluatedMark+"\n", "\n"), StoredMark+"\n", "\n")
			}
			if gotOK != wantOK || cut(got.Stdout) != cut(want.Stdout) || got.Exit != want.Exit || got.Stderr != want.Stderr {
				t.Fatalf("seed %d: the stored answer is not a full evaluation's after %v\nsource:\n%s\nstored (ok %v, exit %d):\n%s\nfull (ok %v, exit %d):\n%s\nreplay: ERGO_STORED_SEED=%d go test -run '^TestStoredVerdictsAgree$' ./internal/sketchfront", seed, steps, src, gotOK, got.Exit, got.Stdout, wantOK, want.Exit, want.Stdout, seed)
			}
			if edit == "the compiler" {
				if got.Stored != 0 {
					t.Fatalf("seed %d: after %v (another compiler) %d verdicts were read from the old compiler's store\n%s", seed, steps, got.Stored, got.Stdout)
				}
				reached["compiler"] = true
			}
			if got.Stored > 0 {
				reached["stored"] = true
			}
			if strings.Contains(want.Stdout, "FAIL ") {
				reached["failed"] = true
			}
			if edit == "a comment" && gotOK {
				failed := 0
				for _, line := range strings.Split(want.Stdout, "\n") {
					if strings.HasPrefix(line, "FAIL ") && !strings.HasPrefix(line, "FAIL (") {
						failed++
					}
				}
				if got.Obligations != failed {
					t.Fatalf("seed %d: after %v (a comment, no token changed) %d obligations ran, want the %d failed ones\n%s", seed, steps, got.Obligations, failed, got.Stdout)
				}
				reached["comment"] = true
			}
		}
	}
	if first != last {
		for _, k := range []string{"stored", "failed", "comment", "compiler"} {
			if !reached[k] {
				t.Fatalf("the seeds never reached a %s answer (reached %v)", k, reached)
			}
		}
	}
}

// storedProgram is TestStoredVerdictsAgree's program: a callee, a caller
// of it, tests of each, a property whose verdict depends on the seed, and
// a law.
type storedProgram struct {
	k0, k1, e0, v, comments int
	seed                    uint64
}

func (p storedProgram) source() string {
	var b strings.Builder
	fmt.Fprintf(&b, "func base(n int) int { n + %d }\n\n", p.k0)
	fmt.Fprintf(&b, "func scaled(n int) int { base(n) * %d }\n\n", p.k1)
	for range p.comments {
		b.WriteString("// a note\n")
	}
	fmt.Fprintf(&b, "test Base { assert base(1) == %d }\n\n", 1+p.e0)
	b.WriteString("test Scaled { assert scaled(0) == 2 }\n\n")
	fmt.Fprintf(&b, "test Avoids(n int8) { assert int(n) != %d }\n\n", p.v)
	b.WriteString("law Shifts(n int) { base(n) - n == base(0) }\n")
	return b.String()
}

// TestRouteNoteSkipsEvaluation is FT2's route note, counted: a package
// whose test the evaluator leaves incomplete (its loop outruns the budget
// of steps) costs its steps once; the next answer of the same package
// spends none and sends it to the test binary (packages_routed); an edit
// of that test's body makes a new key, so the evaluator tries again.
//
// Mutants only it kills (testdata/mutants.json): ft-route-note-ignored and
// ft-route-key-omits-digest.
func TestRouteNoteSkipsEvaluation(t *testing.T) {
	store := lawcache.Memory("process:route-test")
	spin := func(n int) string {
		return "func spin(k int) int {\n\tlet mut n = 0\n\tfor n < k {\n\t\tn = n + 1\n\t}\n\tn\n}\n\ntest Quick { assert spin(3) == 3 }\n\ntest Spins { assert spin(" + strconv.Itoa(n) + ") == 1 }\n"
	}
	answer := func(src string) (steps, routed int64, ok bool) {
		t.Helper()
		a := Check(src)
		if len(a.Diagnostics) > 0 {
			t.Fatal(a.Diagnostics)
		}
		s0, r0 := stage.Get(stage.EvalSteps), stage.Get(stage.PackagesRouted)
		_, ok = a.EvaluateTests(1, 0, "/work/main.ergo", "main.ergo", store, "")
		return stage.Get(stage.EvalSteps) - s0, stage.Get(stage.PackagesRouted) - r0, ok
	}
	if steps, routed, ok := answer(spin(2000000)); ok || steps == 0 || routed != 0 {
		t.Fatalf("first answer: steps %d, routed %d, ok %v; want an undecided package that spent its steps", steps, routed, ok)
	}
	if steps, routed, ok := answer(spin(2000000)); ok || steps != 0 || routed != 1 {
		t.Fatalf("second answer: steps %d, routed %d, ok %v; want the package routed with no step", steps, routed, ok)
	}
	if steps, routed, ok := answer(spin(3000000)); ok || steps == 0 || routed != 0 {
		t.Fatalf("after an edit of the test: steps %d, routed %d, ok %v; want the evaluator to try again", steps, routed, ok)
	}
}

// TestNativeVerdictsKeyTheToolchain is the bug the orchestrator asked
// about (FT2 r1, r3): a law verdict the test binary wrote depends on the
// Go toolchain that linked it, so the build of another toolchain, and the
// evaluator under another pin or none, must not read it; the same
// toolchain does, and so does the evaluator under that pin.
//
// Mutants only it kills (testdata/mutants.json):
// ft-native-key-omits-toolchain and ft-check-ignores-pinned.
func TestNativeVerdictsKeyTheToolchain(t *testing.T) {
	a := Check("func base(n int) int { n + 1 }\n\nlaw Shifts(n int) { base(n) - n == base(0) }\n")
	if len(a.Diagnostics) > 0 {
		t.Fatal(a.Diagnostics)
	}
	// withA is a store that holds only the verdict go A's binary wrote.
	withA := func() *lawcache.Store {
		s := lawcache.Memory("process:toolchain-test")
		a.TestVerdicts(s, 1, 0, "go A").Record("ok law Shifts (100 cases)\nPASS (1 tests)\n")
		return s
	}
	store := withA()
	if a.TestVerdicts(store, 1, 0, "go A").CachedFile() == "" {
		t.Fatal("the toolchain that stored the verdict does not read it")
	}
	if text := a.TestVerdicts(store, 1, 0, "go B").CachedFile(); text != "" {
		t.Fatalf("another toolchain read the verdict: %q", text)
	}
	if got, ok := a.EvaluateTests(1, 0, "/work/main.ergo", "main.ergo", withA(), ""); !ok || got.Stored != 0 {
		t.Fatalf("the evaluator read a verdict the test binary wrote (ok %v, stored %d):\n%s", ok, got.Stored, got.Stdout)
	}
	if got, ok := a.EvaluateTests(1, 0, "/work/main.ergo", "main.ergo", withA(), "go B"); !ok || got.Stored != 0 {
		t.Fatalf("the evaluator under the pin go B read go A's verdict (ok %v, stored %d):\n%s", ok, got.Stored, got.Stdout)
	}
	// Under the pin that linked the binary, the evaluator (ergo check,
	// tier 1) reads its verdict and does not run the law again (FT2 r3).
	if got, ok := a.EvaluateTests(1, 0, "/work/main.ergo", "main.ergo", withA(), "go A"); !ok || got.Stored != 1 {
		t.Fatalf("the evaluator under the pin go A did not read go A's verdict (ok %v, stored %d):\n%s", ok, got.Stored, got.Stdout)
	}
}
