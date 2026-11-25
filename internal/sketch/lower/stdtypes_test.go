package lower

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
)

// TestStdTypesNativeHostEntry verifies NO-PRELUDE Unit 8a end-to-end:
//  1. A program using qualified Unit-8 std types (io.Console, os.Ambient,
//     time.Scheduler, time.LiveScheduler, rand.Random, codec.Writer/Reader/
//     WireWrongKind/WireMissingField, core.Closer/Context/Range, cmp.Ordered/
//     Number, maps.Hashing, task.Task) checks with 0 diagnostics, lowers
//     strictly via IR (NoFallback=true), and runs natively under
//     ERGO_FALLBACK=off with hostProvides binding io.Console and os.Ambient.
//  2. Qualified and unqualified spellings resolve to the exact same canonical
//     *check.TypeName and interoperate seamlessly in the same program.
func TestStdTypesNativeHostEntry(t *testing.T) {
	src := `import cmp
import codec
import core
import io
import maps
import os
import rand
import scope
import task
import time

type Item { Name string, Count int }

type KeyHash {} implements maps.Hashing[string]

func Hash(_ KeyHash, k string) uint64 { maps.Hash(k) }

func Equal(_ KeyHash, a string, b string) bool { a == b }

type Res { n int } implements core.Closer

func Close(_ own Res) {}

func minOrd[T cmp.Ordered](a T, b T) T {
	if a < b { a } else { b }
}

func maxNum[T cmp.Number](a T, b T) T {
	if a > b { a } else { b }
}

func sumRange(r core.Range[int]) int {
	let mut total = 0
	for i in r {
		total = total + i
	}
	total
}

func drawAndSleep() int needs time.Scheduler | rand.Random {
	time.Scheduler.Sleep(0s)
	rand.Random.IntN(1)
}

func runTask() int fails codec.WireWrongKind {
	let t task.Task[int, codec.WireWrongKind] = task.Go() { 40 }
	try t.Wait()
}

func checkWireKind(k codec.WireKind) int {
	match k {
	case codec.WireNumber:
		1
	case codec.WireNull, codec.WireBool, codec.WireString, codec.WireList, codec.WireObject, codec.WireEnd:
		0
	}
}

func capture() core.Context[io.Console] needs io.Console {
	scope.InForce[io.Console]()
}

func main() needs io.Console | os.Ambient {
	let res = Res{n: 1}
	let _ = res.n
	let sched time.LiveScheduler = time.NewLiveScheduler()
	let _ = sched
	let ev = codec.NewEvents()
	let w codec.Writer = ev
	codec.Encode(Item{Name: "ok", Count: 5}, w)
	let r codec.Reader = codec.NewReplay(ev.Recorded())
	let back = codec.Decode[Item](r) catch {
	case codec.WireWrongKind, codec.WireMissingField:
		Item{Name: "err", Count: 0}
	}
	let m = maps.Using[string, int, KeyHash](KeyHash)
	let roll = with context { time.Scheduler: time.NewManual(0), rand.Random: rand.Seeded(7) } {
		drawAndSleep()
	}
	let tv = runTask() catch {
	case codec.WireWrongKind:
		-1
	}
	let ctx = capture()
	let calc = minOrd(3, 7) + maxNum(2, 4) + sumRange(0..4) + back.Count + tv + roll + m.Len() + checkWireKind(codec.WireNumber)
	with ctx {
		io.Console.Println("\(back.Name) \(calc)")
	}
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
	want := "ok 59\n"
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
