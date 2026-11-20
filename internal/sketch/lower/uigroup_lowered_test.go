package lower

import (
	"regexp"
	"testing"
)

// groupedRows is NS1's rows bench cut to its component calls: row(f, x)
// with an int (line 21), card(f, s) with a record whose read fields the
// group compares (line 23).
const groupedRows = `import ui

type state { rows []int, tick int }

func row(f *ui.Frame[int], x int) {
	ui.Row(f, ui.Plain()) {
		ui.Text(f, "row \(x)")
		ui.Pressable(f, "go", x) {
			ui.Text(f, "go")
		}
	}
}

func card(f *ui.Frame[int], s state) {
	ui.Text(f, "tick \(s.tick)")
}

func view(f *ui.Frame[int], s state) {
	ui.Column(f, ui.Plain()) {
		ui.Each(f, s.rows, func(x int) ui.Key { ui.ByInt(x) }) func(x int) {
			row(f, x)
		}
		card(f, s)
	}
}

func main() {
	let mut f = ui.NewFrame[int]()
	let _ = ui.Render(&f, view, state{rows: []int{1, 2, 3}, tick: 0})
}
`

// TestComponentCallsLowerToGroup is GROUP-OFF's guard, in process and in
// the fast tier: a component call (stdlib.md, UI 13) lowers to a call of
// its frame's group, under the source build and under the IR build, the
// one a release build runs. The shape is the spec here: a component call
// that is no group call writes every node of every pass, which no
// program's output shows (the full-tier TestUISkipAgrees and irspike's
// TestIRUISkipMatchesSource see it only by running built programs). From
// 2026-11-18 to the g4 rebuild the IR build had no group lowering, and a
// built program's components never skipped (UI-COMPILE
// group-regression.md); nothing in the fast tier went red.
//
// Mutants it kills (testdata/mutants.json): ui-skip-never,
// group-lawed-never; irspike's ly-irgroup-off.
func TestComponentCallsLowerToGroup(t *testing.T) {
	u := factsUnit(t, groupedRows)
	o := mustLowerUnit(t, "uigroup_lowered", u, false)
	// A body the IR cannot take yet keeps its source body (rep.Fallback,
	// another unit's gap); the views here are checked by their Go below.
	irFiles, _, err := LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	builds := map[string][]File{"source": o.files, "IR": irFiles}
	for _, build := range []string{"source", "IR"} {
		var text string
		for _, f := range builds[build] {
			if f.Path == "main.go" {
				text = string(f.Data)
			}
		}
		if text == "" {
			t.Fatalf("the %s build has no main.go", build)
		}
		for _, line := range []string{"21", "23"} {
			at := regexp.MustCompile(`:` + line + `:\d+\*/group(By)?\[`)
			if !at.MatchString(text) {
				t.Errorf("the %s build does not lower the component call at line %s through group or groupBy", build, line)
			}
		}
	}
}
