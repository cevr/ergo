package rt

import "testing"

// TestStartsEndAlone: each way of starting a second goroutine that runs
// Ergo code (a task, a group) ends Alone before that goroutine exists; a
// Ref read while Alone holds takes no lock (v1 sync.go), so a start that
// left it true would let two tasks write one cell unlocked (the mutants
// "spawn keeps alone" and "group keeps alone"). The test resets the
// process-wide flag, which no other test of this package reads, and puts
// it back.
func TestStartsEndAlone(t *testing.T) {
	if Exploring {
		t.Skip("the explored build is never alone")
	}
	was := spread.Load()
	t.Cleanup(func() { spread.Store(was) })
	starts := map[string]func(root *Task){
		"task": func(root *Task) {
			s := NewScope(root)
			h := Go(&s, "child", func(*Task) (int, error) { return 1, nil })
			if _, err := h.Wait(); err != nil {
				t.Errorf("the child failed %v", err)
			}
		},
		"group": func(root *Task) {
			g := NewGroup(root, "group")
			g.End()
		},
	}
	for name, start := range starts {
		spread.Store(false)
		if !Alone() {
			t.Fatalf("%s: a program that has started nothing is not alone", name)
		}
		start(Root(name))
		if Alone() {
			t.Errorf("%s: the program is still alone after it started one", name)
		}
	}
}
