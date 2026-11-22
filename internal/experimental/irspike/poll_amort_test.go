package irspike

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRPollAmortBound is POLL-AMORT's law, counted: a stop is seen
// within lower.PollEvery back edges of a quiet loop nest, and at the next
// back edge of a loop that makes a call that can wait. For each seed of
// GeneratePoll it lowers the program to IR twice, amortized and with the
// reference lowering (every back edge checks), and runs each nestN's IR
// body in process at several n with pollEval, a small evaluator of the
// IR those bodies use, which counts back edges since the last Check. No
// stop is sent: the bound is the count of back edges between two Checks,
// which is when a stop already requested is seen.
//
//   - both lowerings give each nestN the same result;
//   - the reference checks at every back edge (at most 1 between Checks);
//   - the amortized lowering checks within PollEvery back edges, and at
//     every back edge of a loop whose body calls Console.Println;
//   - the amortized lowering checks less: a quiet nest of at least
//     4*PollEvery back edges makes fewer Checks than half its back edges.
//
// Mutants: poll-amort-reset-doubled (the count restarts at 2*PollEvery),
// poll-amort-counter-per-loop (each loop counts apart), poll-amort-loud
// (a loop with an impure call amortized) and poll-amort-off (no loop
// amortized).
// A failing seed prints the Ergo source, the function, n, the counts and
// the replay command.
func TestIRPollAmortBound(t *testing.T) {
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 6)
	amortized := 0
	for seed := first; seed < first+count; seed++ {
		text := GeneratePoll(seed)
		dir := t.TempDir()
		file := filepath.Join(dir, "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("seed %d: the generated program does not check: %v\n%s", seed, err, text)
		}
		amort := pollFuncs(t, u, "", seed, text) // the default lowering
		each := pollFuncs(t, u, "each", seed, text)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIRPollAmortBound ./internal/experimental/irspike"
		for f := 0; strings.Contains(text, fmt.Sprintf("func nest%d(", f)); f++ {
			name := fmt.Sprintf("nest%d", f)
			if amort[name] == nil || each[name] == nil {
				t.Fatalf("seed %d: %s has no IR body\n%s\n%s", seed, name, replay, text)
			}
			for _, n := range []int{1, 37, 2500} {
				a := runPoll(t, amort, name, n)
				r := runPoll(t, each, name, n)
				fail := func(format string, args ...any) {
					t.Fatalf("seed %d: %s(%d): %s\namortized: %+v\nreference: %+v\n%s\n%s", seed, name, n, fmt.Sprintf(format, args...), a, r, replay, text)
				}
				switch {
				case a.result != r.result:
					fail("the lowerings give different results")
				case r.maxGap > 1:
					fail("the reference runs %d back edges between two Checks", r.maxGap)
				case a.maxGap > lower.PollEvery:
					fail("the amortized lowering runs %d back edges between two Checks, past %d", a.maxGap, lower.PollEvery)
				case a.loudGap > 1:
					fail("a loop that calls Console.Println runs %d back edges between two Checks", a.loudGap)
				case !a.loud && a.edges >= 4*lower.PollEvery && 2*a.checks >= a.edges:
					fail("a quiet nest checks %d times in %d back edges: not amortized", a.checks, a.edges)
				}
				if !a.loud && a.edges >= 4*lower.PollEvery {
					amortized++
				}
			}
		}
	}
	if amortized == 0 {
		t.Fatal("no quiet nest ran 4*PollEvery back edges: the property checked no amortization")
	}
}

// pollFuncs lowers u to IR with pollAmortLowering mode ("" keeps the build's
// default, which must be the amortized one) and gives its functions'
// bodies by name.
func pollFuncs(t *testing.T, u lower.Unit, mode string, seed uint64, text string) map[string]*pollFunc {
	t.Helper()
	if mode != "" {
		defer lower.SetPollAmortLoweringForTest(mode)()
	}
	files, _, err := lower.LowerIR(u)
	if err != nil {
		t.Fatalf("seed %d: %v\n%s", seed, err, text)
	}
	var ir struct {
		Funcs []pollFunc `json:"funcs"`
	}
	for _, f := range files {
		if f.Path == lower.IRFileName {
			if err := json.Unmarshal(f.Data, &ir); err != nil {
				t.Fatal(err)
			}
		}
	}
	out := map[string]*pollFunc{}
	for i := range ir.Funcs {
		out[ir.Funcs[i].Name] = &ir.Funcs[i]
	}
	return out
}

type pollFunc struct {
	Name string           `json:"name"`
	Body []map[string]any `json:"body"`
}

// pollRun is what pollEval counted in one call.
type pollRun struct {
	result  int
	edges   int  // back edges run
	checks  int  // Checks run
	maxGap  int  // most back edges between two Checks
	loudGap int  // most back edges between two Checks at a back edge of a loop with an impure call
	loud    bool // some loop run has an impure call
}

// runPoll runs funcs[name](env, n) with pollEval.
func runPoll(t *testing.T, funcs map[string]*pollFunc, name string, n int) pollRun {
	t.Helper()
	e := &pollEval{t: t, funcs: funcs}
	v := e.call(name, []any{pollEnv{}, n})
	e.run.result = v.(int)
	return e.run
}

type pollEnv struct{}

// pollEval evaluates the IR of GeneratePoll's functions: int and bool
// locals, a Range literal, count and while loops, if, block, break,
// continue and return, calls of the program's functions, the task's
// Check (counted) and Console calls (no effect). Any other form fails the
// test, naming it.
type pollEval struct {
	t     *testing.T
	funcs map[string]*pollFunc
	run   pollRun
	gap   int
}

type pollFlow struct {
	k     string // "", "break", "continue", "ret"
	label string
	v     any
}

type pollFrame struct{ locals map[int]any }

func (e *pollEval) call(name string, args []any) any {
	f := e.funcs[name]
	if f == nil {
		e.t.Fatalf("pollEval: no IR body for %s", name)
	}
	fr := &pollFrame{locals: map[int]any{}}
	for i, a := range args {
		fr.locals[i] = a
	}
	fl := e.list(fr, f.Body)
	if fl.k != "ret" {
		e.t.Fatalf("pollEval: %s ends without a return", name)
	}
	return fl.v
}

func (e *pollEval) list(fr *pollFrame, ss []map[string]any) pollFlow {
	for _, s := range ss {
		if fl := e.stmt(fr, s); fl.k != "" {
			return fl
		}
	}
	return pollFlow{}
}

func stmts(v any) []map[string]any {
	l, _ := v.([]any)
	out := make([]map[string]any, len(l))
	for i, s := range l {
		out[i], _ = s.(map[string]any)
	}
	return out
}

func localOf(s map[string]any) int {
	f, _ := s["local"].(float64)
	return int(f)
}

func (e *pollEval) stmt(fr *pollFrame, s map[string]any) pollFlow {
	switch s["k"] {
	case "def", "set":
		v := e.expr(fr, s["x"])
		if op, _ := s["op"].(string); op != "" {
			v = pollBin(e.t, op, fr.locals[localOf(s)], v)
		}
		fr.locals[localOf(s)] = v
	case "var":
		fr.locals[localOf(s)] = 0
	case "expr":
		e.expr(fr, s["x"])
	case "if":
		if e.expr(fr, s["x"]).(bool) {
			return e.list(fr, stmts(s["then"]))
		}
		return e.list(fr, stmts(s["else"]))
	case "block":
		fl := e.list(fr, stmts(s["body"]))
		if fl.k == "break" && fl.label != "" && fl.label == s["label"] {
			return pollFlow{}
		}
		return fl
	case "break", "continue":
		l, _ := s["label"].(string)
		return pollFlow{k: s["k"].(string), label: l}
	case "ret":
		return pollFlow{k: "ret", v: e.expr(fr, s["x"])}
	case "count", "while":
		return e.loop(fr, s)
	default:
		e.t.Fatalf("pollEval: statement %v", s["k"])
	}
	return pollFlow{}
}

// impure reports a call in v other than a Check and a call of the
// program's functions: Console.Println.
func impure(v any) bool {
	switch v := v.(type) {
	case map[string]any:
		if v["k"] == "mcall" && v["name"] != "Check" {
			return true
		}
		for _, x := range v {
			if impure(x) {
				return true
			}
		}
	case []any:
		for _, x := range v {
			if impure(x) {
				return true
			}
		}
	}
	return false
}

func (e *pollEval) loop(fr *pollFrame, s map[string]any) pollFlow {
	label, _ := s["label"].(string)
	body := stmts(s["body"])
	loud := impure(s["body"])
	if loud {
		e.run.loud = true
	}
	i, end := 0, 0
	if s["k"] == "count" {
		i, end = e.expr(fr, s["x"]).(int), e.expr(fr, s["y"]).(int)
	}
	for {
		if s["k"] == "count" {
			if i >= end {
				return pollFlow{}
			}
			fr.locals[localOf(s)] = i
		} else if s["x"] != nil && !e.expr(fr, s["x"]).(bool) {
			return pollFlow{}
		}
		fl := e.list(fr, body)
		switch {
		case fl.k == "ret":
			return fl
		case fl.k == "break" && (fl.label == "" || fl.label == label):
			return pollFlow{}
		case fl.k == "break":
			return fl
		case fl.k == "continue" && fl.label != "" && fl.label != label:
			return fl
		}
		// the back edge
		e.run.edges++
		e.gap++
		if loud {
			e.run.loudGap = max(e.run.loudGap, e.gap)
		}
		e.run.maxGap = max(e.run.maxGap, e.gap)
		i++
	}
}

func (e *pollEval) expr(fr *pollFrame, v any) any {
	x, _ := v.(map[string]any)
	if x == nil {
		e.t.Fatalf("pollEval: no expression")
	}
	switch x["k"] {
	case "local":
		f, _ := x["i"].(float64)
		return fr.locals[int(f)]
	case "const":
		s := x["v"].(string)
		if s == "true" || s == "false" {
			return s == "true"
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			e.t.Fatalf("pollEval: constant %q", s)
		}
		return n
	case "bin":
		op := x["op"].(string)
		a := e.expr(fr, x["x"])
		if op == "&&" && !a.(bool) || op == "||" && a.(bool) {
			return a
		}
		return pollBin(e.t, op, a, e.expr(fr, x["y"]))
	case "un":
		a := e.expr(fr, x["x"])
		switch x["op"] {
		case "!":
			return !a.(bool)
		case "-":
			return -a.(int)
		}
	case "lit":
		keys, _ := x["keys"].([]any)
		elems, _ := x["elems"].([]any)
		m := map[string]any{}
		for i, k := range keys {
			m[k.(string)] = e.expr(fr, elems[i])
		}
		return m
	case "field":
		if m, ok := e.expr(fr, x["x"]).(map[string]any); ok {
			return m[x["name"].(string)]
		}
		return pollEnv{} // env.task, env.Console
	case "call":
		var args []any
		as, _ := x["args"].([]any)
		for _, a := range as {
			args = append(args, e.expr(fr, a))
		}
		return e.call(x["name"].(string), args)
	case "mcall":
		if x["name"] == "Check" {
			e.run.checks++
			e.gap = 0
		}
		return nil // Console.Println: no effect here
	}
	e.t.Fatalf("pollEval: expression %v", x["k"])
	return nil
}

func pollBin(t *testing.T, op string, a, b any) any {
	switch a := a.(type) {
	case int:
		b := b.(int)
		switch op {
		case "+":
			return a + b
		case "-":
			return a - b
		case "*":
			return a * b
		case "%":
			return a % b
		case "/":
			return a / b
		case "==":
			return a == b
		case "!=":
			return a != b
		case "<":
			return a < b
		case "<=":
			return a <= b
		case ">":
			return a > b
		case ">=":
			return a >= b
		}
	case bool:
		switch op {
		case "&&":
			return a && b.(bool)
		case "||":
			return a || b.(bool)
		case "==":
			return a == b.(bool)
		}
	}
	t.Fatalf("pollEval: %T %s", a, op)
	return nil
}
