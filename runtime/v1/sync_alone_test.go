package v1

import (
	"os"
	"os/exec"
	"runtime"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

const refFlipChild = "ERGO_REF_FLIP_CHILD"

// TestRefFlipInsideUpdate: an operation entered while the program is
// alone (rt.Alone) whose function starts a task that uses the same Ref
// stays atomic: the task's Set waits until the operation has stored, as
// it does behind the lock. Two operations may run such a function: a plain
// Update (any function value), and an in-place form the lowering did not
// prove alone (a literal whose footprint starts a task). Each function
// yields many times after the start and fails if the task's Set finished
// meanwhile, or saw the cell poisoned by the operation in flight (the
// mutant "inplace-alone-ignored"; a plain Update that skips its lock
// while alone, the mutant "update-skips-lock"). Run with
// -race, a Set during the function is also a data race on the cell.
//
// Alone is the process's history, and this package's other tests start
// tasks, so each case runs in a fresh process of this binary, as
// TestMainExit does, where nothing has been started yet.
func TestRefFlipInsideUpdate(t *testing.T) {
	if rt.Exploring {
		t.Skip("the explored build is never alone")
	}
	cases := map[string]func(r *Ref[int], f func(int) int){
		"Update": func(r *Ref[int], f func(int) int) { r.Update(rt.Root("sync"), f) },
		"InPlace not alone": func(r *Ref[int], f func(int) int) {
			RefUpdateInPlace(rt.Root("sync"), r, func(p *int) { *p = f(*p) }, func(v int) int { return v }, true, false)
		},
	}
	name := os.Getenv(refFlipChild)
	if name == "" {
		for name := range cases {
			cmd := exec.Command(os.Args[0], "-test.run=^TestRefFlipInsideUpdate$", "-test.count=1")
			cmd.Env = append(os.Environ(), refFlipChild+"="+name)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("%s: the fresh process failed: %v\n%s", name, err, out)
			}
		}
		return
	}
	if !rt.Alone() {
		t.Fatal("a fresh process has started a task before the test")
	}
	r := NewRef(0)
	s := rt.NewScope(rt.Root("flip"))
	set := make(chan struct{})
	var h *rt.Handle[struct{}]
	early := false
	cases[name](r, func(v int) int {
		h = rt.Go(&s, "setter", func(*rt.Task) (struct{}, error) {
			r.Set(5)
			close(set)
			return struct{}{}, nil
		})
		for range 2000 {
			runtime.Gosched()
			select {
			case <-set:
				early = true
			default:
			}
		}
		return v + 1
	})
	if _, err := h.Wait(); err != nil {
		t.Fatalf("%s: the setter failed %v", name, err)
	}
	var err error
	s.End(&err)
	if early {
		t.Errorf("%s: the task's Set ran while the operation that started it had not stored", name)
	}
	if got := r.Get(); got != 5 {
		t.Errorf("%s: after the operation and then the task's Set the Ref holds %d, want 5", name, got)
	}
}
