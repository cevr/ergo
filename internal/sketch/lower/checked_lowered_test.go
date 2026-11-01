package lower

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// Checked implies lowered (check-v1 stage D3a rewrite, spec ruling item
// 4). Kinds are generated from the type grammar: a basic, slice, Map,
// function (with a fails row, a needs row, both or neither), tuple, record,
// operation type and union; an instance of a
// generic slice, record, operation type and union; a defined type over each
// allowed base; a defined type over each refused base; and a Task-holding
// kind, checked only until stage D3b lowers task.Go. Every kind meets every
// kind at assignment sites, including writes, aggregates, contextual joins,
// closure results, invariant pointers/slices, a pattern's written type and
// the operands of == and !=; every kind meets the operation type Store as a
// wire value, whose admitted pairs lowering refuses until stage E (an
// expected refusal, S109, on the wire's line). A refused pair has exactly one diagnostic, on the
// line of its site (or of the refused definition, or of a Task written in a
// type position: a tuple result or a record field). An admitted pair lowers
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
	optionMember             bool // Some[int] and None inhabit Option[int].
	function                 bool // func() int, with the row below
	// row is the function's rows as one set: bit 0 is the failure Fault,
	// bit 1 the capability Console (clause 5 widens both halves alike).
	row uint
	// comparable: the kind takes == (types.md, Operators 1: structural; a
	// slice, Map, function or operation type never compares, and a record,
	// tuple or union only when every part does).
	comparable bool
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
type DFE func() int fails Fault
type DP (int, int)
type NT ((int, int), int)
type NT2 NT
type DGS GS[int]
type DDS DS
type PP[T] Pair[T, T]
type Holder[T] { f T }
type DFN func() int needs Console
type DSome Some[int]
type DNone None
type TS (Store, int)
type PS Pair[Store, int]
type DSM Map[string, Store]

func Get(m Mem) int { m.N }
func Get(c DInt) int { int(c) }
func Get(m DMem) int { m.N }
func Get(s DS) int { len(s) }
func one() int { 1 }
func work() int fails Fault { 1 }
func callRow(f func() int fails Fault) int {
	f() catch {
	case Fault: 0
	}
}
func cons() int needs Console { 1 }
func consFail() int fails Fault needs Console { 1 }
func callRowN(f func() int fails Fault needs Console) int needs Console {
	f() catch {
	case Fault: 0
	}
}
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
		{name: "Int", ty: "int", pre: let("int", "1"), ok: func(v string) string { return v + " == 1" }, under: "int", named: true, comparable: true},
		{name: "DInt", ty: "DInt", pre: let("DInt", "DInt(1)"), ok: get, under: "int", named: true, impl: true, comparable: true},
		{name: "Slice", ty: "[]int", pre: let("[]int", "[]int{1}"), ok: length, under: "[]int"},
		{name: "DS", ty: "DS", pre: let("DS", "DS{1}"), ok: get, under: "[]int", named: true, impl: true},
		{name: "GS", ty: "GS[int]", pre: let("GS[int]", "GS[int]{1}"), ok: length, under: "[]int", named: true},
		{name: "Map", ty: "Map[string, int]", pre: let("Map[string, int]", `Map[string, int]{"a": 1}`), ok: length, under: "map"},
		{name: "DMap", ty: "DMap", pre: let("DMap", `DMap{"a": 1}`), ok: length, under: "map", named: true},
		{name: "Func", ty: "func() int", pre: let("func() int", "one"), ok: func(v string) string { return v + "() == 1" }, under: "func", function: true},
		{name: "DF", ty: "DF", pre: let("DF", "one"), ok: func(v string) string { return v + "() == 1" }, under: "func", named: true, function: true},
		{name: "FE", ty: "func() int fails Fault", pre: let("func() int fails Fault", "work"), ok: func(v string) string { return "callRow(" + v + ") == 1" }, under: "func-fails", function: true, row: 1},
		{name: "DFE", ty: "DFE", pre: let("DFE", "work"), ok: func(v string) string { return "callRow(" + v + ") == 1" }, under: "func-fails", named: true, function: true, row: 1},
		{name: "FN", ty: "func() int needs Console", pre: let("func() int needs Console", "cons"), ok: func(v string) string { return v + "() == 1" }, under: "func-needs", function: true, row: 2},
		{name: "DFN", ty: "DFN", pre: let("DFN", "cons"), ok: func(v string) string { return v + "() == 1" }, under: "func-needs", named: true, function: true, row: 2},
		{name: "FEN", ty: "func() int fails Fault needs Console", pre: let("func() int fails Fault needs Console", "consFail"), ok: func(v string) string { return "callRowN(" + v + ") == 1" }, under: "func-fails-needs", function: true, row: 3},
		{name: "Tuple", ty: "(int, int)", pre: let("(int, int)", "(1, 2)"), under: "tuple", comparable: true},
		{name: "DP", ty: "DP", pre: let("DP", "(1, 2)"), under: "tuple", named: true, comparable: true},
		{name: "TS", ty: "TS", pre: let("TS", "(Mem{N: 1}, 2)"), under: "tuple-store", named: true},
		{name: "Mem", ty: "Mem", pre: let("Mem", "Mem{N: 1}"), ok: func(v string) string { return v + ".N == 1" }, under: "Mem", named: true, impl: true, member: true, comparable: true},
		{name: "DMem", ty: "DMem", pre: let("DMem", "DMem{N: 1}"), ok: get, under: "Mem", named: true, impl: true, comparable: true},
		{name: "Pair", ty: "Pair[int, int]", pre: let("Pair[int, int]", "Pair[int, int]{First: 1, Second: 2}"), ok: func(v string) string { return v + ".First == 1" }, under: "Pair", named: true, comparable: true},
		{name: "DPair", ty: "DPair", pre: let("DPair", "DPair{First: 1, Second: 2}"), ok: func(v string) string { return v + ".Second == 2" }, under: "Pair", named: true, comparable: true},
		{name: "PS", ty: "PS", pre: let("PS", "PS{First: Mem{N: 1}, Second: 2}"), ok: func(v string) string { return v + ".First.Get() == 1" }, under: "Pair-store", named: true},
		{name: "Store", ty: "Store", pre: let("Store", "Mem{N: 1}"), ok: get, under: "ops", named: true, ops: true, impl: true},
		{name: "Gen", ty: "Gen[int]", pre: let("Gen[int]", "Mem{N: 1}"), ok: get, under: "ops", named: true, ops: true, impl: true},
		{name: "U", ty: "U", pre: let("U", "Mem{N: 1}"), under: "U", named: true, union: true, comparable: true},
		{name: "Opt", ty: "Option[int]", pre: let("Option[int]", "Some{1}"), ok: func(v string) string { return v + " != None" }, under: "Option", named: true, union: true, comparable: true},
		{name: "Some", ty: "Some[int]", pre: let("Some[int]", "Some{1}"), under: "Some", named: true, optionMember: true, comparable: true},
		{name: "DSome", ty: "DSome", pre: let("DSome", "DSome{1}"), under: "Some", named: true, comparable: true},
		{name: "None", ty: "None", pre: let("None", "None"), under: "None", named: true, optionMember: true, comparable: true},
		{name: "DNone", ty: "DNone", pre: let("DNone", "DNone{}"), under: "None", named: true, comparable: true},
		{name: "NT", ty: "NT", pre: let("NT", "((1, 2), 3)"), under: "nested-tuple", named: true, comparable: true},
		{name: "NT2", ty: "NT2", pre: let("NT2", "((1, 2), 3)"), under: "nested-tuple", named: true, comparable: true},
		{name: "DGS", ty: "DGS", pre: let("DGS", "DGS{1}"), ok: length, under: "[]int", named: true},
		{name: "DDS", ty: "DDS", pre: let("DDS", "DDS{1}"), ok: length, under: "[]int", named: true},
		{name: "DInts", ty: "[]DInt", pre: let("[]DInt", "[]DInt{DInt(1)}"), ok: length, under: "[]DInt"},
		{name: "Stores", ty: "[]Store", pre: let("[]Store", "[]Store{Mem{N: 1}}"), ok: length, under: "[]Store"},
		{name: "StoreMap", ty: "Map[string, Store]", pre: let("Map[string, Store]", `Map[string, Store]{"a": Mem{N: 1}}`), ok: length, under: "map-store"},
		{name: "DSM", ty: "DSM", pre: let("DSM", `DSM{"a": Mem{N: 1}}`), ok: length, under: "map-store", named: true},
		{name: "PP", ty: "PP[int]", pre: let("PP[int]", "PP[int]{First: 1, Second: 2}"), ok: func(v string) string { return v + ".Second == 2" }, under: "Pair", named: true, comparable: true},
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
// clause 4 a variant into its union, clause 5 subset rows (the error row
// and the needs row) on otherwise identical function shapes with at least
// one unnamed side. The row bits are sets from the source declarations,
// independent of checker types.
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
	case t.name == "Opt":
		return s.optionMember
	case s.function && t.function && (!s.named || !t.named):
		return s.row & ^t.row == 0
	}
	return false
}

var pairSites = []string{
	"let", "multi", "arg", "ret", "field",
	"reassign", "fieldset", "mapwrite", "maplit", "slicelit", "indexwrite",
	"tuplelit", "tupleret", "ifjoin", "ifjoinrev", "matchjoin", "matchjoinrev",
	"closure", "genericfield", "pointer", "nested", "option",
	"patternlet", "eq", "neq", "wire",
}

// Containers and pointers are invariant in their element type (clauses 1
// and 2 compare the complete underlying type). The operands of == and !=
// compare when both kinds are comparable and one goes into the other's
// type (types.md, Assignability 2; Operators 1). Other sites, the wire
// value included, assign S to T.
func pairAdmits(site string, s, t pairKind) bool {
	switch site {
	case "pointer", "nested":
		return s.ty == t.ty
	case "eq", "neq":
		return s.comparable && t.comparable && (admits(s, t) || admits(t, s))
	}
	return admits(s, t)
}

// taskTypeAt is the line text of a site's own Task type position, refused
// by the type-position rule (types.md, Task): the multi site's helper
// returns a tuple of the source kind, and the field site's record has a
// field of the target kind. "" when the site writes no Task in a type.
func taskTypeAt(site string, s, t pairKind) string {
	switch {
	case site == "multi" && s.task:
		return "Two() ("
	case site == "field" && t.task:
		return "Box { f "
	}
	return ""
}

// pairFunc is the generated code of one pair at one site: declarations and
// a function named id that prints id, and the text of the line a refusal
// is reported on.
func pairFunc(site string, s, t pairKind) (id, code, at string) {
	id = site + s.name + "To" + t.name
	use := func(v string) string {
		switch {
		case t.task:
			return v + ".Stop()"
		case s.name == "None" && t.name == "Opt":
			return "if !(" + v + " == None) { Console.Println(\"FAIL " + id + "\") }"
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
	default:
		var setup, body, value string
		setup = s.pre("src")
		switch site {
		case "reassign":
			setup += "\n" + strings.Replace(t.pre("dst"), "let dst ", "let mut dst ", 1)
			body, value, at = "dst = src", "dst", "dst = src"
		case "fieldset":
			fmt.Fprintf(&b, "type %sBox { f %s }\n", id, t.ty)
			setup += "\n" + t.pre("initial")
			body = fmt.Sprintf("let mut box = %sBox{f: initial}\nbox.f = src", id)
			value, at = "box.f", "box.f = src"
		case "mapwrite", "maplit":
			body = fmt.Sprintf("let mut dst = Map[string, %s]{}\ndst[\"a\"] = src", t.ty)
			at = `dst["a"] = src`
			if site == "maplit" {
				body = fmt.Sprintf("let dst = Map[string, %s]{\"a\": src}", t.ty)
				at = `{"a": src}`
			}
			body += "\nmatch dst.Get(\"a\") {\ncase Some{v}: " + use("v") + "\ncase None: Console.Println(\"FAIL " + id + "\")\n}"
		case "slicelit":
			body = fmt.Sprintf("let dst = []%s{src}", t.ty)
			value, at = "dst[0]", "{src}"
		case "indexwrite":
			setup += "\n" + t.pre("initial")
			body = fmt.Sprintf("let mut dst = []%s{initial}\ndst[0] = src", t.ty)
			value, at = "dst[0]", "dst[0] = src"
		case "tuplelit":
			body = fmt.Sprintf("let dst (%s, int) = (src, 0)\nlet (item, _) = dst", t.ty)
			value, at = "item", "= (src, 0)"
		case "tupleret":
			fmt.Fprintf(&b, "func %sConv(x %s) (%s, int) { (x, 0) }\n", id, s.ty, t.ty)
			body = fmt.Sprintf("let (item, _) = %sConv(src)", id)
			value, at = "item", "{ (x, 0) }"
		case "ifjoin", "ifjoinrev", "matchjoin", "matchjoinrev":
			setup += "\n" + t.pre("other")
			a, z, condition := "src", "other", "true"
			if strings.HasSuffix(site, "rev") {
				a, z = z, a
				condition = "false" // Execute src in both orders.
			}
			if strings.HasPrefix(site, "if") {
				body = fmt.Sprintf("let dst %s = if %s { %s } else { %s }", t.ty, condition, a, z)
				at = "let dst "
			} else {
				body = fmt.Sprintf("let dst %s = match %s {\ncase true: %s\ncase false: %s\n}", t.ty, condition, a, z)
				at = "case true: src"
				if a != "src" {
					at = "case false: src"
				}
			}
			value = "dst"
		case "closure":
			body = fmt.Sprintf("let f = func() %s { src }\nlet dst = f()", t.ty)
			value, at = "dst", "{ src }"
		case "genericfield":
			body = fmt.Sprintf("let box = Holder[%s]{f: src}", t.ty)
			value, at = "box.f", "{f: src}"
		case "pointer":
			fmt.Fprintf(&b, "func %sTake(dst *%s) needs Console { %s }\n", id, t.ty, use("(*dst)"))
			setup = strings.Replace(setup, "let src ", "let mut src ", 1)
			body = fmt.Sprintf("%sTake(&src)", id)
			at = "Take(&src)"
		case "nested":
			body = fmt.Sprintf("let items = []%s{src}\nlet dst []%s = items", s.ty, t.ty)
			value, at = "dst[0]", "= items"
		case "option":
			body = fmt.Sprintf("let dst Option[%s] = Some{src}", t.ty)
			body += "\nmatch dst {\ncase Some{v}: " + use("v") + "\ncase None: Console.Println(\"FAIL " + id + "\")\n}"
			at = "= Some{src}"
		case "patternlet":
			body = fmt.Sprintf("let (item, _) (%s, int) = (src, 0)", t.ty)
			value, at = "item", "= (src, 0)"
		case "eq", "neq":
			// Every value of the grid is 1-like, so two comparable values
			// are equal, except None against Some{1}.
			setup += "\n" + t.pre("dst")
			op, holds := "==", (s.name == "None") == (t.name == "None")
			if site == "neq" {
				op, holds = "!=", !holds
			}
			test := "src " + op + " dst"
			if holds {
				test = "!(" + test + ")"
			}
			body = "if " + test + " { Console.Println(\"FAIL " + id + "\") }"
			at = "src " + op + " dst"
		case "wire":
			body = "with wire { Store: src } {\nif Store.Get() != 1 { Console.Println(\"FAIL " + id + "\") }\n}"
			at = "Store: src"
		default:
			panic("unknown pair site: " + site)
		}
		if value != "" {
			body += "\n" + use(value)
		}
		fmt.Fprintf(&b, "func %s() needs Console {\n%s\n%s\nConsole.Println(%q)\n}\n", id, setup, body, id)
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
		deferred := 0 // admitted pairs whose lowering waits for stage E
		for _, s := range kinds {
			for _, k := range kinds {
				if site != "let" && site != "multi" && site != "arg" && site != "ret" && site != "field" && (s.decl != "" || k.decl != "" || s.task || k.task) {
					continue // The original sites own refused definitions and Task type positions.
				}
				if s.decl != "" && k.decl != "" && s.name != k.name {
					continue // two refused definitions are two mistakes
				}
				if site == "wire" && k.name != "Store" {
					continue // a wire key is an operation type
				}
				if (site == "field" || site == "fieldset") && k.under == "tuple" && !k.named {
					// `f (int, int)` in a type body is an operation
					// (grammar.md, Parser rule 11): no record field has
					// an unnamed tuple type.
					continue
				}
				taskAt := taskTypeAt(site, s, k)
				if taskAt != "" && (s.decl != "" || k.decl != "") {
					continue // a refused definition and a Task type: two mistakes
				}
				id, code, at := pairFunc(site, s, k)
				src := pairProgram([]pairKind{s, k}, s.task, []string{id}, []string{code})
				o := lowerSource(id, src, true)
				admitted := o.err == nil && len(o.diags) == 0
				if model := pairAdmits(site, s, k) && taskAt == ""; admitted != model {
					t.Errorf("%s: admitted %v, the assignability model says %v; diagnostics %v\n%s", id, admitted, model, o.diags, src)
					continue
				}
				if !admitted {
					if o.err != nil {
						t.Errorf("%s: %v", id, o.err)
						continue
					}
					where := at
					if taskAt != "" {
						where = taskAt
					} else if s.decl != "" {
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
				if site == "wire" {
					// An inline wire value is lowered in stage E: the one
					// expected refusal, on the wire's line.
					const wireRefusal = "an inline wire value is lowered after check-v1 stage E (196x)"
					lines := strings.Split(src, "\n")
					if o.panicked != nil || len(o.refused) != 1 || o.refused[0].Message != wireRefusal || o.refused[0].Line < 1 || o.refused[0].Line > len(lines) || !strings.Contains(lines[o.refused[0].Line-1], at) {
						t.Errorf("%s: want the one stage E wire refusal on the line with %q, got panic %v, refusals %+v\n%s", id, at, o.panicked, o.refused, src)
					}
					deferred++
					continue
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
		t.Logf("%s: %d admitted pairs run, %d admitted pairs refused by lowering until stage E", site, len(ids), deferred)
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
			got, expected := strings.Split(stdout, "\n"), strings.Split(want[p.name], "\n")
			first := 0
			for first < len(got) && first < len(expected) && got[first] == expected[first] {
				first++
			}
			t.Errorf("%s: exit %d, stderr %q, output first differs at marker %d:\n%s", p.name, code, stderr, first+1, firstLines(strings.Join(got[first:], "\n"), 10))
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
