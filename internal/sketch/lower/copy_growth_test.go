package lower

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// uiPassSource is the shape of a UI pass (V8): each node put reads its
// parent's sibling Map out of a Map of Maps and the parent's kept slice
// out of a Map of slices, through a borrow, then writes both back. The
// fan-out is bounded (four children a parent), so the work a node costs
// does not grow with the tree.
const uiPassSource = `type index { kids Map[int, Map[int, int]], kept Map[int, []int] }

func place(x *index, parent int, id int) int {
	let mut sib = Map[int, int]{}
	match x.kids.Get(parent) {
	case Some[Map[int, int]] as m:
		sib = m.Value
	case None:
	}
	sib[id] = len(sib)
	x.kids[parent] = sib
	let mut seen = 0
	match x.kept.Get(parent) {
	case Some[[]int] as k:
		seen = len(k.Value)
	case None:
	}
	x.kept[id] = []int{parent, id}
	seen
}

func pass(n int) int {
	let mut x = index{kids: Map[int, Map[int, int]]{}, kept: Map[int, []int]{}}
	let mut t = 0
	for id in 1..n + 1 {
		t = t + place(&x, id / 4, id)
	}
	t + len(x.kids)
}

func main() needs Console {
	Console.Println("\(pass(10))")
}
`

const uiPassGoTest = `package main

import (
	"fmt"
	"runtime"
	"testing"
)

func TestCount(t *testing.T) {
	for _, n := range []int{1000, 2000} {
		pass(n) // the first run grows the heap
		var a, b runtime.MemStats
		runtime.ReadMemStats(&a)
		pass(n)
		runtime.ReadMemStats(&b)
		fmt.Printf("mallocs %d %d\n", n, b.Mallocs-a.Mallocs)
	}
}
`

// TestUIPassGrowsLinearly is the growth guard of the entry rule (V8): a UI
// pass over n nodes allocates at most twice, plus a little, at 2n what it
// allocates at n. Under the rule before V8 each Get through the borrow
// copied the whole index, entry by entry, so the pass was quadratic.
// The count is the Go runtime's allocation counter, not a time.
//
// It builds Go because the count is of the built program's allocations,
// which only a run of it shows.
func TestUIPassGrowsLinearly(t *testing.T) {
	needsBuild(t)
	p := mustLower(t, "uipass", uiPassSource)
	if !regexp.MustCompile(`(?m)^func pass\(n int\) int \{`).MatchString(p.mainGo()) {
		t.Fatalf("pass is not a plain Go function:\n%s", goFuncBody(t, p.mainGo(), "pass"))
	}
	m := newGoModuleDir(t)
	m.write("p/uipass/main.go", []byte(p.mainGo()))
	m.write("p/uipass/count_test.go", []byte(uiPassGoTest))
	exe := filepath.Join(m.root, "uipass.test")
	if out, ok := m.goCommand("test", "-c", "-trimpath", "-o", exe, "./p/uipass"); !ok {
		t.Fatalf("build the count: %s", firstLines(out, 30))
	}
	cmd := exec.Command(exe, "-test.run", "^TestCount$", "-test.count", "1")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := runProgram(t, cmd); err != nil {
		t.Fatalf("the count: %v\n%s", err, out.String())
	}
	got := map[int]uint64{}
	for _, x := range regexp.MustCompile(`mallocs (\d+) (\d+)`).FindAllStringSubmatch(out.String(), -1) {
		n, _ := strconv.Atoi(x[1])
		c, _ := strconv.ParseUint(x[2], 10, 64)
		got[n] = c
	}
	small, large := got[1000], got[2000]
	if small == 0 || large == 0 {
		t.Fatalf("no counts in:\n%s", out.String())
	}
	t.Logf("allocations: %d at 1000 nodes, %d at 2000", small, large)
	if large > 2*small+small/8 {
		t.Errorf("the UI pass grows faster than its nodes: %d allocations at 1000 nodes, %d at 2000 (bound %d)\n%s", small, large, 2*small+small/8, uiPassSource)
	}
}
