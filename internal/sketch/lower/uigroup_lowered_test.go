package lower

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// groupedRows is NS1's rows bench in the bench's shape: three component
// calls, row(f, x) with an int (line 16), list(f, s) and card(f, s) with a
// record (lines 26 and 27). std ui itself calls no group: every group call
// in the lowered program is one of these.
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

func list(f *ui.Frame[int], s state) {
	ui.Each(f, s.rows, func(x int) ui.Key { ui.ByInt(x) }) func(x int) {
		row(f, x)
	}
}

func card(f *ui.Frame[int], s state) {
	ui.Text(f, "tick \(s.tick)")
}

func view(f *ui.Frame[int], s state) {
	ui.Column(f, ui.Plain()) {
		list(f, s)
		card(f, s)
	}
}

func main() {
	let mut f = ui.NewFrame[int]()
	let _ = ui.Render(&f, view, state{rows: []int{1, 2, 3}, tick: 0})
}
`

// groupedCalls are the component calls of groupedRows, by line.
var groupedCalls = []string{"16", "26", "27"}

// TestComponentCallsLowerToGroup is GROUP-OFF's guard, in process and in
// the fast tier: a component call (stdlib.md, UI 13) lowers to a call of
// its frame's group, under the source build (by the line of each call)
// and under the IR build, the one a release build runs (a group or
// groupBy call in the IR for each component call). The shape is the spec
// here: a component call that is no group call writes every node of every
// pass, which no program's output shows (the full-tier TestUISkipAgrees
// and irspike's TestIRUISkipMatchesSource see it only by running built
// programs). Until IRG-MAIN reached main (the I-FLIP g4 rebuild) the IR
// build had no group lowering, and a built program's components never
// skipped (UI-COMPILE r/group-regression.md, GROUP-OFF); nothing in the
// fast tier went red.
//
// Mutants it kills (testdata/mutants.json): ui-skip-never,
// group-lawed-never; irspike's ly-irgroup-off.
func TestComponentCallsLowerToGroup(t *testing.T) {
	u := factsUnit(t, groupedRows)
	o := mustLowerUnit(t, "uigroup_lowered", u, false)
	text := ""
	for _, f := range o.files {
		if f.Path == "main.go" {
			text = string(f.Data)
		}
	}
	for _, line := range groupedCalls {
		if !regexp.MustCompile(`:` + line + `:\d+\*/group(By)?\[`).MatchString(text) {
			var near []string
			for _, l := range strings.Split(text, "\n") {
				if strings.Contains(l, ":"+line+":") {
					near = append(near, strings.TrimSpace(l))
				}
			}
			t.Errorf("the source build does not lower the component call at line %s through group or groupBy; its Go there:\n%s", line, strings.Join(near, "\n"))
		}
	}
	// A body the IR cannot take yet keeps its source body (another unit's
	// gap); the IR's own calls are what is counted.
	files, _, err := LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	groups := 0
	for _, f := range files {
		if f.Path != IRFileName {
			continue
		}
		var v any
		if err := json.Unmarshal(f.Data, &v); err != nil {
			t.Fatal(err)
		}
		groups += irGroupCalls(v)
	}
	if groups < len(groupedCalls) {
		t.Errorf("the IR build calls group or groupBy %d times, want one for each of the %d component calls", groups, len(groupedCalls))
	}
}

// irGroupCalls counts the calls of group and groupBy in a decoded IR file.
func irGroupCalls(v any) int {
	n := 0
	switch v := v.(type) {
	case map[string]any:
		name, _ := v["name"].(string)
		name = strings.TrimRightFunc(name, func(r rune) bool { return r == '_' || r >= '0' && r <= '9' })
		if v["k"] == "call" && (name == "group" || name == "groupBy") {
			n++
		}
		for _, x := range v {
			n += irGroupCalls(x)
		}
	case []any:
		for _, x := range v {
			n += irGroupCalls(x)
		}
	}
	return n
}
