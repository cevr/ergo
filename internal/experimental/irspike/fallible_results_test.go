package irspike

import (
	"fmt"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRBoxedSuccessAgrees builds Go because the success's dynamically
// named result and the two catches must run under both result ABIs.
// Mutant flat-boxed-success-out adds the out slot back.
func TestIRBoxedSuccessAgrees(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		text := boxedSuccess(seed)
		dir := t.TempDir()
		u := loadText(t, dir, text)
		gotFns := pollFuncs(t, u, "", seed, text)
		if gotFns["choose"] == nil || gotFns["chooseFlat"] != nil {
			t.Fatalf("seed %d: choose body=%t, flat worker=%t; want a body and no flat worker:\n%s", seed, gotFns["choose"] != nil, gotFns["chooseFlat"] != nil, text)
		}
		sb, ib, _ := both(t, u, src, ir, dir, BuildOptions{})
		ref := filepath.Join(dir, "ref")
		func() {
			defer lower.SetFlatLoweringForTest("signature")()
			if _, err := Emit(u, IR, ref); err != nil {
				t.Fatal(err)
			}
		}()
		rb := filepath.Join(dir, "ref.bin")
		if _, err := Build(ir, IR, ref, rb, BuildOptions{}); err != nil {
			t.Fatal(err)
		}
		got, old, source := run(t, ib), run(t, rb), run(t, sb)
		if got != old || got != source || got.stdout != fmt.Sprintf("%d 0 -1\n", seed) {
			t.Fatalf("seed %d: native %+v, reference %+v, source %+v\nreplay: ERGO_IRSPIKE_SEED=%d ERGO_IRSPIKE_SEEDS=1 go test -run '^TestIRBoxedSuccessAgrees$' ./internal/experimental/irspike\n%s", seed, got, old, source, seed, text)
		}
	}
}

func boxedSuccess(seed uint64) string {
	return fmt.Sprintf(`
type Wide { A int, B int, C int, D int, E int }
type Other {}
type Verdict = Wide | Other
type Failed { Reason string }

func choose(n int) Verdict fails Failed {
	if n < 0 { return Failed{Reason: "negative"} }
	if n == 0 { return Other }
	Wide{A: n, B: n, C: n, D: n, E: n}
}

func carry(v Verdict) Verdict { v }

func show(n int) int {
	let v = choose(n) catch { case Failed: return -1 }
	match carry(v) { case Wide as w: w.E; case Other: 0 }
}

func main() needs Console {
	Console.Println("\(show(%d)) \(show(0)) \(show(-1))")
}
`, seed)
}
