package lower

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// Checked implies lowered (check-v1 stage D3a rewrite, spec ruling item
// 4). Kinds are generated from the type grammar: a basic, slice, Map,
// function, tuple, record, operation type and union; an instance of a
// generic slice, record, operation type and union; a defined type over each
// allowed base; a defined type over each refused base; and a Task-holding
// kind, checked only until stage D3b lowers task.Go. Every kind meets every
// kind at every site: a let, a multi-name let, an argument, a result and a
// composite-literal field. A refused pair has exactly one diagnostic, on the
// line of its site (or of the refused definition). An admitted pair lowers
// on its own, and every admitted pair of a site, batched in one program,
// builds and runs, printing its marker. A small model of the
// assignability clauses (types.md, Assignability) says which pairs the
// checker admits, so a changed verdict fails here too.

// pairKind is one kind of the generated grid.
type pairKind struct {
	name string // the kind's identifier part
	ty   string // its type, as written
	// pre declares v of type ty, given the name v.
	pre func(v string) string
	// ok is a bool expression over v that holds for pre's value; "" for
	// none.
	ok func(v string) string
	// under is the kind's underlying type, as a key; named is Go's
	// hasName (a defined type, a basic type, an instance).
	under string
	named bool
	// ops: the kind is an operation type with Get() int; impl: the kind
	// provides Get() int; member: the kind is a variant of U.
	ops, impl, member, union bool
	// decl declares a refused defined type: every use of the kind meets
	// that one diagnostic.
	decl string
	// task: a Task-holding kind, checked but not lowered until stage D3b.
	task bool
}

const pairDecls = `type Fault { msg string }
type DInt int
type DS []int
type GS[T] []T
type DMap Map[string, int]
type Mem { N int }
type Other { C int }
type DMem Mem
type Pair[A, B] { First A, Second B }
type DPair Pair[int, int]
type Store { Get() int }
type Gen[T] { Get() T }
type U = Mem | Other
type DF func() int
type DP (int, int)

func Get(m Mem) int { m.N }
func Get(c DInt) int { int(c) }
func Get(m DMem) int { m.N }
func Get(s DS) int { len(s) }
func one() int { 1 }
func work() int fails Fault { 1 }
`

func pairKinds() []pairKind {
	let := func(ty, val string) func(string) string {
		return func(v string) string { return "let " + v + " " + ty + " = " + val }
	}
	via := func(ty, mid, val string) func(string) string {
		return func(v string) string {
			return "let " + v + "0 " + mid + " = " + val + "\n\tlet " + v + " " + ty + " = " + v + "0"
		}
	}
	get := func(v string) string { return v + ".Get() == 1" }
	length := func(v string) string { return "len(" + v + ") == 1" }
	return []pairKind{
		{name: "Int", ty: "int", pre: let("int", "1"), ok: func(v string) string { return v + " == 1" }, under: "int", named: true},
		{name: "DInt", ty: "DInt", pre: let("DInt", "DInt(1)"), ok: get, under: "int", named: true, impl: true},
		{name: "Slice", ty: "[]int", pre: let("[]int", "[]int{1}"), ok: length, under: "[]int"},
		{name: "DS", ty: "DS", pre: let("DS", "DS{1}"), ok: get, under: "[]int", named: true, impl: true},
		{name: "GS", ty: "GS[int]", pre: let("GS[int]", "GS[int]{1}"), ok: length, under: "[]int", named: true},
		{name: "Map", ty: "Map[string, int]", pre: let("Map[string, int]", `Map[string, int]{"a": 1}`), ok: length, under: "map"},
		{name: "DMap", ty: "DMap", pre: let("DMap", `DMap{"a": 1}`), ok: length, under: "map", named: true},
		{name: "Func", ty: "func() int", pre: let("func() int", "one"), ok: func(v string) string { return v + "() == 1" }, under: "func"},
		{name: "DF", ty: "DF", pre: let("DF", "one"), ok: func(v string) string { return v + "() == 1" }, under: "func", named: true},
		{name: "Tuple", ty: "(int, int)", pre: let("(int, int)", "(1, 2)"), under: "tuple"},
		{name: "DP", ty: "DP", pre: let("DP", "(1, 2)"), under: "tuple", named: true},
		{name: "Mem", ty: "Mem", pre: let("Mem", "Mem{N: 1}"), ok: func(v string) string { return v + ".N == 1" }, under: "Mem", named: true, impl: true, member: true},
		{name: "DMem", ty: "DMem", pre: let("DMem", "DMem{N: 1}"), ok: get, under: "Mem", named: true, impl: true},
		{name: "Pair", ty: "Pair[int, int]", pre: let("Pair[int, int]", "Pair[int, int]{First: 1, Second: 2}"), ok: func(v string) string { return v + ".First == 1" }, under: "Pair", named: true},
		{name: "DPair", ty: "DPair", pre: let("DPair", "DPair{First: 1, Second: 2}"), ok: func(v string) string { return v + ".Second == 2" }, under: "Pair", named: true},
		{name: "Store", ty: "Store", pre: let("Store", "Mem{N: 1}"), ok: get, under: "ops", named: true, ops: true, impl: true},
		{name: "Gen", ty: "Gen[int]", pre: let("Gen[int]", "Mem{N: 1}"), ok: get, under: "ops", named: true, ops: true, impl: true},
		{name: "U", ty: "U", pre: let("U", "Mem{N: 1}"), under: "U", named: true, union: true},
		{name: "Opt", ty: "Option[int]", pre: let("Option[int]", "Some{1}"), ok: func(v string) string { return v + " != None" }, under: "Option", named: true, union: true},
		{name: "DStore", ty: "DStore", pre: via("DStore", "Store", "Mem{N: 1}"), decl: "type DStore Store"},
		{name: "DGen", ty: "DGen", pre: via("DGen", "Gen[int]", "Mem{N: 1}"), decl: "type DGen Gen[int]"},
		{name: "DU", ty: "DU", pre: via("DU", "U", "Mem{N: 1}"), decl: "type DU U"},
		{name: "DOpt", ty: "DOpt", pre: via("DOpt", "Option[int]", "Some{1}"), decl: "type DOpt Option[int]"},
		{name: "Task", ty: "Task[int, Fault]", pre: func(v string) string { return "let " + v + " = task.Go() { work() }" }, under: "Task", named: true, task: true},
	}
}

// admits is the model of the checker's assignability (types.md,
// Assignability) over the grid: clause 1 identity, clause 2 one underlying
// type with an unnamed side, clause 3 an operation type from a provider,
// clause 4 a variant into its union.
func admits(s, t pairKind) bool {
	switch {
	case s.decl != "" || t.decl != "":
		return false
	case s.name == t.name:
		return true
	case s.task || t.task:
		return false
	case s.under == t.under && (!s.named || !t.named):
		return true
	case t.ops:
		return s.impl
	case t.name == "U":
		return s.member
	}
	return false
}

var pairSites = []string{"let", "multi", "arg", "ret", "field"}

// pairFunc is the generated code of one pair at one site: declarations and
// a function named id that prints id, and the text of the line a refusal
// is reported on.
func pairFunc(site string, s, t pairKind) (id, code, at string) {
	id = site + s.name + "To" + t.name
	use := func(v string) string {
		switch {
		case t.task:
			return v + ".Stop()"
		case t.ok != nil:
			return "if !(" + t.ok(v) + ") { Console.Println(\"FAIL " + id + "\") }"
		}
		return "_ = " + v
	}
	var b strings.Builder
	switch site {
	case "let":
		fmt.Fprintf(&b, "func %s() needs Console {\n\t%s\n\tlet dst %s = src\n\t%s\n\tConsole.Println(%q)\n}\n", id, s.pre("src"), t.ty, use("dst"), id)
		at = "let dst " + t.ty + " = src"
	case "multi":
		fmt.Fprintf(&b, "func %sTwo() (%s, %s) {\n\t%s\n\t(src, src)\n}\n\n", id, s.ty, s.ty, s.pre("src"))
		fmt.Fprintf(&b, "func %s() needs Console {\n\tlet d1, d2 %s = %sTwo()\n\t%s\n\t%s\n\tConsole.Println(%q)\n}\n", id, t.ty, id, use("d1"), use("d2"), id)
		at = "let d1, d2"
	case "arg":
		fmt.Fprintf(&b, "func %sTake(dst %s) needs Console {\n\t%s\n}\n\n", id, t.ty, use("dst"))
		fmt.Fprintf(&b, "func %s() needs Console {\n\t%s\n\t%sTake(src)\n\tConsole.Println(%q)\n}\n", id, s.pre("src"), id, id)
		at = "Take(src)"
	case "ret":
		fmt.Fprintf(&b, "func %sConv(x %s) %s { x }\n\n", id, s.ty, t.ty)
		fmt.Fprintf(&b, "func %s() needs Console {\n\t%s\n\tlet dst = %sConv(src)\n\t%s\n\tConsole.Println(%q)\n}\n", id, s.pre("src"), id, use("dst"), id)
		at = "{ x }"
	case "field":
		fmt.Fprintf(&b, "type %sBox { f %s }\n\n", id, t.ty)
		fmt.Fprintf(&b, "func %s() needs Console {\n\t%s\n\tlet box = %sBox{f: src}\n\t%s\n\tConsole.Println(%q)\n}\n", id, s.pre("src"), id, use("box.f"), id)
		at = "Box{f: src}"
	}
	return id, b.String(), at
}

// pairProgram assembles one program from the kinds it uses and its pair
// functions, with a main that runs each function in order; started says
// whether a function starts a task, which needs the task import.
func pairProgram(kinds []pairKind, started bool, ids, funcs []string) string {
	var b strings.Builder
	if started {
		b.WriteString("import task\n\n")
	}
	seen := map[string]bool{}
	b.WriteString(pairDecls)
	for _, k := range kinds {
		if k.decl != "" && !seen[k.decl] {
			seen[k.decl] = true
			b.WriteString(k.decl + "\n")
		}
	}
	for _, f := range funcs {
		b.WriteString("\n" + f)
	}
	b.WriteString("\nfunc main() needs Console {\n")
	for _, id := range ids {
		b.WriteString("\t" + id + "()\n")
	}
	b.WriteString("}\n")
	return b.String()
}

func TestCheckedImpliesLowered(t *testing.T) {
	kinds := pairKinds()
	var programs []*lowered
	want := map[string]string{}
	for _, site := range pairSites {
		var ids, funcs []string
		for _, s := range kinds {
			for _, k := range kinds {
				if s.decl != "" && k.decl != "" && s.name != k.name {
					continue // two refused definitions are two mistakes
				}
				if site == "field" && k.under == "tuple" && !k.named {
					// `f (int, int)` in a type body is an operation
					// (grammar.md, Parser rule 11): no record field has
					// an unnamed tuple type.
					continue
				}
				id, code, at := pairFunc(site, s, k)
				src := pairProgram([]pairKind{s, k}, s.task, []string{id}, []string{code})
				o := lowerSource(id, src, true)
				admitted := o.err == nil && len(o.diags) == 0
				if admitted != admits(s, k) {
					t.Errorf("%s: admitted %v, the assignability model says %v; diagnostics %v\n%s", id, admitted, admits(s, k), o.diags, src)
					continue
				}
				if !admitted {
					if o.err != nil {
						t.Errorf("%s: %v", id, o.err)
						continue
					}
					where := at
					if s.decl != "" {
						where = s.decl
					} else if k.decl != "" {
						where = k.decl
					}
					lines := strings.Split(src, "\n")
					if len(o.diags) != 1 || o.diags[0].Line < 1 || o.diags[0].Line > len(lines) || !strings.Contains(lines[o.diags[0].Line-1], where) {
						t.Errorf("%s: want one diagnostic on the line with %q, got %s\n%s", id, where, diagText(o.diags), src)
					}
					continue
				}
				if s.task || k.task {
					continue // task.Go lowers in stage D3b
				}
				switch {
				case o.panicked != nil:
					t.Errorf("%s: checked, lowering panicked: %v\n%s", id, o.panicked, src)
					continue
				case len(o.refused) > 0:
					t.Errorf("%s: checked, lowering refused %+v\n%s", id, o.refused, src)
					continue
				}
				ids = append(ids, id)
				funcs = append(funcs, code)
			}
		}
		t.Logf("%s: %d admitted pairs run", site, len(ids))
		if len(ids) == 0 {
			continue
		}
		var used []pairKind
		for _, k := range kinds {
			if k.decl == "" && !k.task {
				used = append(used, k)
			}
		}
		src := pairProgram(used, false, ids, funcs)
		programs = append(programs, mustLower(t, "pairs"+site, src))
		want["pairs"+site] = strings.Join(ids, "\n") + "\n"
	}
	exes := buildAll(t, programs, true)
	for _, p := range programs {
		exe, ok := exes[p.name]
		if !ok {
			continue
		}
		stdout, stderr, code := runExe(t, exe)
		if code != 0 || stdout != want[p.name] {
			t.Errorf("%s: exit %d, stderr %q, output differs from the markers:\n%s", p.name, code, stderr, firstLines(stdout, 40))
		}
	}
}

func diagText(ds []check.Diagnostic) string {
	parts := make([]string, len(ds))
	for i, d := range ds {
		parts[i] = fmt.Sprintf("%s:%d %s", d.Code, d.Line, d.Message)
	}
	return "[" + strings.Join(parts, "; ") + "]"
}
