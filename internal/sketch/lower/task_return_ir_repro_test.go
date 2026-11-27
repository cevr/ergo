package lower

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/syntax"
)

const taskReturnIRSource = `import queue
import scope
import task

type Fault { Code int }
type Unit {}

type Store { Get() int }
type Mem { N int } implements Store
func Get(m Mem) int { m.N }

type Spawner { Spawn(n int) Task[int] }

type LocalSpawner { Base int } implements Spawner
func Spawn(s LocalSpawner, n int) Task[int] {
	task.Go() { s.Base + n }
}

func popper(q queue.Queue[int]) Task[int, queue.Closed] {
	task.Go() { try q.Pop() }
}

func makeExplicitTask() task.Task[int, Fault] {
	task.Go() { 42 }
}

func makeUnitTask() task.Task[Unit] {
	task.Go() { Unit }
}

func makeCapTask(base int) task.Task[int] needs Store {
	task.Go() { Store.Get() + base }
}

func retTaskFn(x int) Task[int] {
	task.Go() { x + 1 }
}

func caseOuter() int {
	let outer = task.Go() {
		task.Go() { 42 }
	}
	let inner = outer.Wait()
	inner.Wait()
}

func caseExplicit() int {
	let outerExplicit = task.Go() {
		let t task.Task[int, Fault] = task.Go() { 99 }
		t
	}
	let innerExplicit = outerExplicit.Wait()
	innerExplicit.Wait() catch {
	case _:
		0
	}
}

func caseCall() int {
	let q = queue.New[int](1)
	let outerCall = task.Go() { popper(q) }
	let innerCall = outerCall.Wait()
	q.Push(7) catch {
	case _:
	}
	innerCall.Wait() catch {
	case _:
		0
	}
}

func caseMake() int {
	let outerMake = task.Go() { makeExplicitTask() }
	let inner = outerMake.Wait()
	inner.Wait() catch {
	case _:
		0
	}
}

func caseUnit() int {
	let outerUnit = task.Go() { makeUnitTask() }
	let inner = outerUnit.Wait()
	inner.Wait()
	1
}

func caseCap() int needs Store {
	let capVal = 5
	let exited = queue.New[int](1)
	let outerCap = task.Go() {
		scope.OnExit() {
			exited.Push(100) catch {
			case _:
			}
		}
		makeCapTask(capVal)
	}
	let inner = outerCap.Wait()
	let done = exited.Pop() catch {
	case _:
		0
	}
	inner.Wait() + done
}

func caseFnLit() int {
	let outerFn = task.Go(func() Task[int] { task.Go() { 77 } })
	let inner = outerFn.Wait()
	inner.Wait()
}

func caseRet() int {
	let outerRet = task.Go(func() Task[int] {
		let cond = true
		if cond {
			return task.Go() { 11 }
		}
		task.Go() { 22 }
	})
	let inner = outerRet.Wait()
	inner.Wait()
}

func caseIndirect() int {
	let outerIndirect = task.Go() {
		let fn = retTaskFn
		fn(4)
	}
	let inner = outerIndirect.Wait()
	inner.Wait()
}

func caseChainedWait() int {
	let outer = task.Go() {
		task.Go() { 42 }
	}
	outer.Wait().Wait()
}

func caseProviderAndOp() int needs Spawner {
	let s = LocalSpawner{Base: 100}
	let spawnedFromCap = Spawner.Spawn(3)
	let spawnedFromOp = s.Spawn(4)
	let nestedViaOp = task.Go() { s.Spawn(5) }
	spawnedFromCap.Wait() + spawnedFromOp.Wait() + nestedViaOp.Wait().Wait()
}

func caseStopReturnedTask() int {
	let ready = queue.New[int](1)
	let blocker = queue.New[int](1)
	let cleaned = queue.New[int](1)
	let outer = task.Go() {
		let child = task.Go() {
			scope.OnExit() {
				cleaned.Push(99) catch {
				case _:
				}
			}
			ready.Push(1) catch {
			case _:
			}
			blocker.Pop() catch {
			case _:
				-1
			}
		}
		child
	}
	let handed = outer.Wait()
	let sig = ready.Pop() catch {
	case _:
		0
	}
	handed.Stop()
	let ex = cleaned.Pop() catch {
	case _:
		0
	}
	sig + ex
}

test TaskGoReturnsTaskTest {
	with context { Store: Mem{N: 10}, Spawner: LocalSpawner{Base: 50} } {
		assert caseOuter() == 42
		assert caseExplicit() == 99
		assert caseCall() == 7
		assert caseMake() == 42
		assert caseUnit() == 1
		assert caseCap() == 115
		assert caseFnLit() == 77
		assert caseRet() == 11
		assert caseIndirect() == 5
		assert caseChainedWait() == 42
		assert caseProviderAndOp() == 262
		assert caseStopReturnedTask() == 100
	}
}

func main() needs Console | Ambient {
	with context { Store: Mem{N: 10}, Spawner: LocalSpawner{Base: 50}, Scheduler: NewLiveScheduler() } {
		Console.Println("\(caseOuter()) \(caseExplicit()) \(caseCall()) \(caseMake()) \(caseUnit()) \(caseCap()) \(caseFnLit()) \(caseRet()) \(caseIndirect()) \(caseChainedWait()) \(caseProviderAndOp()) \(caseStopReturnedTask())")
	}
}
`

// TestTaskGoReturnsTaskLowersInIR verifies issue #236: a task.Go body
// returning a Task (awaited via Wait, operation, capability method, or function
// value, and stopped after being handed out) lowers strictly via IR with zero
// text fallbacks, passes evaluation, and executes cleanly when compiled.
// builds Go because Task ownership and host compilation across yielded task.Task values must hold end-to-end in the native runtime.
func TestTaskGoReturnsTaskLowersInIR(t *testing.T) {
	file, err := syntax.Parse(taskReturnIRSource)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	file.Name = "main.ergo"
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("check:\n%v", diags)
	}

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "main.ergo")
	if err := os.WriteFile(srcPath, []byte(taskReturnIRSource), 0o644); err != nil {
		t.Fatal(err)
	}

	u := Unit{
		Package:    pkg,
		Files:      []*syntax.File{file},
		Sources:    []Source{{Path: srcPath, Text: taskReturnIRSource}},
		NoFallback: true,
	}
	if _, irRep, err := LowerIR(u); err != nil {
		t.Fatalf("LowerIR(NoFallback=true): %v", err)
	} else if len(irRep.Fallback) > 0 {
		t.Fatalf("LowerIR had fallbacks: %v", irRep.Fallback)
	}
	if _, testRep, err := LowerIRTests(u); err != nil {
		t.Fatalf("LowerIRTests(NoFallback=true): %v", err)
	} else if len(testRep.Fallback) > 0 {
		t.Fatalf("LowerIRTests had fallbacks: %v", testRep.Fallback)
	}

	rep, err := eval.Run(pkg, eval.DefaultBudget())
	if err != nil {
		t.Fatalf("eval.Run: %v", err)
	}
	if len(rep.Results) == 0 {
		t.Fatal("eval.Run returned no test results")
	}
	for _, r := range rep.Results {
		if r.Status != eval.Passed {
			t.Errorf("eval test %s: status=%v msg=%q", r.Name, r.Status, r.Message)
		}
	}

	needsBuild(t)
	cli := ergoCLI(t)
	runCmd := exec.Command(cli, "run", srcPath)
	runCmd.Dir = dir
	runCmd.Env = append(os.Environ(), "ERGO_FALLBACK=off")
	runOut, err := runCmd.CombinedOutput()
	got := strings.TrimSpace(string(withoutBuildNote(runOut)))
	const want = "42 99 7 42 1 115 77 11 5 42 262 100"
	if err != nil {
		t.Fatalf("ergo run failed (%v):\n%s", err, runOut)
	}
	if got != want {
		t.Fatalf("ergo run output = %q, want %q", got, want)
	}

	testCmd := exec.Command(cli, "test", "--native", srcPath)
	testCmd.Dir = dir
	testCmd.Env = append(os.Environ(), "ERGO_FALLBACK=off")
	testOut, err := testCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ergo test --native failed (%v):\n%s", err, testOut)
	}
	if !strings.Contains(string(testOut), "TaskGoReturnsTaskTest") {
		t.Fatalf("native test output missing TaskGoReturnsTaskTest:\n%s", testOut)
	}
}
