package lower

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// groupedRows is NS1's rows bench in the bench's shape, with four
// component calls: row(f, x) with an int in list's Each (line 16), list(f, s)
// and card(f, s) with a record (lines 26 and 27), and row(f, x) in an Each
// written inline in view's Column block (line 29), which skips as the
// separate list does (UI-COMPILE REGION-GROUPS asked). std ui itself calls
// no group: every group call in the lowered program is one of these.
const groupedRows = `import ui

type state { rows []int, tick int }

func row(f mut ui.Frame[int], x int) {
	ui.Row(f, ui.Plain()) {
		ui.Text(f, "row \(x)")
		ui.Pressable(f, "go", x) {
			ui.Text(f, "go")
		}
	}
}

func list(f mut ui.Frame[int], s state) {
	ui.Each(f, s.rows, func(x int) ui.Key { ui.ByInt(x) }) func(x int) {
		row(f, x)
	}
}

func card(f mut ui.Frame[int], s state) {
	ui.Text(f, "tick \(s.tick)")
}

func view(f mut ui.Frame[int], s state) {
	ui.Column(f, ui.Plain()) {
		list(f, s)
		card(f, s)
		ui.Each(f, s.rows, func(x int) ui.Key { ui.ByInt(x + 100) }) func(x int) {
			row(f, x)
		}
	}
}

func main() {
	let mut f = ui.NewFrame[int]()
	let _ = ui.Render(&f, view, state{rows: []int{1, 2, 3}, tick: 0})
}
`

// groupedCalls are the component calls of groupedRows, by line; recordCalls
// those with a record argument.
var (
	groupedCalls = []string{"16", "26", "27", "29"}
	recordCalls  = []string{"26", "27"}
)

// TestComponentCallsLowerToGroup is GROUP-OFF's guard, in process and in
// the fast tier: a component call (stdlib.md, UI 13) lowers to a call of
// its frame's group under the IR build, the one a release build runs (a
// group or groupBy call in the IR for each component call), and under the
// source build for the calls with a record argument (by the line of each
// call). The source build lowers row(f, x), an int argument, as a plain
// call (measured, GROUP-OFF state.md): a body the IR gives back to the
// source build loses its skip there, a follow-up. The shape is the spec
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
	// The group functions are named as the lowering finds them, by shape
	// and law (GroupFuncsForTest), never by their spelling.
	names := groupFuncNames(t, u)
	var quoted []string
	for n := range names {
		quoted = append(quoted, regexp.QuoteMeta(n))
	}
	for _, line := range recordCalls {
		if !regexp.MustCompile(`:` + line + `:\d+\*/(` + strings.Join(quoted, "|") + `)\[`).MatchString(text) {
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
		groups += irGroupCalls(v, names)
	}
	if groups < len(groupedCalls) {
		t.Errorf("the IR build calls group or groupBy %d times, want one for each of the %d component calls", groups, len(groupedCalls))
	}
}

// groupFuncNames is the names of the group functions of the frame
// packages u imports, as the lowering finds them (GroupFuncsForTest).
func groupFuncNames(t *testing.T, u Unit) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	seen := map[*check.Package]bool{}
	var walk func(u Unit)
	walk = func(u Unit) {
		if u.Package == nil || seen[u.Package] {
			return
		}
		seen[u.Package] = true
		for _, g := range GroupFuncsForTest(u.Package) {
			names[g.Name] = true
		}
		for _, i := range u.Imports {
			walk(i)
		}
	}
	walk(u)
	// The frame packages of the program's components: the package of each
	// pointer parameter's named type (a component takes its frame first).
	for _, f := range u.Package.Funcs {
		if f.Sig == nil || len(f.Sig.Params) == 0 {
			continue
		}
		if p, ok := f.Sig.Params[0].Type.(*check.Pointer); ok {
			if n, ok := p.Elem.(*check.Named); ok && n.Obj != nil && n.Obj.Pkg != nil && !seen[n.Obj.Pkg] {
				seen[n.Obj.Pkg] = true
				for _, g := range GroupFuncsForTest(n.Obj.Pkg) {
					names[g.Name] = true
				}
			}
		}
	}
	if len(names) == 0 {
		t.Fatal("no frame package of the program declares a lawed group")
	}
	return names
}

// irGroupCalls counts the calls of the group functions names in a decoded
// IR file.
func irGroupCalls(v any, names map[string]bool) int {
	n := 0
	switch v := v.(type) {
	case map[string]any:
		name, _ := v["name"].(string)
		name = strings.TrimRightFunc(name, func(r rune) bool { return r == '_' || r >= '0' && r <= '9' })
		if v["k"] == "call" && names[name] {
			n++
		}
		for _, x := range v {
			n += irGroupCalls(x, names)
		}
	case []any:
		for _, x := range v {
			n += irGroupCalls(x, names)
		}
	}
	return n
}
