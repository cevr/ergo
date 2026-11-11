package lower

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
)

// symArg is an argument of a generated law call: a parameter (param >= 0),
// a constant string or bool, or a computed join of string parameters, each
// optionally wrapped in the extern function.
type symArg struct {
	param int
	str   *string
	bool  *bool
	join  []symPart
}

type symPart struct {
	param int
	up    bool
}

type symCall struct {
	op   string
	args []symArg
}

type symLaw struct {
	params   []string // "string" or "bool"
	lhs, rhs []symCall
	guard    bool
	// want is "proved" or "unproved" for a planted law, "" for a drawn one.
	want string
	// row is a text the not-proved line must hold, for the three rows.
	row string
}

// symOps are the generated provider's operations: their parameter types,
// their Ergo bodies (receiver t) and the model's semantics.
var symOps = map[string]struct {
	params []string
	body   string
	result bool
}{
	"SetC":  {params: []string{"bool"}, body: "{ t.c = b0 }"},
	"Raw":   {params: []string{"string"}, body: "{ t.out = t.out + s0 }"},
	"Sep":   {body: "{ if t.c { t.out = t.out + \",\" } }"},
	"Up":    {body: "{ t.out = up(t.out) }"},
	"Wrap":  {params: []string{"string"}, body: "{ t.out = t.out + up(s0) }"},
	"Empty": {body: "{ t.c = match t.out {\ncase \"\": true\ncase _: t.c\n} }"},
	"Dup":   {body: "{ t.out = t.out + t.out }"},
	"Long":  {body: "{ t.c = len(t.out) > 1 }"},
	"C":     {result: true},
}

var symOpNames = []string{"SetC", "Raw", "Sep", "Up", "Wrap", "Empty", "Dup", "Long", "C"}

// symState is the model's provider state.
type symState struct {
	c   bool
	out string
}

// modelRun runs calls from st with the law's arguments, independently of the
// evaluator: the extern is strings.ToUpper.
func (l symLaw) modelRun(st symState, strs []string, bools []bool, calls []symCall) (symState, []bool) {
	var results []bool
	str := func(a symArg) string {
		switch {
		case a.param >= 0:
			return strs[a.param]
		case a.str != nil:
			return *a.str
		}
		var b strings.Builder
		for _, p := range a.join {
			s := strs[p.param]
			if p.up {
				s = strings.ToUpper(s)
			}
			b.WriteString(s)
		}
		return b.String()
	}
	for _, c := range calls {
		switch c.op {
		case "SetC":
			if c.args[0].bool != nil {
				st.c = *c.args[0].bool
			} else {
				st.c = bools[c.args[0].param]
			}
		case "Raw":
			st.out += str(c.args[0])
		case "Sep":
			if st.c {
				st.out += ","
			}
		case "Up":
			st.out = strings.ToUpper(st.out)
		case "Wrap":
			st.out += strings.ToUpper(str(c.args[0]))
		case "Empty":
			if st.out == "" {
				st.c = true
			}
		case "Dup":
			st.out += st.out
		case "Long":
			st.c = len(st.out) > 1
		case "C":
			results = append(results, st.c)
		}
	}
	return st, results
}

// modelHolds runs the law on every state and argument over a finite string
// set: the exhaustive finite instance.
func (l symLaw) modelHolds() (bool, string) {
	set := []string{"", "a", "b", "ab", ","}
	n := 2 * len(set)
	for _, p := range l.params {
		if p == "string" {
			n *= len(set)
		} else {
			n *= 2
		}
	}
	for k := range n {
		r := k
		st := symState{c: r%2 == 1}
		r /= 2
		st.out = set[r%len(set)]
		r /= len(set)
		strs := make([]string, len(l.params))
		bools := make([]bool, len(l.params))
		for i, p := range l.params {
			if p == "string" {
				strs[i] = set[r%len(set)]
				r /= len(set)
			} else {
				bools[i] = r%2 == 1
				r /= 2
			}
		}
		if l.guard && !st.c {
			continue
		}
		ls, lr := l.modelRun(st, strs, bools, l.lhs)
		rs, rr := l.modelRun(st, strs, bools, l.rhs)
		if ls != rs || fmt.Sprint(lr) != fmt.Sprint(rr) {
			return false, fmt.Sprintf("state %+v, strings %q, bools %v: left %+v %v, right %+v %v", st, strs, bools, ls, lr, rs, rr)
		}
	}
	return true, ""
}

func (a symArg) source(params []string) string {
	switch {
	case a.param >= 0:
		return params[a.param]
	case a.str != nil:
		return strconv.Quote(*a.str)
	case a.bool != nil:
		return strconv.FormatBool(*a.bool)
	}
	var parts []string
	for _, p := range a.join {
		s := params[p.param]
		if p.up {
			s = "up(" + s + ")"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " + ")
}

// source is the law's operation type, provider and functions, suffixed i.
func (l symLaw) source(i int) string {
	sfx := strconv.Itoa(i)
	var names []string
	for k := range l.params {
		names = append(names, fmt.Sprintf("p%d", k))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "type T%s { c bool, out string }\n", sfx)
	for _, name := range symOpNames {
		op := symOps[name]
		if op.result {
			fmt.Fprintf(&b, "func %s%s(t T%s) bool { t.c }\n", name, sfx, sfx)
			continue
		}
		fmt.Fprintf(&b, "func %s%s(t *T%s", name, sfx, sfx)
		for k, p := range op.params {
			fmt.Fprintf(&b, ", %c%d %s", p[0], k, p)
		}
		fmt.Fprintf(&b, ") %s\n", op.body)
	}
	fmt.Fprintf(&b, "type Ops%s {\n", sfx)
	for _, name := range symOpNames {
		op := symOps[name]
		var ps []string
		for k, p := range op.params {
			ps = append(ps, fmt.Sprintf("%c%d %s", p[0], k, p))
		}
		res := ""
		if op.result {
			res = " bool"
		}
		fmt.Fprintf(&b, "\t%s%s(%s)%s,\n", name, sfx, strings.Join(ps, ", "), res)
	}
	var decl []string
	for k, p := range l.params {
		decl = append(decl, names[k]+" "+p)
	}
	side := func(calls []symCall) string {
		var stmts []string
		for _, c := range calls {
			var args []string
			for _, a := range c.args {
				args = append(args, a.source(names))
			}
			stmts = append(stmts, c.op+sfx+"("+strings.Join(args, ", ")+")")
		}
		if len(stmts) == 0 {
			return "{ }"
		}
		return "{ " + strings.Join(stmts, "; ") + " }"
	}
	guard := ""
	if l.guard {
		guard = " when C" + sfx + "() == true"
	}
	fmt.Fprintf(&b, "\tlaw L(%s)%s %s == %s,\n}\n", strings.Join(decl, ", "), guard, side(l.lhs), side(l.rhs))
	return b.String()
}

func par(i int) symArg { return symArg{param: i} }
func strArg(s string) symArg {
	return symArg{param: -1, str: &s}
}
func joinArg(parts ...symPart) symArg { return symArg{param: -1, join: parts} }
func call(op string, args ...symArg) symCall {
	return symCall{op: op, args: args}
}

// plantedSymLaws are the true laws a symbolic run proves and the false laws
// (false only for some string values, or by a read of the field) it must
// never prove.
func plantedSymLaws() []symLaw {
	s2 := []string{"string", "string"}
	return []symLaw{
		{params: s2, lhs: []symCall{call("Raw", par(0)), call("Raw", par(1))}, rhs: []symCall{call("Raw", joinArg(symPart{param: 0}, symPart{param: 1}))}, want: "proved"},
		{params: []string{"bool", "string"}, lhs: []symCall{call("SetC", par(0)), call("Raw", par(1))}, rhs: []symCall{call("Raw", par(1)), call("SetC", par(0))}, want: "proved"},
		{params: s2, lhs: []symCall{call("Wrap", par(0)), call("Raw", par(1))}, rhs: []symCall{call("Raw", joinArg(symPart{param: 0, up: true}, symPart{param: 1}))}, want: "proved"},
		{params: []string{"bool", "bool"}, lhs: []symCall{call("SetC", par(0)), call("SetC", par(1))}, rhs: []symCall{call("SetC", par(1))}, want: "proved"},
		{guard: true, lhs: []symCall{call("Sep")}, rhs: []symCall{call("Raw", strArg(","))}, want: "proved"},
		{params: []string{"string"}, lhs: []symCall{call("C"), call("Raw", par(0))}, rhs: []symCall{call("Raw", par(0)), call("C")}, want: "proved"},
		// A swap across a read of c.
		{params: []string{"bool"}, lhs: []symCall{call("SetC", par(0)), call("Sep")}, rhs: []symCall{call("Sep"), call("SetC", par(0))}, want: "unproved"},
		// A join in the wrong order: true only when a + b == b + a.
		{params: s2, lhs: []symCall{call("Raw", par(0)), call("Raw", par(1))}, rhs: []symCall{call("Raw", joinArg(symPart{param: 1}, symPart{param: 0}))}, want: "unproved", row: "the sides end in different terms"},
		// An append that reads the field: true only when a is empty.
		{params: []string{"string"}, lhs: []symCall{call("Dup"), call("Raw", par(0))}, rhs: []symCall{call("Raw", par(0)), call("Dup")}, want: "unproved"},
		// The extern applied to different arguments.
		{params: s2, lhs: []symCall{call("Wrap", par(0)), call("Wrap", par(1))}, rhs: []symCall{call("Wrap", par(1)), call("Wrap", par(0))}, want: "unproved"},
		// A write dropped: true only when a is empty.
		{params: []string{"string"}, lhs: []symCall{call("Raw", par(0))}, want: "unproved"},
		// A branch on the symbol: false only at the empty string.
		{lhs: []symCall{call("Empty")}, want: "unproved", row: "Empty%s branches on t.out at "},
		{lhs: []symCall{call("Long")}, want: "unproved", row: "Long%s computes with t.out at "},
		// Without its guard, Sep is no comma.
		{lhs: []symCall{call("Sep")}, rhs: []symCall{call("Raw", strArg(","))}, want: "unproved"},
	}
}

// drawSymLaw draws a law of 1-3 left calls and 0-3 right calls.
func drawSymLaw(rng *rand.Rand) symLaw {
	var l symLaw
	draw := func(left bool, used map[int]bool) symCall {
		name := symOpNames[rng.Intn(len(symOpNames))]
		c := symCall{op: name}
		for _, p := range symOps[name].params {
			if left {
				l.params = append(l.params, p)
				c.args = append(c.args, par(len(l.params)-1))
				continue
			}
			var same []int
			for k, q := range l.params {
				if q == p && !used[k] {
					same = append(same, k)
				}
			}
			switch {
			case p == "bool" && (len(same) == 0 || rng.Intn(3) == 0):
				v := rng.Intn(2) == 0
				c.args = append(c.args, symArg{param: -1, bool: &v})
			case p == "bool":
				k := same[rng.Intn(len(same))]
				used[k] = true
				c.args = append(c.args, par(k))
			case len(same) > 0 && rng.Intn(2) == 0:
				k := same[rng.Intn(len(same))]
				used[k] = true
				c.args = append(c.args, par(k))
			default:
				var strs []int
				for k, q := range l.params {
					if q == "string" && !used[k] {
						strs = append(strs, k)
					}
				}
				if len(strs) == 0 {
					c.args = append(c.args, strArg([]string{"", "a", ","}[rng.Intn(3)]))
					break
				}
				// A value passes through once (linear): each part is a
				// parameter not used yet.
				rng.Shuffle(len(strs), func(i, j int) { strs[i], strs[j] = strs[j], strs[i] })
				var parts []symPart
				for _, k := range strs[:min(len(strs), 1+rng.Intn(2))] {
					used[k] = true
					parts = append(parts, symPart{param: k, up: rng.Intn(3) == 0})
				}
				c.args = append(c.args, joinArg(parts...))
			}
		}
		return c
	}
	for range 1 + rng.Intn(3) {
		l.lhs = append(l.lhs, draw(true, nil))
	}
	l.guard = rng.Intn(4) == 0
	if len(l.lhs) > 1 && rng.Intn(2) == 0 {
		// Derive the right side from the left: a swap of two neighbors, or
		// a join of a Raw or Wrap with the Raw after it. Many such laws are
		// true, so the model checks proofs, not only refusals.
		l.rhs = append([]symCall(nil), l.lhs...)
		i := rng.Intn(len(l.rhs) - 1)
		a, b := l.rhs[i], l.rhs[i+1]
		if (a.op == "Raw" || a.op == "Wrap") && b.op == "Raw" && rng.Intn(2) == 0 {
			joined := call("Raw", joinArg(symPart{param: a.args[0].param, up: a.op == "Wrap"}, symPart{param: b.args[0].param}))
			l.rhs = append(append(l.rhs[:i:i], joined), l.rhs[i+2:]...)
		} else {
			l.rhs[i], l.rhs[i+1] = b, a
		}
		return l
	}
	used := map[int]bool{}
	for range rng.Intn(4) {
		c := draw(false, used)
		if len(l.lhs) == 1 && c.op == l.lhs[0].op {
			continue // an unfold that calls itself is S108
		}
		l.rhs = append(l.rhs, c)
	}
	return l
}

// TestSymbolicProofsSound checks symbolic runs (eval.ProveSymbolic, through
// the test build's runner) against an independent model on a finite
// instance: a proved law agrees on every state and argument over a string
// set, the planted false laws are never proved and the planted true ones
// are. It reads the lowered test build's text in process; no Go is built.
func TestSymbolicProofsSound(t *testing.T) {
	seed := int64(1)
	if s := os.Getenv("ERGO_SYMBOLIC_SEED"); s != "" {
		var err error
		if seed, err = strconv.ParseInt(s, 10, 64); err != nil {
			t.Fatal(err)
		}
	}
	rng := rand.New(rand.NewSource(seed))
	laws := plantedSymLaws()
	for range 24 {
		laws = append(laws, drawSymLaw(rng))
	}
	var src strings.Builder
	src.WriteString("extern go strings.ToUpper func up(s string) string\n")
	for i, l := range laws {
		src.WriteString(l.source(i))
	}
	src.WriteString("func main() { }\n")
	text := src.String()
	u := checkPackage(t, "main", []pkgSource{{testSourcePath, text}}, nil)
	files, err := LowerTests(u)
	if err != nil {
		t.Fatalf("seed %d: %v\n%s", seed, err, text)
	}
	var gen strings.Builder
	for _, f := range files {
		gen.Write(f.Data)
	}
	out := gen.String()
	proved := 0
	for i, l := range laws {
		fail := func(format string, args ...any) {
			t.Fatalf("seed %d: law Ops%d.L: %s\nsource:\n%s\nreplay: ERGO_SYMBOLIC_SEED=%d go test -run TestSymbolicProofsSound ./internal/sketch/lower", seed, i, fmt.Sprintf(format, args...), l.source(i), seed)
		}
		label := fmt.Sprintf("law Ops%d.L for T%d", i, i)
		ok := strings.Contains(out, "ok "+label+" (proved: ")
		skip := strings.Index(out, "skip "+label+" (not proved: ")
		if ok == (skip >= 0) {
			fail("want exactly one verdict line, ok %v skip %v", ok, skip >= 0)
		}
		at := strings.Index(out, label+" (")
		t.Logf("%s", out[at:at+strings.Index(out[at:], "\n")])
		holds, why := l.modelHolds()
		switch {
		case ok && !holds:
			fail("proved, but the model refutes it: %s", why)
		case l.want == "proved" && !ok:
			fail("a true law the symbolic run must prove is unproved: %s", out[skip:skip+strings.Index(out[skip:], "\n")])
		case l.want == "unproved" && ok:
			fail("a planted false law is proved")
		case l.row != "":
			line := out[skip : skip+strings.Index(out[skip:], "\n")]
			if want := strings.ReplaceAll(l.row, "%s", strconv.Itoa(i)); !strings.Contains(line, want) {
				fail("the not-proved text lacks %q: %s", want, line)
			}
		}
		if ok {
			proved++
		}
	}
	if proved == 0 {
		t.Fatalf("seed %d: no law proved", seed)
	}
}
