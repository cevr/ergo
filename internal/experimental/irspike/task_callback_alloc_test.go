package irspike

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRMovedTaskCallbackMatchesSource exercises a moved callback that
// reads a provider and returns either a value or a typed failure. It
// builds Go because the callback must run under the spawned task's
// environment, and the join must observe its exit.
func TestIRMovedTaskCallbackMatchesSource(t *testing.T) {
	program := func(seed uint64) string {
		return fmt.Sprintf(`import task

type Rejected { Code int }

func moved(f own func() int fails Rejected needs Console) int fails Rejected needs Console {
	let t = task.Go() { try f() }
	try t.Wait()
}

func main() needs Console {
	let value = moved(func() int fails Rejected needs Console {
		Console.Println("child")
		if %t {
			return Rejected{Code: %d}
		}
		%d
	}) catch {
	case Rejected as r:
		0 - r.Code
	}
	Console.Println("\(value)")
}
`, seed%2 == 0, 1+seed%100, 100+seed%100)
	}
	t.Run("task-frame", func(t *testing.T) {
		f7Property(t, t.Name(), program, "moved")
	})
	t.Run("local-frame", func(t *testing.T) {
		prev := lower.SetSpawnLoweringForTest("local-frame")
		defer lower.SetSpawnLoweringForTest(prev)
		f7Property(t, t.Name(), program, "moved")
	})
}

// TestIRMovedTaskCallbackAllocations checks that invoking a moved callback
// costs no allocation beyond a spawn that only borrows a plain value.
// It builds Go because Go's escape analysis and the heap allocations of
// the running child are not observable in the checked IR.
func TestIRMovedTaskCallbackAllocations(t *testing.T) {
	_, tc := needsBuild(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "p.ergo")
	if err := os.WriteFile(src, []byte(taskCallbackAllocProgram), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := Load(src)
	if err != nil {
		t.Fatal(err)
	}
	mod, bin := filepath.Join(dir, "ir"), filepath.Join(dir, "alloc")
	rep, err := Emit(u, IR, mod)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"moved", "borrowed"} {
		if !slices.Contains(rep.IR, f) {
			t.Fatalf("%s has no IR body: %v", f, rep.Fallback)
		}
	}
	if err := os.WriteFile(filepath.Join(mod, "count.go"), []byte(taskCallbackAllocCounter), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(tc, IR, mod, bin, BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	got := run(t, bin, "GOGC=off", "GOMAXPROCS=1")
	var moved, borrowed float64
	if _, err := fmt.Sscan(strings.TrimSpace(got.stdout), &moved, &borrowed); got.exit != 0 || err != nil {
		t.Fatalf("counting task allocations: %+v (%v)", got, err)
	}
	t.Logf("allocations per spawn: moved %.3f, borrowed %.3f", moved, borrowed)
	if moved != borrowed {
		t.Fatalf("a moved callback allocates %.3f times per spawn; borrowed allocates %.3f", moved, borrowed)
	}
}

const taskCallbackAllocProgram = `import task

func moved(f own func() int) int {
	let t = task.Go() { f() }
	t.Wait()
}

func borrowed(i int) int {
	let t = task.Go() { i & 3 }
	t.Wait()
}

type wrongTaskValue {}

func main() fails wrongTaskValue {
	if moved(func() int { 3 }) + borrowed(3) != 6 {
		return wrongTaskValue
	}
}
`

const taskCallbackAllocCounter = `package main

import (
	"fmt"
	"testing"
	"ergo.local/prototype/runtime/rt"
)

func init() {
	_, err := rt.Run(rt.Root("moved callback allocs"), func(task *rt.Task) (struct{}, error) {
		env := &Env{task: task}
		var held *Env
		f := func(child *Env) (int, error) {
			if child.task == nil || child.task == task {
				panic("callback did not run on the child task")
			}
			held = child
			return 3, nil
		}
		m := testing.AllocsPerRun(1000, func() {
			if moved(env, f) != 3 { panic("moved callback returned the wrong value") }
			if held.task == nil || held.task == task { panic("the child environment was changed on return") }
		})
		b := testing.AllocsPerRun(1000, func() {
			if borrowed(env, 3) != 3 { panic("borrowed task returned the wrong value") }
		})
		fmt.Println(m, b)
		return struct{}{}, nil
	})
	if err != nil { panic(err) }
}
`
