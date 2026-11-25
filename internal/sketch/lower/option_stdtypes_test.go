package lower

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
)

// TestOptionStdTypesNativeRun verifies NO-PRELUDE Unit 10a end-to-end:
// a program using only qualified option.Option, option.Some, and option.None
// across types, explicit/inferred literals, bare option.None values and ==/!=
// comparisons, match arms, refutable let patterns, and .Map/.Then/.OrFail/
// .Fold/.MapOr combinators checks with 0 diagnostics, evaluates cleanly at
// check time, lowers strictly via IR (NoFallback=true), and runs natively
// under ERGO_FALLBACK=off with identical stdout.
func TestOptionStdTypesNativeRun(t *testing.T) {
	src := `import io
import option

type Missing { Key string }

type Box { Held option.Option[int] }

func wrap(x int) option.Option[int] {
	if x <= 0 { option.None } else { option.Some[int]{Value: x} }
}

func wrapInferred(x int) option.Option[int] {
	if x <= 0 { option.None } else { option.Some{Value: x} }
}

func inspect(o option.Option[int]) int {
	match o {
	case option.Some[int] as s:
		s.Value
	case option.None:
		-1
	}
}

func unwrapField(o option.Option[int]) int {
	match o {
	case option.Some{Value: v}:
		v
	case option.None:
		0
	}
}

func wrapVal(x int) int {
	let s = option.Some[int]{Value: x}
	s.Value
}

func matchAsUnwrap(o option.Option[int]) int {
	match o {
	case option.Some as s:
		s.Value
	case option.None:
		-6
	}
}

func require(o option.Option[int]) int fails Missing {
	try o.OrFail(Missing{Key: "k"})
}

func main() needs io.Console {
	let xs = []option.Option[int]{wrap(4), wrapInferred(0), option.Some{Value: 2}, option.None}
	let b0 = Box{Held: option.None}
	let eq = b0.Held == option.None
	let ne = xs[0] != option.None
	let mapped = wrap(3).Map(func(v int) int { v * 2 }).Then(func(v int) option.Option[int] { option.Some{Value: v + 1} })
	let folded = mapped.Fold(10, func(acc int, v int) int { acc + v })
	let orVal = mapped.MapOr(0, func(v int) int { v })
	let req = require(xs[0]) catch {
	case Missing:
		-1
	}
	let reqMiss = require(xs[1]) catch {
	case Missing:
		-2
	}
	let sum = inspect(xs[0]) + unwrapField(xs[2]) + wrapVal(4) + matchAsUnwrap(xs[0]) + folded + orVal + req + reqMiss
	io.Console.Println("\(eq) \(ne) \(sum)")
}
`
	pkg, diags := check.CheckSource(src)
	if len(diags) > 0 {
		t.Fatalf("check failed: %v", diags)
	}
	evalOut, err := eval.RunMain(pkg, eval.DefaultBudget(), eval.Process{})
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	if evalOut.Skipped != "" {
		t.Fatalf("eval skipped: %q", evalOut.Skipped)
	}
	want := "true true 40\n"
	if evalOut.Stdout != want {
		t.Fatalf("eval stdout = %q, want %q", evalOut.Stdout, want)
	}

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "main.ergo")
	if err := os.WriteFile(srcPath, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	u := Unit{
		Package:    pkg,
		Files:      pkg.Files(),
		Sources:    []Source{{Path: srcPath, Text: src}},
		NoFallback: true,
	}
	if _, rep, err := LowerIR(u); err != nil || len(rep.Fallback) > 0 {
		t.Fatalf("LowerIR(NoFallback=true): err=%v fallback=%v", err, rep.Fallback)
	}

	needsBuild(t)
	cmd := exec.Command(ergoCLI(t), "run", srcPath)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "ERGO_FALLBACK=off", "ERGO_CACHE="+t.TempDir())
	outBytes, err := cmd.CombinedOutput()
	got := string(withoutBuildNote(outBytes))
	if err != nil {
		t.Fatalf("ergo run failed (%v):\n%s", err, got)
	}
	if got != want {
		t.Fatalf("native stdout = %q, want %q", got, want)
	}
}
