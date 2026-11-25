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

// TestFixOptionProperty is a seeded property test of the --option pass
// (NO-PRELUDE §5.2, §6 step 6d, Unit 10a) covering Option, Some, and None
// across every syntactic position: type annotations, slice/map element types,
// explicit and inferred Some literals, bare None values and ==/!= comparisons,
// match case arms (case Some[T] as s:, case Some{Value: v}:, case None:),
// refutable let patterns (let Some{Value: v} = e else { ... }), and Option
// combinators (.Map, .Then, .OrFail, .Fold, .MapOr).
func TestFixOptionProperty(t *testing.T) {
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
		r := rand.New(rand.NewPCG(seed, 0x09710a))
		src, wantExact, wantAbsent := genOptionProgram(r)
		fail := func(msg string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d: %s\nReplay: ERGO_SEED=%d go test -run '^TestFixOptionProperty$' ./internal/sketch/fix\nsource:\n%s",
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

		fixed, rewrites, err := fix.OptionSource(src, "gen.ergo")
		if err != nil {
			fail("OptionSource: %v", err)
		}
		if len(rewrites) == 0 {
			fail("expected option rewrites, got 0")
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
		again, more, err := fix.OptionSource(fixed, "gen.ergo")
		if err != nil || again != fixed || len(more) != 0 {
			fail("second OptionSource is not a no-op: err=%v rewrites=%d\nagain:\n%s", err, len(more), again)
		}
	}

	t.Run("aliased import reused", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "ergo.mod"), []byte("module aliasopt\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "option"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "option", "option.ergo"), []byte("func Tag() int { 9 }\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		src := `import aliasopt/option
import opt option

func maybe(x int) Option[int] {
	if x == 0 { None } else { Some{Value: x + option.Tag()} }
}

func main() {
	let _ = opt.MapOr(maybe(1), 0, func(v int) int { v })
}
`
		mainPath := filepath.Join(dir, "main.ergo")
		if err := os.WriteFile(mainPath, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		out, rewrites, err := fix.OptionPackage(dir, map[string]string{mainPath: src})
		if err != nil {
			t.Fatalf("OptionPackage: %v", err)
		}
		if len(rewrites) != 3 {
			t.Fatalf("expected 3 rewrites, got %d (%v)", len(rewrites), rewrites)
		}
		fixed := out[mainPath]
		if !strings.Contains(fixed, "opt.Option[int]") || !strings.Contains(fixed, "opt.None") || !strings.Contains(fixed, "opt.Some{Value:") {
			t.Fatalf("expected existing alias opt to be reused:\n%s", fixed)
		}
		if strings.Contains(fixed, "\nimport option\n") {
			t.Fatalf("must not add duplicate plain import option:\n%s", fixed)
		}
		a, err := sketchfront.CheckDirOptions(dir, out, check.Options{})
		if err != nil || len(a.Diagnostics) > 0 {
			t.Fatalf("fixed package failed to check: err=%v diags=%v\nfixed:\n%s", err, a.Diagnostics, fixed)
		}
	})

	t.Run("package shadow untouched", func(t *testing.T) {
		src := `type Some { Val int }

type None {}

type Option = Some | None

func makeAll(ok bool) Option {
	if ok { Some{Val: 4} } else { None }
}

func main() {
	match makeAll(true) {
	case Some{Val: v}:
		let _ = v
	case None:
	}
}
`
		if _, diags := check.CheckSource(src); len(diags) > 0 {
			t.Fatalf("source check failed: %v", diags)
		}
		fixed, rewrites, err := fix.OptionSource(src, "shadow.ergo")
		if err != nil {
			t.Fatalf("OptionSource: %v", err)
		}
		if len(rewrites) != 0 || fixed != src {
			t.Fatalf("package-level shadows must stay untouched: rewrites=%v\nfixed:\n%s", rewrites, fixed)
		}
	})

	t.Run("package name clash auto-aliases import", func(t *testing.T) {
		src := `import io

func option() int { 12 }

func maybe(x int) Option[int] {
	if x == 0 { None } else { Some{Value: x + option()} }
}

func main() needs io.Console {
	match maybe(3) {
	case Some{Value: v}:
		io.Console.Println("\(v)")
	case None:
		io.Console.Println("empty")
	}
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
		fixed, rewrites, err := fix.OptionSource(src, "clash.ergo")
		if err != nil {
			t.Fatalf("OptionSource: %v", err)
		}
		if len(rewrites) != 5 {
			t.Fatalf("expected 5 rewrites, got %d (%v)", len(rewrites), rewrites)
		}
		if !strings.Contains(fixed, "import stdoption option\n") || !strings.Contains(fixed, "stdoption.Option[int]") || !strings.Contains(fixed, "stdoption.None") || !strings.Contains(fixed, "stdoption.Some{Value:") {
			t.Fatalf("expected auto-aliased import stdoption option:\n%s", fixed)
		}
		afterPkg, afterDiags := check.CheckSource(fixed)
		if len(afterDiags) > 0 {
			t.Fatalf("after check: %v\nfixed:\n%s", afterDiags, fixed)
		}
		afterOut, err := eval.RunMain(afterPkg, eval.DefaultBudget(), eval.Process{})
		if err != nil || afterOut.Skipped != "" || afterOut.Stdout != beforeOut.Stdout {
			t.Fatalf("eval mismatch: err=%v skipped=%q before=%q after=%q", err, afterOut.Skipped, beforeOut.Stdout, afterOut.Stdout)
		}
		again, more, err := fix.OptionSource(fixed, "clash.ergo")
		if err != nil || again != fixed || len(more) != 0 {
			t.Fatalf("second pass not idempotent: err=%v rewrites=%d\nagain:\n%s", err, len(more), again)
		}
	})

	t.Run("unit 8 and machines types untouched in unit 10a", func(t *testing.T) {
		src := `func step(s int, e int) Decision[int, int] {
	if e == 0 { Ignore } else { Stay{Next: s + e} }
}

func main() needs Console {
	let _ = step(0, 1)
	Console.Println("ok")
}
`
		if _, diags := check.CheckSource(src); len(diags) > 0 {
			t.Fatalf("source check failed: %v", diags)
		}
		fixed, rewrites, err := fix.OptionSource(src, "stay.ergo")
		if err != nil {
			t.Fatalf("OptionSource: %v", err)
		}
		if len(rewrites) != 0 || fixed != src {
			t.Fatalf("Unit-8 and Machine types must stay untouched by --option: rewrites=%v\nfixed:\n%s", rewrites, fixed)
		}
	})
}

func genOptionProgram(r *rand.Rand) (src string, wantExact, wantAbsent []string) {
	preOption := r.IntN(2) == 1
	preCore := r.IntN(2) == 1

	imps := []string{}
	if preCore {
		imps = append(imps, "import core")
	}
	imps = append(imps, "import io")
	if preOption {
		imps = append(imps, "import option")
	}

	n := 2 + r.IntN(7)

	var b strings.Builder
	for _, im := range imps {
		b.WriteString(im + "\n")
	}
	b.WriteString("\n")
	b.WriteString("type Missing { Key string }\n\n")
	b.WriteString("type Box { Held Option[int] }\n\n")
	b.WriteString("func wrap(x int) Option[int] {\n")
	b.WriteString("\tif x <= 0 { None } else { Some[int]{Value: x} }\n")
	b.WriteString("}\n\n")
	b.WriteString("func wrapInferred(x int) Option[int] {\n")
	b.WriteString("\tif x <= 0 { None } else { Some{Value: x} }\n")
	b.WriteString("}\n\n")
	b.WriteString("func inspect(o Option[int]) int {\n")
	b.WriteString("\tmatch o {\n")
	b.WriteString("\tcase Some[int] as s:\n\t\ts.Value\n")
	b.WriteString("\tcase None:\n\t\t-1\n")
	b.WriteString("\t}\n}\n\n")
	b.WriteString("func unwrapField(o Option[int]) int {\n")
	b.WriteString("\tmatch o {\n")
	b.WriteString("\tcase Some{Value: v}:\n\t\tv\n")
	b.WriteString("\tcase None:\n\t\t0\n")
	b.WriteString("\t}\n}\n\n")
	b.WriteString("func letUnwrap(x int) int {\n")
	b.WriteString("\tlet Some{Value: v} = Some[int]{Value: x}\n")
	b.WriteString("\tv\n")
	b.WriteString("}\n\n")
	b.WriteString("func matchAsUnwrap(o Option[int]) int {\n")
	b.WriteString("\tmatch o {\n\tcase Some as s:\n\t\ts.Value\n\tcase None:\n\t\t-6\n\t}\n")
	b.WriteString("}\n\n")
	b.WriteString("func require(o Option[int]) int fails Missing {\n")
	b.WriteString("\ttry o.OrFail(Missing{Key: \"k\"})\n")
	b.WriteString("}\n\n")
	b.WriteString("func main() needs io.Console {\n")
	if preCore {
		b.WriteString("\tlet _ = core.CallSite{File: \"\", Line: 0, Column: 0, Pkg: 0, Id: 0}\n")
	}
	if preOption {
		b.WriteString("\tlet _ = option.MapOr(option.None, 0, func(v int) int { v })\n")
	}
	fmt.Fprintf(&b, "\tlet xs = []Option[int]{wrap(%d), wrapInferred(0), Some{Value: 1}, None}\n", n)
	fmt.Fprintf(&b, "\tlet m = Map[string, Option[int]]{\"a\": Some[int]{Value: %d}, \"b\": None}\n", n)
	b.WriteString("\tlet b0 = Box{Held: None}\n")
	b.WriteString("\tlet eq = b0.Held == None\n")
	b.WriteString("\tlet ne = xs[0] != None\n")
	b.WriteString("\tlet mapped = wrap(3).Map(func(v int) int { v * 2 }).Then(func(v int) Option[int] { Some{Value: v + 1} })\n")
	b.WriteString("\tlet folded = mapped.Fold(10, func(acc int, v int) int { acc + v })\n")
	b.WriteString("\tlet orVal = mapped.MapOr(0, func(v int) int { v })\n")
	b.WriteString("\tlet req = require(xs[0]) catch {\n\tcase Missing:\n\t\t-1\n\t}\n")
	fmt.Fprintf(&b, "\tlet sum = inspect(xs[0]) + unwrapField(xs[2]) + letUnwrap(%d) + matchAsUnwrap(xs[0]) + folded + orVal + req + m.Len()\n", n)
	b.WriteString("\tio.Console.Println(\"\\(eq) \\(ne) \\(sum)\")\n")
	b.WriteString("}\n")

	wantExact = []string{
		"import io\nimport option\n",
		"type Box { Held option.Option[int] }",
		"func wrap(x int) option.Option[int]",
		"if x <= 0 { option.None } else { option.Some[int]{Value: x} }",
		"if x <= 0 { option.None } else { option.Some{Value: x} }",
		"case option.Some[int] as s:",
		"case option.Some{Value: v}:",
		"case option.Some as s:",
		"case option.None:",
		"let option.Some{Value: v} = option.Some[int]{Value: x}",
		"[]option.Option[int]{",
		"Map[string, option.Option[int]]{",
		"Box{Held: option.None}",
		"b0.Held == option.None",
		"xs[0] != option.None",
		"wrap(3).Map(",
		"func(v int) option.Option[int] { option.Some{Value: v + 1} }",
	}
	wantAbsent = []string{
		" Option[",
		"[]Option[",
		" Some[",
		" Some{",
		" None }",
		" None\n",
		"== None",
		"!= None",
		"case Some",
		"case None:",
		"let Some",
	}
	return b.String(), wantExact, wantAbsent
}
