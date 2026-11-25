package lower

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
)

// TestCollOpsOutContractAndUserTwin verifies NO-PRELUDE Unit 7 (COLLOPS-OUT):
//  1. Built-in slice, Map (string-keyed and int-keyed), string, []byte and rune
//     operations check, lower strictly through IR (NoFallback=true), keep their
//     Go-floor forms (comma-ok map read, direct append/delete, value-rule copy
//     on live Take), and produce identical output in eval and native Go.
//  2. Generic functions over core.{Lengthed,Appender,View,Joins,Lookup,MapWriter}
//     work identically on built-in types and a user twin claimant (`type List[T]`
//     implementing `core.Appender[T]` with `extern go append`), and a user
//     `extern go append` gets the exact same Body.AppendsSlice and pure
//     footprint facts as built-in []T.
func TestCollOpsOutContractAndUserTwin(t *testing.T) {
	t.Run("builtins and floor guarantees", func(t *testing.T) {
		src := `func fillSquares(n int) []int {
	let mut out = []int{}
	for i in 0..n {
		out = out.Append(i * i)
	}
	out
}

func lookupOr(m Map[string, int], k string, fallback int) int {
	match m.Get(k) {
	case Some[int] as s:
		s.Value
	case None:
		fallback
	}
}

func intMapOps() int {
	let mut m = Map[int, int]{}
	for i in 0..5 {
		m = m.Put(i, i*10)
	}
	m = m.Delete(2)
	let hit = match m.Get(3) {
	case Some[int] as s:
		s.Value
	case None:
		-1
	}
	let miss = match m.Get(2) {
	case Some[int] as s:
		s.Value
	case None:
		-1
	}
	hit + miss + m.Len()
}

func main() needs Console {
	let sq = fillSquares(5)
	let win = sq.Slice(1, 4)
	let (first, rest) = win.TakeAt(0)
	let joined = rest.Clone().Concat([]int{99}).Repeat(2)
	let capSlice = MakeSlice[int](3).Append(7)

	let m0 = Map[string, int]{}.Put("a", 10).Put("b", 20).Put("c", 30)
	let m1 = m0.Delete("b")
	let (taken, afterTake) = m1.Take("a")
	let takenVal = match taken {
	case Some[int] as s:
		s.Value
	case None:
		-1
	}
	let stillA = lookupOr(m1, "a", -1)
	let goneA = lookupOr(afterTake, "a", -1)
	let clonedC = m1.Clone().Get("c")
	let clonedVal = match clonedC {
	case Some[int] as s:
		s.Value
	case None:
		-1
	}

	let s = "hello world"
	let sub = s.Slice(0, 5)
	let buf = sub.Bytes().Grow(16).AppendString("!").AppendBytes([]byte{63}).AppendFrom(0, 2)
	let txt = buf.Text()
	let rtxt = 'Z'.RuneText()

	Console.Println("\(first) \(joined.Len()) \(capSlice[0]) \(takenVal) \(stillA) \(goneA) \(clonedVal) \(intMapOps()) \(txt) \(rtxt)")
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
		gotGo, irJSON := lowerAndRunIRCollOps(t, pkg, src)
		if gotGo != evalOut.Stdout {
			t.Fatalf("native stdout %q != eval stdout %q", gotGo, evalOut.Stdout)
		}
		want := "1 6 7 10 10 -1 30 33 hello!?he Z\n"
		if gotGo != want {
			t.Fatalf("stdout = %q, want %q", gotGo, want)
		}
		// Verify Go-floor IR nodes are emitted directly without any CollOp table.
		for _, wantIR := range []string{`"mapget"`, `"mapAt"`, `"delete"`, `"append"`} {
			if !strings.Contains(irJSON, wantIR) {
				t.Fatalf("expected %s in ergoir.json", wantIR)
			}
		}
	})

	t.Run("generic core contracts and user twin", func(t *testing.T) {
		src := `import core
import option

extern go append func rawAppend[T own](xs own []T, x own T) []T

type List { items []int } implements core.Appender[int]

func Len(l List) int { l.items.Len() }

func Append(l List, e int) List { List{items: rawAppend(l.items, e)} }

func Reserve(l List, extra int) List { List{items: l.items.Reserve(extra)} }

func pushTwo[C core.Appender[int]](c C, a int, b int) C {
	c.Reserve(2).Append(a).Append(b)
}

func readOrZero[C core.Lookup[string, int]](c C, k string) int {
	match c.Get(k) {
	case option.Some[int] as s:
		s.Value
	case option.None:
		0
	}
}

func putAndCount[C core.MapWriter[string, int]](c C, k string, v int) int {
	let updated = c.Put(k, v)
	let (got, rest) = updated.Take(k)
	let val = match got {
	case option.Some[int] as s:
		s.Value
	case option.None:
		-1
	}
	val + rest.Len() + updated.Clone().Delete(k).Len()
}

func sliceAndJoin[C core.View[int], J core.Joins](v C, j J) int {
	let w = v.Slice(0, 2)
	let (head, _) = w.TakeAt(0)
	head + j.Concat(j).Repeat(2).Len()
}

func main() needs Console {
	let xs = pushTwo([]int{1}, 2, 3)
	let ls = pushTwo(List{items: []int{1}}, 2, 3)
	let m = Map[string, int]{"x": 4}
	let r = readOrZero(m, "x")
	let p = putAndCount(m, "y", 9)
	let sj = sliceAndJoin(xs, xs)
	Console.Println("\(xs.Len()) \(ls.Len()) \(ls.items[2]) \(r) \(p) \(sj)")
}
`
		pkg, diags := check.CheckSource(src)
		if len(diags) > 0 {
			t.Fatalf("check failed: %v", diags)
		}
		// User extern go append receives the exact same Body.AppendsSlice and
		// purity facts as built-in []T's Append.
		var rawAppendFn *check.Func
		for _, f := range pkg.Funcs {
			if f.Name == "rawAppend" {
				rawAppendFn = f
				break
			}
		}
		if rawAppendFn == nil {
			t.Fatal("rawAppend not found")
		}
		if !check.BodyOf(rawAppendFn).AppendsSlice {
			t.Fatalf("user extern go append must have Body.AppendsSlice == true")
		}
		if !check.PureStd(rawAppendFn) {
			t.Fatalf("user extern go append must be pure")
		}
		evalOut, err := eval.RunMain(pkg, eval.DefaultBudget(), eval.Process{})
		if err != nil {
			t.Fatalf("eval failed: %v", err)
		}
		gotGo, _ := lowerAndRunIRCollOps(t, pkg, src)
		if gotGo != evalOut.Stdout {
			t.Fatalf("native stdout %q != eval stdout %q", gotGo, evalOut.Stdout)
		}
		want := "3 3 3 4 11 13\n"
		if gotGo != want {
			t.Fatalf("stdout = %q, want %q", gotGo, want)
		}
	})
}

func lowerAndRunIRCollOps(t *testing.T, pkg *check.Package, src string) (stdout string, irJSON string) {
	t.Helper()
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
	files, report, err := LowerIR(u)
	if err != nil {
		t.Fatalf("LowerIR: %v", err)
	}
	if len(report.Fallback) > 0 {
		t.Fatalf("unexpected IR fallback: %v", report.Fallback)
	}
	for _, f := range files {
		if f.Path == "ergoir.json" {
			irJSON = string(f.Data)
		}
	}
	needsBuild(t)
	cmd := exec.Command(ergoCLI(t), "run", srcPath)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "ERGO_FALLBACK=off", "ERGO_CACHE="+t.TempDir())
	outBytes, err := cmd.CombinedOutput()
	out := string(withoutBuildNote(outBytes))
	if err != nil {
		t.Fatalf("ergo run failed (%v):\n%s", err, out)
	}
	return out, irJSON
}
