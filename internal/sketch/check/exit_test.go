package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// exitShape is one generated signature of a machine's exit function.
type exitShape struct {
	tparam bool   // the function has a type parameter
	param  string // its parameter type
	result string // its result shape (see exitResults)
	own    string // its own rows: "", "needs Svc", "fails Boom"
	runSvc bool   // the runner's caller lists Svc in its row
}

// exitResults are the result shapes, each with a body of that type and
// whether the rule admits the shape, and whether its action needs Svc.
var exitResults = []struct {
	name, typ, body string
	ok, needsSvc    bool
}{
	{"none", "Option[func()]", "None", true, false},
	{"plain", "Option[func()]", "Some{func() {}}", true, false},
	{"needs", "Option[func() needs Svc]", "Some{func() needs Svc { Svc.Note(1) }}", true, true},
	{"fails", "Option[func() fails Boom]", "Some{func() fails Boom { Boom{} }}", false, false},
	{"param", "Option[func(n int)]", "Some{func(n int) {}}", false, false},
	{"result", "Option[func() int]", "Some{func() int { 1 }}", false, false},
	{"bare", "func()", "func() {}", false, false},
	{"value", "Option[int]", "None", false, false},
}

// exitSource is a program with machine M whose exit is the function of
// shape s, its runner run (actor.StartMachine) and a fold (machines.Run).
func exitSource(s exitShape, res int) string {
	r := exitResults[res]
	tp := ""
	if s.tparam {
		tp = "[T]"
	}
	own := ""
	if s.own != "" {
		own = " " + s.own
	}
	body := r.body
	switch s.own {
	case "needs Svc":
		body = "Svc.Note(0)\n\t" + body
	case "fails Boom":
		body = "if s.N < 0 {\n\t\treturn Boom{}\n\t}\n\t" + body
	}
	runRow := "Scheduler"
	if s.runSvc {
		runRow = "Scheduler | Svc"
	}
	return `import actor
import machines

type Count { N int }
type Tick { K int }
type Boom {}
type Svc { Note(n int) }

func step(c Count, t Tick) Decision[Count, int] { Stay[Count]{Next: c} }

func leave` + tp + `(s ` + s.param + `) ` + r.typ + own + ` {
	` + body + `
}

type M machine[Count, Tick] {
	initial: Count{N: 0},
	step:    step,
	exit:    leave,
}

func run() needs ` + runRow + ` {
	let a = actor.StartMachine(M{}, 1, func(o int) {})
	a.Stop()
}

func fold() fails machines.Rejected {
	let _ = try machines.Run(M{}, []Tick{Tick{K: 1}})
}

func main() {}
`
}

// TestMachineExitRule is the rule of a machine's exit entry (FK9-g1;
// runtime.md, Machines and actors 12a) over every generated signature of
// the exit function: its type parameters, its parameter type, its result
// shape and its own rows. The entry is admitted only for func(S)
// Option[func() needs R] with no type parameters, no rows of its own and
// no error row in the action; a refused entry is reported at the exit
// entry. The action's row R is the machine's W: actor.StartMachine needs
// it (a runner whose row lacks Svc is refused there when the action needs
// Svc), and machines.Run, a fold that runs no exit action, never does.
// Mutants: g1-exit-own-rows, g1-exit-fails-admitted, g1-exit-row-uncharged
// (check/testdata/mutants.json).
func TestMachineExitRule(t *testing.T) {
	n := 0
	for _, tparam := range []bool{false, true} {
		for _, param := range []string{"Count", "Tick"} {
			for res := range exitResults {
				for _, own := range []string{"", "needs Svc", "fails Boom"} {
					for _, runSvc := range []bool{false, true} {
						s := exitShape{tparam: tparam, param: param, result: exitResults[res].name, own: own, runSvc: runSvc}
						src := exitSource(s, res)
						_, diags := check.CheckSource(src)
						lines := lineOf(src)
						exitLine, runLine, foldLine := lines("\texit:    leave,"), lines("\tlet a = actor.StartMachine"), lines("\tlet _ = try machines.Run")
						admitted := !tparam && param == "Count" && own == "" && exitResults[res].ok
						var atExit, atRun, atFold, elsewhere []string
						for _, d := range diags {
							text := fmt.Sprintf("%d:%d: %s %s", d.Line, d.Column, d.Code, d.Message)
							switch {
							case d.Line == exitLine:
								atExit = append(atExit, text)
							case d.Line == runLine:
								atRun = append(atRun, text)
							case d.Line == foldLine:
								atFold = append(atFold, text)
							default:
								elsewhere = append(elsewhere, text)
							}
						}
						n++
						fail := func(why string) {
							t.Errorf("%+v: %s\nat exit %v\nat run %v\nat fold %v\nelsewhere %v\n%s", s, why, atExit, atRun, atFold, elsewhere, src)
						}
						if admitted {
							if len(atExit) > 0 || len(atFold) > 0 || len(elsewhere) > 0 {
								fail("an admitted exit has diagnostics")
								continue
							}
							if wantRun := exitResults[res].needsSvc && !runSvc; (len(atRun) > 0) != wantRun {
								fail(fmt.Sprintf("the runner's diagnostics: want refused %v (the action's row is the machine's W)", wantRun))
							}
							continue
						}
						if len(atExit) == 0 {
							fail("a refused exit has no diagnostic at the exit entry")
						}
					}
				}
			}
		}
	}
	if n == 0 {
		t.Fatal("no shape generated")
	}
}

// TestMachineExitDiagnostics holds the text and place of the diagnostics
// of a machine's exit entry (FK9-g1; grammar.md, Declarations 6): an exit
// set twice, an exit after a law, an entry name that is none (the list
// names exit), and an exit function of the wrong type, whose text gives
// the wanted type.
func TestMachineExitDiagnostics(t *testing.T) {
	const head = "type S = Idle{} | Busy{}\ntype E = Ping{}\nfunc step(s S, e E) Decision[S, int] { Ignore }\nfunc leave(s S) Option[func()] { None }\n"
	rows := []struct {
		name, src, code, msg string
		line                 int
	}{
		{"set twice", head + "type M machine[S, E] {\n\tinitial: Idle,\n\tstep: step,\n\texit: leave,\n\texit: leave,\n}\nfunc main() {}\n",
			"S001", "exit is set twice", 9},
		{"after a law", head + "type M machine[S, E] {\n\tinitial: Idle,\n\tstep: step,\n\tlaw L reaches Busy,\n\texit: leave,\n}\nfunc main() {}\n",
			"S001", "a machine's entries come before its laws", 9},
		{"unknown entry", head + "type M machine[S, E] {\n\tinitial: Idle,\n\tstep: step,\n\tleave: leave,\n}\nfunc main() {}\n",
			"S001", `expected initial, step, after, work or exit, found "leave"`, 8},
		{"wrong type", head + "func other(s S) Option[int] { None }\ntype M machine[S, E] {\n\tinitial: Idle,\n\tstep: step,\n\texit: other,\n}\nfunc main() {}\n",
			"S108", "the exit other of machine M must have the type func(S) Option[func() needs R], with no error or capability row of its own and no error row in the action: R is the action's capability row, charged to the machine (runtime.md, Machines and actors 12a)", 9},
	}
	for _, r := range rows {
		_, diags := check.CheckSource(r.src)
		found := false
		for _, d := range diags {
			if d.Code == r.code && d.Message == r.msg && d.Line == r.line {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: want %s %q at line %d, got %v", r.name, r.code, r.msg, r.line, diags)
		}
	}
}

// lineOf gives the 1-based line of src that starts with prefix.
func lineOf(src string) func(prefix string) int {
	ls := strings.Split(src, "\n")
	return func(prefix string) int {
		for i, l := range ls {
			if strings.HasPrefix(l, prefix) {
				return i + 1
			}
		}
		panic("no line " + prefix)
	}
}
