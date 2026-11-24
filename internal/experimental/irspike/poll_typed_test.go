package irspike

import (
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRTypedPollBound requests a stop just after the first 1024 back
// edges of a loop with no source exit. Its body calls an env-free helper
// that can return a typed failure. The default must amortize, not check
// at each edge, but must still observe the pending stop within PollEvery
// more back edges. Mutant poll-typed-requires-pure checks every edge.
func TestIRTypedPollBound(t *testing.T) {
	const seed = 1
	text := pollTypedProgram(seed)
	u := loadText(t, t.TempDir(), text)
	requestAt := lower.PollEvery + 1
	const name = "spin"
	optimized := &pollEval{t: t, funcs: pollFuncs(t, u, "", seed, text), requestAt: requestAt, edgeLimit: requestAt + lower.PollEvery + 1}
	reference := &pollEval{t: t, funcs: pollFuncs(t, u, "each", seed, text), requestAt: requestAt, edgeLimit: optimized.edgeLimit}
	args := []any{pollEnv{}, 2}
	a, r := optimized.callBody(name, args), reference.callBody(name, args)
	if a.k != "stop" || r.k != "stop" ||
		optimized.run.edges-requestAt >= lower.PollEvery || reference.run.edges != requestAt ||
		2*optimized.run.checks >= reference.run.checks {
		t.Fatalf("seed %d: optimized ends %q, %+v; each ends %q, %+v\nreplay: go test -run '^TestIRTypedPollBound$' ./internal/experimental/irspike\n%s",
			seed, a.k, optimized.run, r.k, reference.run, text)
	}
}

// TestIRTypedPollAgrees builds Go because the actual typed outcome and
// output must agree when the fallible loop takes its normal or rejection
// edge, in the frozen source emitter and both native IR lowerings.
func TestIRTypedPollAgrees(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		t.Run(itoa(seed), func(t *testing.T) {
			dir := t.TempDir()
			text := pollTypedProgram(seed)
			t.Cleanup(func() {
				if t.Failed() {
					t.Logf("replay: ERGO_IRSPIKE_SEED=%d ERGO_IRSPIKE_SEEDS=1 go test -run '^TestIRTypedPollAgrees$' ./internal/experimental/irspike\n%s", seed, text)
				}
			})
			u := loadText(t, dir, text)
			srcBin, irBin, rep := both(t, u, src, ir, dir, BuildOptions{})
			if !slices.Contains(rep.IR, "sum") {
				t.Fatalf("sum has no IR body: %v", rep.Fallback)
			}
			legacy := filepath.Join(dir, "legacy")
			func() {
				defer lower.SetPollAmortLoweringForTest("pure")()
				if _, err := Emit(u, IR, legacy); err != nil {
					t.Fatal(err)
				}
			}()
			refBin := filepath.Join(dir, "ref.bin")
			if _, err := Build(ir, IR, legacy, refBin, BuildOptions{}); err != nil {
				t.Fatal(err)
			}
			a, b, c := run(t, irBin), run(t, refBin), run(t, srcBin)
			n := pollTypedSize(seed)
			want := outcome{stdout: fmt.Sprintf("sum %d\n%d -1 -2\n", n*(n-1)/2, n*(n-1)/2)}
			if a != b || a != c || a != want {
				t.Fatalf("optimized: %+v\nlegacy: %+v\nsource: %+v\nwant: %+v", a, b, c, want)
			}
		})
	}
}

func pollTypedSize(seed uint64) int { return 5*lower.PollEvery + int(seed%31) }

func pollTypedProgram(seed uint64) string {
	return fmt.Sprintf(`
type Rejected {}
type Empty {}

func admit(n int) fails Rejected | Empty {
	if n < 0 { return Rejected }
	if n == 0 { return Empty }
	return
}

func spin(n int) fails Rejected | Empty needs Console {
	Console.Println("spin")
	for {
		try admit(n)
	}
}

func sum(n int, offset int) int fails Rejected | Empty needs Console {
	let mut acc = 0
	for i in 0..n {
		try admit(i+offset)
		acc += i
	}
	Console.Println("sum \(acc)")
	if offset == -999 { try spin(offset) }
	acc
}

func main() needs Console {
	let a = sum(%[1]d, 1) catch { case Rejected: -1; case Empty: -2 }
	let b = sum(%[1]d, -1) catch { case Rejected: -1; case Empty: -2 }
	let c = sum(%[1]d, 0) catch { case Rejected: -1; case Empty: -2 }
	Console.Println("\(a) \(b) \(c)")
}
`, pollTypedSize(seed))
}
