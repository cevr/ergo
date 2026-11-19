package mygo

import (
	"strconv"
	"strings"
	"testing"
)

// TestDesktopKeepsItsLayout is the kept layout's growth guard: on the
// shelf app (every row a node, in a Scroll), adding one row after N rows
// lays out the same number of boxes (desktop.LaidOut, the kernel's
// layout.Work) at N = 10 and N = 40, the new row and the boxes around it,
// not the tree. It kills the mutants that rebuild the tree at every paint
// or set every box's style again (hosts/mygo/testdata/mutants.json,
// GPk-*); hostagree's check holds the kept boxes to the rebuilt ones.
//
// It builds and runs a Go binary because the driver is Ergo source that
// calls Go (go.Foreign): hostagree is the program that runs it.
func TestDesktopKeepsItsLayout(t *testing.T) {
	bin, err := buildHostAgree()
	if err != nil {
		t.Fatal(err)
	}
	work := map[int]int{}
	for _, n := range []int{10, 40} {
		script := "shelf\n" + strings.Repeat("tap add\n", n) + "work\ntap add\nwork\n"
		cmd := bounded(bin)
		cmd.Stdin = strings.NewReader(script)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("n=%d: %v\n%s", n, err, out)
		}
		var works []int
		for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
			switch {
			case strings.HasPrefix(line, "ok "), line == "end":
			case strings.HasPrefix(line, "work "):
				k, err := strconv.Atoi(strings.TrimPrefix(line, "work "))
				if err != nil {
					t.Fatalf("n=%d: %q", n, line)
				}
				works = append(works, k)
			default:
				t.Fatalf("n=%d: %s", n, line)
			}
		}
		if len(works) != 2 {
			t.Fatalf("n=%d: %d work lines, want 2:\n%s", n, len(works), out)
		}
		if works[0] != works[1] {
			t.Errorf("n=%d: adding a row laid out %d boxes, then %d", n, works[0], works[1])
		}
		work[n] = works[1]
	}
	if work[10] != work[40] {
		t.Errorf("adding a row laid out %d boxes after 10 rows and %d after 40: the work grows with the tree", work[10], work[40])
	}
	// 40 rows are 160 boxes; the row and the path to the window are few.
	if work[40] >= 40 {
		t.Errorf("adding a row laid out %d boxes", work[40])
	}
	t.Logf("adding a row lays out %d boxes at 10 and at 40 rows", work[40])
}
