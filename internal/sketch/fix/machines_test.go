package fix_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	format "ergo.local/prototype/internal/hostsyntax"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/fix"
	"ergo.local/prototype/internal/sketchfront"
)

// TestFixMachinesProperty is a seeded property test of the --machines pass
// (NO-PRELUDE §5.2, §6 step 6c, Unit 9a) covering all 11 machine types:
// Enter, Stay, Emit, Ignore, Reject, Done, Tell, Decision, Timeout, Machine,
// and InputMachine across every syntactic position (machine declarations,
// step/after/Machine/InputMachine signatures, generic and inferred variant
// literals, bare zero-field Ignore/Reject values and case arms, match arms).
// Each seed verifies 0 diagnostics before & after, formatter fixpoint,
// idempotence, and identical evaluated stdout.
func TestFixMachinesProperty(t *testing.T) {
	var seeds []uint64
	if s := os.Getenv("ERGO_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{n}
	} else {
		for i := range uint64(24) {
			seeds = append(seeds, i+1)
		}
	}
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x9a11c0de))
		src, wantExact, wantAbsent := genMachinesProgram(r)
		fail := func(msg string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d: %s\nReplay: ERGO_SEED=%d go test -run '^TestFixMachinesProperty$' ./internal/sketch/fix\nsource:\n%s",
				seed, fmt.Sprintf(msg, args...), seed, src)
		}
		if formatted, err := format.Source(src); err != nil || formatted != src {
			fail("generated source is not formatted (%v):\n%s", err, formatted)
		}
		beforePkg, beforeDiags := check.CheckSource(src)
		if len(beforeDiags) > 0 {
			fail("generated source does not check: %v", beforeDiags)
		}
		beforeOut, err := eval.RunMain(beforePkg, eval.DefaultBudget(), eval.Process{})
		if err != nil {
			fail("eval before fix: %v", err)
		}
		if beforeOut.Skipped != "" {
			fail("eval before fix skipped: %q", beforeOut.Skipped)
		}

		fixed, rewrites, err := fix.MachinesSource(src, "gen.ergo")
		if err != nil {
			fail("MachinesSource: %v", err)
		}
		if len(rewrites) == 0 {
			fail("expected machine rewrites, got 0")
		}
		afterPkg, afterDiags := check.CheckSource(fixed)
		if len(afterDiags) > 0 {
			fail("fixed source does not check: %v\nfixed:\n%s", afterDiags, fixed)
		}
		if formatted, err := format.Source(fixed); err != nil || formatted != fixed {
			fail("fixed source is not a formatter fixpoint (%v):\nfixed:\n%s\nformatted:\n%s", err, fixed, formatted)
		}
		afterOut, err := eval.RunMain(afterPkg, eval.DefaultBudget(), eval.Process{})
		if err != nil {
			fail("eval after fix: %v\nfixed:\n%s", err, fixed)
		}
		if afterOut.Skipped != "" || afterOut.Stdout != beforeOut.Stdout {
			fail("eval stdout changed:\nbefore: %q (skipped=%q stderr=%q status=%d)\nafter:  %q (skipped=%q stderr=%q status=%d)\nfixed:\n%s",
				beforeOut.Stdout, beforeOut.Skipped, beforeOut.Stderr, beforeOut.Status,
				afterOut.Stdout, afterOut.Skipped, afterOut.Stderr, afterOut.Status, fixed)
		}
		for _, sub := range wantExact {
			if !strings.Contains(fixed, sub) {
				fail("fixed source missing expected spelling %q\nfixed:\n%s", sub, fixed)
			}
		}
		for _, sub := range wantAbsent {
			if strings.Contains(fixed, sub) {
				fail("fixed source must not contain %q\nfixed:\n%s", sub, fixed)
			}
		}
		seenImp := map[string]bool{}
		for _, line := range strings.Split(fixed, "\n") {
			if strings.HasPrefix(line, "import ") {
				if seenImp[line] {
					fail("duplicate import line %q\nfixed:\n%s", line, fixed)
				}
				seenImp[line] = true
			}
		}
		again, more, err := fix.MachinesSource(fixed, "gen.ergo")
		if err != nil || again != fixed || len(more) != 0 {
			fail("second MachinesSource is not a no-op: err=%v rewrites=%d\nagain:\n%s", err, len(more), again)
		}
	}

	t.Run("aliased import reused", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "ergo.mod"), []byte("module aliasm\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "machines"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "machines", "machines.ergo"), []byte("func Tag() int { 7 }\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		src := `import aliasm/machines
import m machines

func step(s int, e int) Decision[int, int] {
	if e == 0 { Ignore } else { Stay{Next: s + e + machines.Tag()} }
}

func main() {
	let _ = m.Next(0, step(0, 1))
}
`
		mainPath := filepath.Join(dir, "main.ergo")
		if err := os.WriteFile(mainPath, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		out, rewrites, err := fix.MachinesPackage(dir, map[string]string{mainPath: src})
		if err != nil {
			t.Fatalf("MachinesPackage: %v", err)
		}
		if len(rewrites) != 3 {
			t.Fatalf("expected 3 rewrites, got %d (%v)", len(rewrites), rewrites)
		}
		fixed := out[mainPath]
		if !strings.Contains(fixed, "m.Decision[int, int]") || !strings.Contains(fixed, "m.Ignore") || !strings.Contains(fixed, "m.Stay{Next:") {
			t.Fatalf("expected existing alias m to be reused:\n%s", fixed)
		}
		if strings.Contains(fixed, "\nimport machines\n") {
			t.Fatalf("must not add duplicate plain import machines:\n%s", fixed)
		}
		a, err := sketchfront.CheckDirOptions(dir, out, check.Options{})
		if err != nil || len(a.Diagnostics) > 0 {
			t.Fatalf("fixed package failed to check: err=%v diags=%v\nfixed:\n%s", err, a.Diagnostics, fixed)
		}
	})

	t.Run("package shadow untouched", func(t *testing.T) {
		src := `type Done { Val int }

type Ignore {}

type Reject {}

type Stay { Next int }

type Machine { ID int }

func makeAll() int {
	let d = Done{Val: 3}
	let _ = Ignore
	let _ = Reject
	let s = Stay{Next: 4}
	let m = Machine{ID: 5}
	d.Val + s.Next + m.ID
}

func main() {
	let _ = makeAll()
}
`
		if _, diags := check.CheckSource(src); len(diags) > 0 {
			t.Fatalf("source check failed: %v", diags)
		}
		fixed, rewrites, err := fix.MachinesSource(src, "shadow.ergo")
		if err != nil {
			t.Fatalf("MachinesSource: %v", err)
		}
		if len(rewrites) != 0 || fixed != src {
			t.Fatalf("package-level shadows must stay untouched: rewrites=%v\nfixed:\n%s", rewrites, fixed)
		}
	})

	t.Run("package name clash auto-aliases import", func(t *testing.T) {
		src := `func machines() int { 10 }

func step(s int, e int) Decision[int, int] {
	if e == 0 { Ignore } else { Stay{Next: s + e + machines()} }
}

func main() needs io.Console {
	match step(1, 2) {
	case Stay as st:
		io.Console.Println("\(st.Next)")
	case Enter, Emit, Ignore, Reject, Done, Tell:
		io.Console.Println("other")
	}
}
`
		src = "import io\n\n" + src
		beforePkg, beforeDiags := check.CheckSource(src)
		if len(beforeDiags) > 0 {
			t.Fatalf("before check: %v", beforeDiags)
		}
		beforeOut, err := eval.RunMain(beforePkg, eval.DefaultBudget(), eval.Process{})
		if err != nil {
			t.Fatalf("eval before: %v", err)
		}
		fixed, rewrites, err := fix.MachinesSource(src, "clash.ergo")
		if err != nil {
			t.Fatalf("MachinesSource: %v", err)
		}
		if len(rewrites) != 10 {
			t.Fatalf("expected 10 rewrites, got %d (%v)", len(rewrites), rewrites)
		}
		if !strings.Contains(fixed, "import stdmachines machines\n") || !strings.Contains(fixed, "stdmachines.Decision[int, int]") || !strings.Contains(fixed, "stdmachines.Stay") || !strings.Contains(fixed, "stdmachines.Ignore") {
			t.Fatalf("expected auto-aliased import stdmachines machines:\n%s", fixed)
		}
		afterPkg, afterDiags := check.CheckSource(fixed)
		if len(afterDiags) > 0 {
			t.Fatalf("after check: %v\nfixed:\n%s", afterDiags, fixed)
		}
		afterOut, err := eval.RunMain(afterPkg, eval.DefaultBudget(), eval.Process{})
		if err != nil || afterOut.Skipped != "" || afterOut.Stdout != beforeOut.Stdout {
			t.Fatalf("eval mismatch: err=%v skipped=%q before=%q after=%q", err, afterOut.Skipped, beforeOut.Stdout, afterOut.Stdout)
		}
		again, more, err := fix.MachinesSource(fixed, "clash.ergo")
		if err != nil || again != fixed || len(more) != 0 {
			t.Fatalf("second pass not idempotent: err=%v rewrites=%d\nagain:\n%s", err, len(more), again)
		}
	})

	t.Run("option and unit 8 types untouched in unit 9a", func(t *testing.T) {
		src := `func optVal(x Option[int]) int {
	match x {
	case Some{Value: v}:
		v
	case None:
		0
	}
}

func main() needs Console {
	Console.Println("\(optVal(Some{Value: 1}))")
}
`
		if _, diags := check.CheckSource(src); len(diags) > 0 {
			t.Fatalf("source check failed: %v", diags)
		}
		fixed, rewrites, err := fix.MachinesSource(src, "stay.ergo")
		if err != nil {
			t.Fatalf("MachinesSource: %v", err)
		}
		if len(rewrites) != 0 || fixed != src {
			t.Fatalf("Option and Unit-8 types must stay untouched by --machines: rewrites=%v\nfixed:\n%s", rewrites, fixed)
		}
	})
}

func genMachinesProgram(r *rand.Rand) (src string, wantExact, wantAbsent []string) {
	preMachines := r.IntN(2) == 1
	preTime := r.IntN(2) == 1

	imps := []string{"import io"}
	if preMachines {
		imps = append(imps, "import machines")
	}
	if preTime {
		imps = append(imps, "import time")
	}

	base := 1 + r.IntN(5)
	delta := 2 + r.IntN(4)

	var b strings.Builder
	for _, im := range imps {
		b.WriteString(im + "\n")
	}
	b.WriteString("\n")
	b.WriteString("type State { V int }\n\n")
	b.WriteString("type Ev = Go{N int} | Bump{N int} | Loud{N int} | Whisper{N int} | Skip{} | Bad{} | Stop{}\n\n")
	b.WriteString("func start(n int) State { State{V: n} }\n\n")
	b.WriteString("func step(s State, e Ev) Decision[State, int] {\n")
	b.WriteString("\tmatch e {\n")
	b.WriteString("\tcase Go{N: n}:\n\t\tEnter[State]{Next: State{V: s.V + n}}\n")
	b.WriteString("\tcase Bump{N: n}:\n\t\tStay{Next: State{V: s.V + n}}\n")
	b.WriteString("\tcase Loud{N: n}:\n\t\tEmit[State, int]{Next: State{V: s.V + n}, Output: n}\n")
	b.WriteString("\tcase Whisper{N: n}:\n\t\tTell{Next: State{V: s.V + n}, Output: n}\n")
	b.WriteString("\tcase Skip:\n\t\tIgnore\n")
	b.WriteString("\tcase Bad:\n\t\tReject\n")
	b.WriteString("\tcase Stop:\n\t\tDone{Output: s.V}\n")
	b.WriteString("\t}\n}\n\n")
	b.WriteString("func afterEv(s State) Option[Timeout[Ev]] {\n")
	b.WriteString("\tif s.V > 100 { Some{Value: Timeout[Ev]{In: 1s, Signal: Stop}} } else { None }\n")
	b.WriteString("}\n\n")
	b.WriteString("type Counter machine[State, Ev] { initial: State{V: 0}, step: step, after: afterEv }\n\n")
	b.WriteString("type Seeded machine[State, Ev] { initial: start, step: step, after: afterEv }\n\n")
	b.WriteString("func describe(d Decision[State, int]) int {\n")
	b.WriteString("\tmatch d {\n")
	b.WriteString("\tcase Enter{Next: n}:\n\t\tn.V\n")
	b.WriteString("\tcase Stay as st:\n\t\tst.Next.V\n")
	b.WriteString("\tcase Emit{Next: n, Output: o}:\n\t\tn.V + o\n")
	b.WriteString("\tcase Tell{Next: n, Output: o}:\n\t\tn.V + o\n")
	b.WriteString("\tcase Ignore:\n\t\t-1\n")
	b.WriteString("\tcase Reject:\n\t\t-2\n")
	b.WriteString("\tcase Done{Output: o}:\n\t\to\n")
	b.WriteString("\t}\n}\n\n")
	b.WriteString("func takeMachine(m Machine[State, Ev, int]) int { m.Initial.V }\n\n")
	b.WriteString("func takeInput(im InputMachine[int, State, Ev, int], seed int) State { im.Start(seed) }\n\n")
	b.WriteString("func main() needs io.Console {\n")
	if preMachines {
		b.WriteString("\tlet _ = machines.Initial(Counter{})\n")
	}
	if preTime {
		b.WriteString("\tlet _ = time.NewManual(0)\n")
	}
	b.WriteString("\tlet a = takeMachine(Counter{})\n")
	fmt.Fprintf(&b, "\tlet b = takeInput(Seeded{}, %d)\n", base)
	fmt.Fprintf(&b, "\tlet total = describe(step(b, Go{N: %d})) + describe(step(b, Bump{N: 1})) + describe(step(b, Loud{N: 2})) + describe(step(b, Whisper{N: 3})) + describe(step(b, Skip)) + describe(step(b, Bad)) + describe(step(b, Stop))\n", delta)
	b.WriteString("\tio.Console.Println(\"\\(a) \\(b.V) \\(total)\")\n")
	b.WriteString("}\n")

	wantExact = []string{
		"import io\nimport machines\n",
		"func step(s State, e Ev) machines.Decision[State, int]",
		"machines.Enter[State]{Next: State{V: s.V + n}}",
		"machines.Stay{Next: State{V: s.V + n}}",
		"machines.Emit[State, int]{Next: State{V: s.V + n}, Output: n}",
		"machines.Tell{Next: State{V: s.V + n}, Output: n}",
		"\t\tmachines.Ignore\n",
		"\t\tmachines.Reject\n",
		"machines.Done{Output: s.V}",
		"Option[machines.Timeout[Ev]]",
		"machines.Timeout[Ev]{In: 1s, Signal: Stop}",
		"func describe(d machines.Decision[State, int]) int",
		"case machines.Enter{Next: n}:",
		"case machines.Stay as st:",
		"case machines.Emit{Next: n, Output: o}:",
		"case machines.Tell{Next: n, Output: o}:",
		"case machines.Ignore:",
		"case machines.Reject:",
		"case machines.Done{Output: o}:",
		"func takeMachine(m machines.Machine[State, Ev, int]) int",
		"func takeInput(im machines.InputMachine[int, State, Ev, int], seed int) State",
	}
	wantAbsent = []string{
		" Decision[",
		" Enter[",
		"\t\tStay{",
		" Emit[",
		"\t\tTell{",
		"\t\tIgnore\n",
		"\t\tReject\n",
		"\t\tDone{",
		"Option[Timeout[",
		"case Enter{",
		"case Stay ",
		"case Emit{",
		"case Tell{",
		"case Ignore:",
		"case Reject:",
		"case Done{",
		" Machine[",
		" InputMachine[",
	}
	return b.String(), wantExact, wantAbsent
}
