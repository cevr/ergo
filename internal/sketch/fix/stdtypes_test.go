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

// TestFixStdTypesProperty is a seeded property test of the --std-types pass
// (NO-PRELUDE §5.2, Unit 8a) covering all 10 destination std packages:
//   - maps: Hashing, Structural
//   - task: Task
//   - cmp: ordered -> cmp.Ordered, number -> cmp.Number
//   - os: Ambient
//   - fstest: TestAmbient, TestDirectory, TestLoopback
//   - rand: Random
//   - time: Scheduler, AlarmClock, LiveScheduler
//   - codec: Writer, Reader, Wire*
//   - io: Console
//   - core: Range, Seq, Never, Context, CallSite, Closer, Keeping, Unsatisfied, NoMatch
//
// Each seed varies pre-existing imports, tests every syntactic position
// (types, rows, capability calls, context literals, implements, patterns,
// composite literals, generic constraints), verifies 0 diagnostics before &
// after, formatter fixpoint, idempotence, and identical evaluated stdout.
func TestFixStdTypesProperty(t *testing.T) {
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
		r := rand.New(rand.NewPCG(seed, 0x57d79e5))
		src, wantExact, wantAbsent := genStdTypesProgram(r)
		fail := func(msg string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d: %s\nReplay: ERGO_SEED=%d go test -run '^TestFixStdTypesProperty$' ./internal/sketch/fix\nsource:\n%s",
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

		fixed, rewrites, err := fix.StdTypesSource(src, "gen.ergo")
		if err != nil {
			fail("StdTypesSource: %v", err)
		}
		if len(rewrites) == 0 {
			fail("expected std-type rewrites, got 0")
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
		if afterOut.Stdout != beforeOut.Stdout {
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
		again, more, err := fix.StdTypesSource(fixed, "gen.ergo")
		if err != nil || again != fixed || len(more) != 0 {
			fail("second StdTypesSource is not a no-op: err=%v rewrites=%d\nagain:\n%s", err, len(more), again)
		}
	}

	t.Run("aliased import reused", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "ergo.mod"), []byte("module aliasio\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "io"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "io", "io.ergo"), []byte("func Tag() string { \"local\" }\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		src := `import aliasio/io
import stdio io

func main() needs Console {
	let _ = stdio.NewRecorder()
	Console.Println(io.Tag())
}
`
		mainPath := filepath.Join(dir, "main.ergo")
		if err := os.WriteFile(mainPath, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		out, rewrites, err := fix.StdTypesPackage(dir, map[string]string{mainPath: src})
		if err != nil {
			t.Fatalf("StdTypesPackage: %v", err)
		}
		if len(rewrites) != 2 {
			t.Fatalf("expected 2 rewrites, got %d (%v)", len(rewrites), rewrites)
		}
		fixed := out[mainPath]
		if !strings.Contains(fixed, "needs stdio.Console") || !strings.Contains(fixed, "stdio.Console.Println") {
			t.Fatalf("expected existing alias stdio to be reused:\n%s", fixed)
		}
		if strings.Contains(fixed, "\nimport io\n") {
			t.Fatalf("must not add duplicate plain import io:\n%s", fixed)
		}
		a, err := sketchfront.CheckDirOptions(dir, out, check.Options{})
		if err != nil || len(a.Diagnostics) > 0 {
			t.Fatalf("fixed package failed to check: err=%v diags=%v\nfixed:\n%s", err, a.Diagnostics, fixed)
		}
	})

	t.Run("package shadow untouched", func(t *testing.T) {
		src := `type Console { Tag string }

type Range { Lo int, Hi int }

type Task { ID int }

func makeAll() int {
	let c = Console{Tag: "mine"}
	let r = Range{Lo: 1, Hi: 4}
	let t = Task{ID: 7}
	c.Tag.Len() + r.Hi - r.Lo + t.ID
}

func main() {}
`
		if _, diags := check.CheckSource(src); len(diags) > 0 {
			t.Fatalf("source check failed: %v", diags)
		}
		fixed, rewrites, err := fix.StdTypesSource(src, "shadow.ergo")
		if err != nil {
			t.Fatalf("StdTypesSource: %v", err)
		}
		if len(rewrites) != 0 || fixed != src {
			t.Fatalf("package-level shadows must stay untouched: rewrites=%v\nfixed:\n%s", rewrites, fixed)
		}
	})

	t.Run("package name clash auto-aliases import", func(t *testing.T) {
		src := `func io() string { "mine" }

func main() needs Console {
	Console.Println(io())
}
`
		beforePkg, beforeDiags := check.CheckSource(src)
		if len(beforeDiags) > 0 {
			t.Fatalf("before check: %v", beforeDiags)
		}
		beforeOut, err := eval.RunMain(beforePkg, eval.DefaultBudget(), eval.Process{})
		if err != nil {
			t.Fatalf("eval before: %v", err)
		}
		fixed, rewrites, err := fix.StdTypesSource(src, "clash.ergo")
		if err != nil {
			t.Fatalf("StdTypesSource: %v", err)
		}
		if len(rewrites) != 2 {
			t.Fatalf("expected 2 rewrites, got %d (%v)", len(rewrites), rewrites)
		}
		if !strings.Contains(fixed, "import stdio io\n") || !strings.Contains(fixed, "needs stdio.Console") || !strings.Contains(fixed, "stdio.Console.Println") {
			t.Fatalf("expected auto-aliased import stdio io:\n%s", fixed)
		}
		afterPkg, afterDiags := check.CheckSource(fixed)
		if len(afterDiags) > 0 {
			t.Fatalf("after check: %v\nfixed:\n%s", afterDiags, fixed)
		}
		afterOut, err := eval.RunMain(afterPkg, eval.DefaultBudget(), eval.Process{})
		if err != nil || afterOut.Stdout != beforeOut.Stdout {
			t.Fatalf("eval mismatch: err=%v before=%q after=%q", err, beforeOut.Stdout, afterOut.Stdout)
		}
		again, more, err := fix.StdTypesSource(fixed, "clash.ergo")
		if err != nil || again != fixed || len(more) != 0 {
			t.Fatalf("second pass not idempotent: err=%v rewrites=%d\nagain:\n%s", err, len(more), again)
		}
	})

	t.Run("machines and option untouched in unit 8a", func(t *testing.T) {
		src := `func optStep(x Option[int]) Decision[int, int] {
	match x {
	case Some{Value: v}:
		Done{Output: v}
	case None:
		Stay{Next: 0}
	}
}

func main() {
	let _ = optStep(Some{Value: 1})
}
`
		if _, diags := check.CheckSource(src); len(diags) > 0 {
			t.Fatalf("source check failed: %v", diags)
		}
		fixed, rewrites, err := fix.StdTypesSource(src, "stay.ergo")
		if err != nil {
			t.Fatalf("StdTypesSource: %v", err)
		}
		if len(rewrites) != 0 || fixed != src {
			t.Fatalf("Option/Decision family must stay untouched in Unit 8a: rewrites=%v\nfixed:\n%s", rewrites, fixed)
		}
	})
}

func genStdTypesProgram(r *rand.Rand) (src string, wantExact, wantAbsent []string) {
	preIO := r.IntN(2) == 1
	preTime := r.IntN(2) == 1
	preRand := r.IntN(2) == 1
	preCore := r.IntN(2) == 1

	imps := []string{"import codec"}
	if preCore {
		imps = append(imps, "import core")
	}
	if preIO {
		imps = append(imps, "import io")
	}
	if preRand {
		imps = append(imps, "import rand")
	}
	if preTime {
		imps = append(imps, "import time")
	}

	n := 2 + r.IntN(4)
	words := []string{"north", "south", "east", "west"}
	w := words[r.IntN(len(words))]

	var b strings.Builder
	for _, im := range imps {
		b.WriteString(im + "\n")
	}
	b.WriteString("\n")
	b.WriteString("type Pair { Tag string, N int }\n\n")
	b.WriteString("type KeyHash {} implements Hashing[string]\n\n")
	b.WriteString("func Hash(_ KeyHash, k string) uint64 { hash(k) }\n\n")
	b.WriteString("func Equal(_ KeyHash, a string, b string) bool { a == b }\n\n")
	b.WriteString("type Handle { v int } implements Closer\n\n")
	b.WriteString("func Close(_ own Handle) {}\n\n")
	b.WriteString("func smallest[T ordered](a T, b T) T {\n\tif a < b { a } else { b }\n}\n\n")
	b.WriteString("func addNums[T number](a T, b T) T { a + b }\n\n")
	b.WriteString("func checkKind(k WireKind) int {\n\tmatch k {\n\tcase WireNull:\n\t\t0\n\tcase WireBool:\n\t\t1\n\tcase WireNumber:\n\t\t2\n\tcase WireString:\n\t\t3\n\tcase WireList:\n\t\t4\n\tcase WireObject:\n\t\t5\n\tcase WireEnd:\n\t\t6\n\t}\n}\n\n")
	b.WriteString("func needHost(_ Ambient, _ TestAmbient, _ TestLoopback, _ TestDirectory) {}\n\n")
	b.WriteString("func needTask(_ Task[int, WireWrongKind], _ AlarmClock, _ LiveScheduler, _ Never, _ CallSite, _ Unsatisfied, _ NoMatch) {}\n\n")
	b.WriteString("func wireMarkers(_ WireOmittable, _ WireNullable, _ WireBare, _ WireNumberText, _ WireMembers, _ WireUnknownTag) {}\n\n")
	b.WriteString("func useCap() int needs Console | Scheduler | Random {\n")
	b.WriteString("\tScheduler.Sleep(0s)\n")
	b.WriteString("\tlet roll = Random.IntN(1)\n")
	b.WriteString("\tConsole.Println(\"roll=\\(roll)\")\n")
	b.WriteString("\troll\n")
	b.WriteString("}\n\n")
	b.WriteString("func sumRange(r Range[int]) int {\n\tlet mut s = 0\n\tfor i in r {\n\t\ts = s + i\n\t}\n\ts\n}\n\n")
	b.WriteString("func captureConsole() Context[Console] needs Console {\n\tInForce[Console]()\n}\n\n")
	b.WriteString("func main() needs Console {\n")
	if preCore {
		b.WriteString("\tlet _ = core.CallSite{File: \"\", Line: 0, Column: 0, Pkg: 0, Id: 0}\n")
	}
	if preIO {
		b.WriteString("\tlet _ = io.NewRecorder()\n")
	}
	if preTime {
		b.WriteString("\tlet _ = time.NewManual(0)\n")
	}
	if preRand {
		b.WriteString("\tlet _ = rand.Seeded(1)\n")
	}
	b.WriteString("\tlet h = Handle{v: 1}\n")
	b.WriteString("\tlet _ = h.v\n")
	fmt.Fprintf(&b, "\tlet p = Pair{Tag: %q, N: %d}\n", w, n)
	b.WriteString("\tlet w Writer = codec.NewEvents()\n")
	b.WriteString("\tcodec.Encode(p, w)\n")
	b.WriteString("\tlet ev = codec.NewEvents()\n")
	b.WriteString("\tcodec.Encode(p, ev)\n")
	b.WriteString("\tlet r Reader = codec.NewReplay(ev.Recorded())\n")
	b.WriteString("\tlet back = codec.Decode[Pair](r) catch {\n")
	b.WriteString("\tcase WireWrongKind, WireMissingField:\n\t\tPair{Tag: \"bad\", N: 0}\n\t}\n")
	b.WriteString("\tlet m = MakeMap[string, int, KeyHash](KeyHash)\n")
	b.WriteString("\tlet m2 Map[string, int, Structural[string]] = Map[string, int]{}\n")
	fmt.Fprintf(&b, "\tlet sm = smallest(%d, %d) + addNums(1, 2)\n", n, n+1)
	fmt.Fprintf(&b, "\tlet sr = sumRange(0..%d)\n", n)
	b.WriteString("\tlet ctx = captureConsole()\n")
	b.WriteString("\tlet k = checkKind(WireNumber)\n")
	b.WriteString("\twith ctx {\n\t\tConsole.Println(\"\\(back.Tag) \\(sm) \\(sr) \\(k) \\(m.Len()) \\(m2.Len())\")\n\t}\n")
	b.WriteString("}\n")

	wantExact = []string{
		"implements maps.Hashing[string]",
		"maps.Structural[string]",
		"implements core.Closer",
		"func smallest[T cmp.Ordered]",
		"func addNums[T cmp.Number]",
		"func checkKind(k codec.WireKind)",
		"case codec.WireNull:",
		"case codec.WireEnd:",
		"_ os.Ambient, _ fstest.TestAmbient, _ fstest.TestLoopback, _ fstest.TestDirectory",
		"_ task.Task[int, codec.WireWrongKind], _ time.AlarmClock, _ time.LiveScheduler, _ core.Never, _ core.CallSite, _ core.Unsatisfied, _ core.NoMatch",
		"_ codec.WireOmittable, _ codec.WireNullable, _ codec.WireBare, _ codec.WireNumberText, _ codec.WireMembers, _ codec.WireUnknownTag",
		"needs io.Console | time.Scheduler | rand.Random",
		"time.Scheduler.Sleep(0s)",
		"rand.Random.IntN(1)",
		"io.Console.Println(\"roll=\\(roll)\")",
		"func sumRange(r core.Range[int])",
		"func captureConsole() core.Context[io.Console] needs io.Console",
		"InForce[io.Console]()",
		"let w codec.Writer = codec.NewEvents()",
		"let r codec.Reader = codec.NewReplay(ev.Recorded())",
		"case codec.WireWrongKind, codec.WireMissingField:",
		"checkKind(codec.WireNumber)",
		"import cmp\nimport codec\nimport core\nimport fstest\nimport io\nimport maps\nimport os\nimport rand\nimport task\nimport time\n",
	}
	wantAbsent = []string{
		"[T ordered]",
		"[T number]",
		"implements Closer",
		"needs Console",
		"\tConsole.Println(",
		"\tScheduler.Sleep(",
		"= Random.IntN(",
		" Context[",
		" Range[int]",
		"_ Task[int,",
	}
	return b.String(), wantExact, wantAbsent
}
