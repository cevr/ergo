package lower

import (
	"fmt"
	"iter"
	"slices"
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
// kind, lowered and run since stage D3b. Every kind meets every
// kind at assignment sites, including writes, aggregates, contextual joins,
// closure results, invariant pointers/slices, a pattern's written type and
// the operands of == and !=; every kind meets the operation type Store as a
// inline provider expression, whose admitted pairs lower and run (#199
// stage 3c). A refused pair has exactly one diagnostic, on the
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
	// equatable: the kind takes == (types.md, Operators 1: structural; a
	// function or operation type never compares, a slice or Map compares
	// when its elements do (eq-structural), and a record, tuple or union
	// only when every part does).
	equatable bool
	// decl declares a refused defined type: every use of the kind meets
	// that one diagnostic.
	decl string
	// task: a Task-holding kind; a program with one imports task.
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
		{name: "Int", ty: "int", pre: let("int", "1"), ok: func(v string) string { return v + " == 1" }, under: "int", named: true, equatable: true},
		{name: "DInt", ty: "DInt", pre: let("DInt", "DInt(1)"), ok: get, under: "int", named: true, impl: true, equatable: true},
		{name: "Slice", ty: "[]int", pre: let("[]int", "[]int{1}"), ok: length, under: "[]int", equatable: true},
		{name: "DS", ty: "DS", pre: let("DS", "DS{1}"), ok: get, under: "[]int", named: true, impl: true, equatable: true},
		{name: "GS", ty: "GS[int]", pre: let("GS[int]", "GS[int]{1}"), ok: length, under: "[]int", named: true, equatable: true},
		{name: "Map", ty: "Map[string, int]", pre: let("Map[string, int]", `Map[string, int]{"a": 1}`), ok: length, under: "map", equatable: true},
		{name: "DMap", ty: "DMap", pre: let("DMap", `DMap{"a": 1}`), ok: length, under: "map", named: true, equatable: true},
		{name: "Func", ty: "func() int", pre: let("func() int", "one"), ok: func(v string) string { return v + "() == 1" }, under: "func", function: true},
		{name: "DF", ty: "DF", pre: let("DF", "one"), ok: func(v string) string { return v + "() == 1" }, under: "func", named: true, function: true},
		{name: "FE", ty: "func() int fails Fault", pre: let("func() int fails Fault", "work"), ok: func(v string) string { return "callRow(" + v + ") == 1" }, under: "func-fails", function: true, row: 1},
		{name: "DFE", ty: "DFE", pre: let("DFE", "work"), ok: func(v string) string { return "callRow(" + v + ") == 1" }, under: "func-fails", named: true, function: true, row: 1},
		{name: "FN", ty: "func() int needs Console", pre: let("func() int needs Console", "cons"), ok: func(v string) string { return v + "() == 1" }, under: "func-needs", function: true, row: 2},
		{name: "DFN", ty: "DFN", pre: let("DFN", "cons"), ok: func(v string) string { return v + "() == 1" }, under: "func-needs", named: true, function: true, row: 2},
		{name: "FEN", ty: "func() int fails Fault needs Console", pre: let("func() int fails Fault needs Console", "consFail"), ok: func(v string) string { return "callRowN(" + v + ") == 1" }, under: "func-fails-needs", function: true, row: 3},
		{name: "Tuple", ty: "(int, int)", pre: let("(int, int)", "(1, 2)"), under: "tuple", equatable: true},
		{name: "DP", ty: "DP", pre: let("DP", "(1, 2)"), under: "tuple", named: true, equatable: true},
		{name: "TS", ty: "TS", pre: let("TS", "(Mem{N: 1}, 2)"), under: "tuple-store", named: true},
		{name: "Mem", ty: "Mem", pre: let("Mem", "Mem{N: 1}"), ok: func(v string) string { return v + ".N == 1" }, under: "Mem", named: true, impl: true, member: true, equatable: true},
		{name: "DMem", ty: "DMem", pre: let("DMem", "DMem{N: 1}"), ok: get, under: "Mem", named: true, impl: true, equatable: true},
		{name: "Pair", ty: "Pair[int, int]", pre: let("Pair[int, int]", "Pair[int, int]{First: 1, Second: 2}"), ok: func(v string) string { return v + ".First == 1" }, under: "Pair", named: true, equatable: true},
		{name: "DPair", ty: "DPair", pre: let("DPair", "DPair{First: 1, Second: 2}"), ok: func(v string) string { return v + ".Second == 2" }, under: "Pair", named: true, equatable: true},
		{name: "PS", ty: "PS", pre: let("PS", "PS{First: Mem{N: 1}, Second: 2}"), ok: func(v string) string { return v + ".First.Get() == 1" }, under: "Pair-store", named: true},
		{name: "Store", ty: "Store", pre: let("Store", "Mem{N: 1}"), ok: get, under: "ops", named: true, ops: true, impl: true},
		{name: "Gen", ty: "Gen[int]", pre: let("Gen[int]", "Mem{N: 1}"), ok: get, under: "ops", named: true, ops: true, impl: true},
		{name: "U", ty: "U", pre: let("U", "Mem{N: 1}"), under: "U", named: true, union: true, equatable: true},
		{name: "Opt", ty: "Option[int]", pre: let("Option[int]", "Some{1}"), ok: func(v string) string { return v + " != None" }, under: "Option", named: true, union: true, equatable: true},
		{name: "Some", ty: "Some[int]", pre: let("Some[int]", "Some{1}"), under: "Some", named: true, optionMember: true, equatable: true},
		{name: "DSome", ty: "DSome", pre: let("DSome", "DSome{1}"), under: "Some", named: true, equatable: true},
		{name: "None", ty: "None", pre: let("None", "None"), under: "None", named: true, optionMember: true, equatable: true},
		{name: "DNone", ty: "DNone", pre: let("DNone", "DNone{}"), under: "None", named: true, equatable: true},
		{name: "NT", ty: "NT", pre: let("NT", "((1, 2), 3)"), under: "nested-tuple", named: true, equatable: true},
		{name: "NT2", ty: "NT2", pre: let("NT2", "((1, 2), 3)"), under: "nested-tuple", named: true, equatable: true},
		{name: "DGS", ty: "DGS", pre: let("DGS", "DGS{1}"), ok: length, under: "[]int", named: true, equatable: true},
		{name: "DDS", ty: "DDS", pre: let("DDS", "DDS{1}"), ok: length, under: "[]int", named: true, equatable: true},
		{name: "DInts", ty: "[]DInt", pre: let("[]DInt", "[]DInt{DInt(1)}"), ok: length, under: "[]DInt", equatable: true},
		{name: "Stores", ty: "[]Store", pre: let("[]Store", "[]Store{Mem{N: 1}}"), ok: length, under: "[]Store"},
		{name: "StoreMap", ty: "Map[string, Store]", pre: let("Map[string, Store]", `Map[string, Store]{"a": Mem{N: 1}}`), ok: length, under: "map-store"},
		{name: "DSM", ty: "DSM", pre: let("DSM", `DSM{"a": Mem{N: 1}}`), ok: length, under: "map-store", named: true},
		{name: "PP", ty: "PP[int]", pre: let("PP[int]", "PP[int]{First: 1, Second: 2}"), ok: func(v string) string { return v + ".Second == 2" }, under: "Pair", named: true, equatable: true},
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
	"patternlet", "eq", "neq", "context",
}

// Containers and pointers are invariant in their element type (clauses 1
// and 2 compare the complete underlying type). The operands of == and !=
// compare when both kinds are comparable and one goes into the other's
// type (types.md, Assignability 2; Operators 1). Other sites, the context
// value included, assign S to T.
func pairAdmits(site string, s, t pairKind) bool {
	switch site {
	case "pointer", "nested":
		return s.ty == t.ty
	case "eq", "neq":
		return s.equatable && t.equatable && (admits(s, t) || admits(t, s))
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
// kept is the type of a parameter the function returns: a function type
// keeps its value only when written escaping (types.md, Function values 2).
func kept(ty string) string {
	if strings.HasPrefix(ty, "func(") {
		return "escaping " + ty
	}
	return ty
}

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
		fmt.Fprintf(&b, "func %sConv(x %s) %s { x }\n\n", id, kept(s.ty), t.ty)
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
			fmt.Fprintf(&b, "func %sConv(x %s) (%s, int) { (x, 0) }\n", id, kept(s.ty), t.ty)
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
			// Every value of the grid is 1-like, so two equatable values
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
		case "context":
			body = "with context { Store: src } {\nif Store.Get() != 1 { Console.Println(\"FAIL " + id + "\") }\n}"
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

// pairCandidate is one pair of the grid at one site, as the generator
// writes it: its function, the program that holds it alone, the site's
// line for a refusal, and the line of a Task type position ("" for none).
type pairCandidate struct {
	s, k                      pairKind
	id, code, at, src, taskAt string
}

// pairCandidates is every pair the grid writes at site, in the order the
// site's program runs them.
func pairCandidates(site string, kinds []pairKind) []pairCandidate {
	var out []pairCandidate
	for _, s := range kinds {
		for _, k := range kinds {
			if site != "let" && site != "multi" && site != "arg" && site != "ret" && site != "field" && (s.decl != "" || k.decl != "" || s.task || k.task) {
				continue // The original sites own refused definitions and Task type positions.
			}
			if s.decl != "" && k.decl != "" && s.name != k.name {
				continue // two refused definitions are two mistakes
			}
			if site == "context" && k.name != "Store" {
				continue // a context entry key is an operation type
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
			out = append(out, pairCandidate{s: s, k: k, id: id, code: code, at: at, src: src, taskAt: taskAt})
		}
	}
	return out
}

// pairSite checks and lowers each pair of site on its own, and returns the
// program of the site's admitted pairs with the output it prints, or nil
// when the site admits no pair.
func pairSite(t *testing.T, site string) (*lowered, string) {
	t.Helper()
	kinds := pairKinds()
	var ids, funcs []string
	started := false // an admitted pair starts a task
	for _, c := range pairCandidates(site, kinds) {
		s, k, id, src := c.s, c.k, c.id, c.src
		o := lowerSource(id, src, true)
		admitted := o.err == nil && len(o.diags) == 0
		if model := pairAdmits(site, s, k) && c.taskAt == ""; admitted != model {
			t.Errorf("%s: admitted %v, the assignability model says %v; diagnostics %v\n%s", id, admitted, model, o.diags, src)
			continue
		}
		if !admitted {
			if o.err != nil {
				t.Errorf("%s: %v", id, o.err)
				continue
			}
			where := c.at
			if c.taskAt != "" {
				where = c.taskAt
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
		switch {
		case o.panicked != nil:
			t.Errorf("%s: checked, lowering panicked: %v\n%s", id, o.panicked, src)
			continue
		case len(o.refused) > 0:
			t.Errorf("%s: checked, lowering refused %+v\n%s", id, o.refused, src)
			continue
		}
		started = started || s.task
		ids = append(ids, id)
		funcs = append(funcs, c.code)
	}
	t.Logf("%s: %d admitted pairs run", site, len(ids))
	if len(ids) == 0 {
		return nil, ""
	}
	var used []pairKind
	for _, k := range kinds {
		if k.decl == "" {
			used = append(used, k)
		}
	}
	src := pairProgram(used, started, ids, funcs)
	return mustLower(t, "pairs"+site, src), strings.Join(ids, "\n") + "\n"
}

// checkedCase is one case of TestCheckedImpliesLowered: a site of the pair
// grid, a site of the std grid, or a kind of the operand-order axis.
type checkedCase struct{ grid, site string }

// checkedSplit is TestCheckedImpliesLowered in parts. A part checks and
// lowers each pair of its cases on its own, then builds and runs one
// program per case in a module of its own.
var checkedSplit = newSplit(split[checkedCase]{
	name:  "TestCheckedImpliesLowered",
	parts: checkedParts,
	cases: func(yield func(checkedCase) bool) {
		for _, site := range pairSites {
			if !yield(checkedCase{"pairs", site}) {
				return
			}
		}
		for _, site := range stdSites {
			if !yield(checkedCase{"std", site}) {
				return
			}
		}
		for _, k := range orderKinds {
			if !yield(checkedCase{"order", k.name}) {
				return
			}
		}
	},
	key: func(c checkedCase) string {
		var b strings.Builder
		b.WriteString(c.grid + "/" + c.site + "\n")
		switch c.grid {
		case "pairs":
			for _, p := range pairCandidates(c.site, pairKinds()) {
				b.WriteString(p.id + " " + probeSum([]byte(p.src)) + "\n")
			}
		case "std":
			for _, op := range stdOps {
				id, code := stdFunc(op, c.site)
				b.WriteString(id + " " + probeSum([]byte(stdProgram([]string{id}, []string{code}))) + "\n")
			}
		case "order":
			for _, o := range orderCases(orderKindNamed(c.site)) {
				b.WriteString(o.name + " " + probeSum([]byte(o.src)) + "\n")
			}
		}
		return b.String()
	},
	run: runChecked,
})

// checkedParts: a pair site costs up to 7 s to check at load 35 (measured
// at f79be34b). With 20 parts, parts 0 to 5 hold one of the six costly
// first sites and one of the six cheap last sites; the other parts hold one
// pair site and one std site or order kind.
const checkedParts = 20

func TestCheckedImpliesLoweredPart0(t *testing.T)  { checkedSplit.Part(t, 0) }
func TestCheckedImpliesLoweredPart1(t *testing.T)  { checkedSplit.Part(t, 1) }
func TestCheckedImpliesLoweredPart2(t *testing.T)  { checkedSplit.Part(t, 2) }
func TestCheckedImpliesLoweredPart3(t *testing.T)  { checkedSplit.Part(t, 3) }
func TestCheckedImpliesLoweredPart4(t *testing.T)  { checkedSplit.Part(t, 4) }
func TestCheckedImpliesLoweredPart5(t *testing.T)  { checkedSplit.Part(t, 5) }
func TestCheckedImpliesLoweredPart6(t *testing.T)  { checkedSplit.Part(t, 6) }
func TestCheckedImpliesLoweredPart7(t *testing.T)  { checkedSplit.Part(t, 7) }
func TestCheckedImpliesLoweredPart8(t *testing.T)  { checkedSplit.Part(t, 8) }
func TestCheckedImpliesLoweredPart9(t *testing.T)  { checkedSplit.Part(t, 9) }
func TestCheckedImpliesLoweredPart10(t *testing.T) { checkedSplit.Part(t, 10) }
func TestCheckedImpliesLoweredPart11(t *testing.T) { checkedSplit.Part(t, 11) }
func TestCheckedImpliesLoweredPart12(t *testing.T) { checkedSplit.Part(t, 12) }
func TestCheckedImpliesLoweredPart13(t *testing.T) { checkedSplit.Part(t, 13) }
func TestCheckedImpliesLoweredPart14(t *testing.T) { checkedSplit.Part(t, 14) }
func TestCheckedImpliesLoweredPart15(t *testing.T) { checkedSplit.Part(t, 15) }
func TestCheckedImpliesLoweredPart16(t *testing.T) { checkedSplit.Part(t, 16) }
func TestCheckedImpliesLoweredPart17(t *testing.T) { checkedSplit.Part(t, 17) }
func TestCheckedImpliesLoweredPart18(t *testing.T) { checkedSplit.Part(t, 18) }
func TestCheckedImpliesLoweredPart19(t *testing.T) { checkedSplit.Part(t, 19) }

// runChecked checks and lowers the pairs of cases, then builds and runs
// the program of each case.
//
// It builds Go because its callers ask what the built program prints and how
// it exits; no in-process check observes that, and Ergo has no reference
// interpreter yet (test-speed T8).
func runChecked(t *testing.T, cases iter.Seq[checkedCase]) {
	var programs []*lowered
	want := map[string]string{}
	orderWant := map[string]string{}
	for c := range cases {
		switch c.grid {
		case "pairs":
			if p, w := pairSite(t, c.site); p != nil {
				programs = append(programs, p)
				want[p.name] = w
			}
		case "std":
			if p, w := stdSite(t, c.site); p != nil {
				programs = append(programs, p)
				want[p.name] = w
			}
		case "order":
			ps, ws := orderGrid(t, orderKindNamed(c.site))
			programs = append(programs, ps...)
			for name, w := range ws {
				orderWant[name] = w
			}
		default:
			t.Fatalf("unknown grid %q", c.grid)
		}
	}
	exes := buildAll(t, programs, true)
	for _, p := range programs {
		exe, ok := exes[p.name]
		if !ok {
			continue
		}
		stdout, stderr, code := runExe(t, exe)
		if defect, ok := orderWant[p.name]; ok {
			if code != 2 || stdout != "" || !strings.Contains(stderr, defect) {
				t.Errorf("%s: exit %d, stdout %q, want the defect %q first; stderr:\n%s\n%s", p.name, code, stdout, defect, firstLines(stderr, 6), p.src)
			}
			continue
		}
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

// The std grid (stage D3b round 1 review, required in round 2): every std
// operation meets every site where a program can reach it. An operation is
// work on q, a fresh queue.Queue[int] of capacity 4, that yields a known
// int; a site is where that work stands: a declared function called
// directly, the same function called as a function value, the work with its
// operation taken as a value (a method value, or the std function itself),
// a function literal, a task.Go body, and a provider of the program's
// operation type called through a value of it. Each pair is checked and
// lowered on its own; a pair the checker refuses must be one the model
// (stdChecks) refuses, with a diagnostic on the line that takes the value;
// a pair lowering refuses must name its owner (undeferred); every other
// pair of a site runs in one program and prints its result.
type stdOp struct {
	name  string
	body  string // the work, which yields want
	value string // the work with the operation taken as a value
	at    string // the line of value that takes the value
	fails bool   // the work can fail with queue.Closed
	want  int
}

var stdOps = []stdOp{
	{"GoValue", "let t = task.Go() { 7 }\nq.Close()\nt.Wait()", "let g = task.Go\nlet t = g() { 7 }\nq.Close()\nt.Wait()", "let g = task.Go", false, 7},
	{"GoNoValue", "let t = task.Go() { try q.Push(3) }\ntry t.Wait()\ntry q.Pop()", "let g = task.Go\nlet t = g() { try q.Push(3) }\ntry t.Wait()\ntry q.Pop()", "let g = task.Go", true, 3},
	{"Wait", "let t = task.Go() { try q.Pop() }\ntry q.Push(4)\ntry t.Wait()", "let t = task.Go() { try q.Pop() }\ntry q.Push(4)\nlet w = t.Wait\ntry w()", "let w = t.Wait", true, 4},
	{"Stop", "let t = task.Go() { try q.Pop() }\nt.Stop()\n5", "let t = task.Go() { try q.Pop() }\nlet s = t.Stop\ns()\n5", "let s = t.Stop", false, 5},
	{"Move", "let t = popper(q)\ntry q.Push(6)\ntry t.Wait()", "let p = popper\nlet t = p(q)\ntry q.Push(6)\ntry t.Wait()", "let p = popper", true, 6},
	{"OnExit", "scope.OnExit() { q.Close() }\n8", "let o = scope.OnExit\no() { q.Close() }\n8", "let o = scope.OnExit", false, 8},
	{"Push", "try q.Push(9)\ntry q.Pop()", "let push = q.Push\ntry push(9)\ntry q.Pop()", "let push = q.Push", true, 9},
	{"Pop", "try q.Push(10)\nlet v = try q.Pop()\nv", "try q.Push(10)\nlet pop = q.Pop\ntry pop()", "let pop = q.Pop", true, 10},
	{"Values", "try q.Push(5)\ntry q.Push(6)\nq.Close()\nlet mut n = 0\nfor v in q.Values() {\nn += v\n}\nn", "try q.Push(5)\ntry q.Push(6)\nq.Close()\nlet vs = q.Values\nlet mut n = 0\nfor v in vs() {\nn += v\n}\nn", "let vs = q.Values", true, 11},
	{"Close", "q.Close()\n12", "let c = q.Close\nc()\n12", "let c = q.Close", false, 12},
	{"Closed", "q.Close()\nlet v = q.Pop() catch {\ncase queue.Closed: 13\n}\nv", "q.Close()\nlet pop = q.Pop\nlet v = pop() catch {\ncase queue.Closed: 13\n}\nv", "let pop = q.Pop", false, 13},
}

var stdSites = []string{"direct", "funcvalue", "value", "literal", "taskbody", "provider"}

// stdChecks is the model of the checker over the std grid: a std function
// (task.Go, scope.OnExit) is generic, so it is only called or instantiated,
// and a Task's operation taken as a value would let the Task leave its
// function other than by return (runtime.md, Tasks). Every other pair
// checks.
func stdChecks(op stdOp, site string) bool {
	if site != "value" {
		return true
	}
	switch op.name {
	case "GoValue", "GoNoValue", "OnExit", "Wait", "Stop":
		return false
	}
	return true
}

// stdFunc is the pair's declarations and its run function, which prints
// the pair's id and the work's result.
func stdFunc(op stdOp, site string) (id, code string) {
	id = "Std" + op.name + strings.ToUpper(site[:1]) + site[1:]
	const sig = "(q queue.Queue[int]) int fails queue.Closed"
	var b strings.Builder
	var call string
	switch site {
	case "direct", "funcvalue", "value":
		body := op.body
		if site == "value" {
			body = op.value
		}
		fmt.Fprintf(&b, "func %sWork%s {\n%s\n}\n", id, sig, body)
		call = "let r = try " + id + "Work(q)"
		if site == "funcvalue" {
			call = "let f = " + id + "Work\nlet r = try f(q)"
		}
	case "literal":
		call = "let f = func() int fails queue.Closed {\n" + op.body + "\n}\nlet r = try f()"
	case "taskbody":
		wait := "t.Wait()"
		if op.fails {
			wait = "try " + wait
		}
		call = "let t = task.Go() {\n" + op.body + "\n}\nlet r = " + wait
	case "provider":
		fmt.Fprintf(&b, "type %sImpl {}\n\nfunc Run(i %sImpl, q queue.Queue[int]) int fails queue.Closed {\n%s\n}\n", id, id, op.body)
		call = "let p Runner = " + id + "Impl\nlet r = try p.Run(q)"
	default:
		panic("unknown std site: " + site)
	}
	fmt.Fprintf(&b, "\nfunc %s() fails queue.Closed needs Console {\nlet q = queue.New[int](4)\n%s\nConsole.Println(\"%s \\(r)\")\n}\n", id, call, id)
	return id, b.String()
}

// stdProgram assembles one program from pair functions: the imports its
// code uses, the operation type Runner, the Task-returning popper, and a
// main that runs each pair in order.
func stdProgram(ids, funcs []string) string {
	code := strings.Join(funcs, "\n")
	var b strings.Builder
	b.WriteString("import queue\nimport task\n")
	if strings.Contains(code, "scope.") {
		b.WriteString("import scope\n")
	}
	b.WriteString("\ntype Runner { Run(q queue.Queue[int]) int fails queue.Closed }\n\n")
	b.WriteString("func popper(q queue.Queue[int]) Task[int, queue.Closed] {\n\ttask.Go() { try q.Pop() }\n}\n\n")
	b.WriteString(code)
	b.WriteString("\nfunc main() fails queue.Closed needs Console {\n")
	for _, id := range ids {
		b.WriteString("\ttry " + id + "()\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// stdSite checks and lowers each pair of the std grid at site, and returns
// the program of the pairs that lower with the output it prints, or nil
// when none lowers.
func stdSite(t *testing.T, site string) (*lowered, string) {
	t.Helper()
	var ids, funcs, lines []string
	refused, unchecked := 0, 0
	for _, op := range stdOps {
		id, code := stdFunc(op, site)
		src := stdProgram([]string{id}, []string{code})
		o := lowerSource(id, src, true)
		if o.err != nil {
			t.Errorf("%s: %v\n%s", id, o.err, src)
			continue
		}
		checked := len(o.diags) == 0
		if model := stdChecks(op, site); checked != model {
			t.Errorf("%s: checked %v, the std model says %v; diagnostics %s\n%s", id, checked, model, diagText(o.diags), src)
			continue
		}
		if !checked {
			srcLines := strings.Split(src, "\n")
			d := o.diags[0]
			if d.Line < 1 || d.Line > len(srcLines) || !strings.Contains(srcLines[d.Line-1], op.at) {
				t.Errorf("%s: want the first diagnostic on the line with %q, got %s\n%s", id, op.at, diagText(o.diags), src)
			}
			unchecked++
			continue
		}
		if o.panicked != nil {
			t.Errorf("%s: checked, lowering panicked: %v\n%s", id, o.panicked, src)
			continue
		}
		if len(o.refused) > 0 {
			for _, r := range o.refused {
				if why := undeferred(r.Message); why != "" {
					t.Errorf("%s: checked, lowering refused at %d:%d with %s: %q\n%s", id, r.Line, r.Column, why, r.Message, src)
				}
			}
			t.Logf("%s: refused naming its owner: %s", id, o.refused[0].Message)
			refused++
			continue
		}
		ids = append(ids, id)
		funcs = append(funcs, code)
		lines = append(lines, fmt.Sprintf("%s %d", id, op.want))
	}
	t.Logf("std %s: %d pairs run, %d refused naming their owner, %d refused by the checker as the model says", site, len(ids), refused, unchecked)
	if len(ids) == 0 {
		return nil, ""
	}
	name := "std" + site
	return mustLower(t, name, stdProgram(ids, funcs)), strings.Join(lines, "\n") + "\n"
}

// The operand-order axis (stage D3b round 2 review M1): each kind of
// expression whose lowering writes statements stands after an earlier
// operand that fails, in each kind of operand list. The earlier operand,
// early[10 / zero], fails with integer divide by zero and writes no
// statement; the later expression reads xs[0], ss[0] or qs[0] of an empty
// slice, in a statement of its own, which would fail with index out of
// range. Operands run left to right, each once (runtime.md, Evaluation
// order), so every case must fail with the divide. A control per kind, the
// later expression alone, must fail with index out of range: the later
// expression reaches its own failure, so a case that passes is not vacuous.
type orderKind struct {
	name  string
	typ   string // the later expression's type
	late  string
	local string // the local the later expression reads
	fails string // main's error row, when the later expression needs one
}

var orderKinds = []orderKind{
	{"MethodUser", "func() int", "ss[0].Area", "let ss = []Shape{}", ""},
	{"MethodStd", "func() int fails queue.Closed", "qs[0].Pop", "let qs = []queue.Queue[int]{}", ""},
	{"Call", "int", "id(xs[0])", "let xs = []int{}", ""},
	{"TaskWait", "int", "try spawn(xs[0]).Wait()", "let xs = []int{}", "Fault"},
	{"Interpolation", "string", `"\(id(xs[0]))"`, "let xs = []int{}", ""},
	{"Try", "int", "try work(xs[0])", "let xs = []int{}", "Fault"},
	{"Catch", "int", "work(xs[0]) catch { case Fault: 0 }", "let xs = []int{}", ""},
	{"If", "int", "if xs[0] > 0 { 1 } else { 2 }", "let xs = []int{}", ""},
	{"Match", "int", "match xs[0] { case 0: 1; case _: 2 }", "let xs = []int{}", ""},
}

// orderLists are the operand lists, each with @E for the earlier operand,
// @L for the later expression and @T for its type, and the types it takes
// (nil: any).
var orderLists = []struct {
	name, expr string
	types      []string
}{
	{"Args", "take(@E, @L)", nil},
	{"Record", "keep(Rec{A: @E, B: @L})", nil},
	{"Tuple", "keep((@E, @L))", nil},
	{"Slice", "keep([]@T{@E, @L})", nil},
	{"Map", "keep(Map[int, @T]{1: @E, 2: @L})", nil},
	{"Binary", "keep(@E + (@L))", []string{"int", "string"}}, // try and catch bind looser than +
	{"Parts", `keep("\(@E) \(@L)")`, []string{"int", "string"}},
}

// orderProgram is one case's program: the later expression of k in the
// operand list list, after the earlier operand; or, for an empty list, the
// later expression alone (the control).
func orderProgram(k orderKind, list string) string {
	var b strings.Builder
	if strings.Contains(k.typ+k.local, "queue.") {
		b.WriteString("import queue\n")
	}
	b.WriteString("import task\n\n")
	b.WriteString("type Shape { Area() int }\ntype Sq { N int }\nfunc Area(s Sq) int { s.N }\n")
	b.WriteString("type Fault { Code int }\nfunc id(n int) int { n }\nfunc work(n int) int fails Fault { n }\n")
	b.WriteString("func spawn(n int) Task[int, Fault] { task.Go() { n } }\nfunc keep[V](v V) {}\n")
	// A basic type is written as itself, so + and interpolation take it;
	// a function type gets a name, which composite literal types read.
	typ := k.typ
	if typ != "int" && typ != "string" {
		fmt.Fprintf(&b, "type T %s\n", typ)
		typ = "T"
	}
	fmt.Fprintf(&b, "type Rec { A %s, B %s }\nfunc take(a %s, b %s) {}\n", typ, typ, typ, typ)
	row := ""
	if k.fails != "" {
		row = " fails " + k.fails
	}
	fmt.Fprintf(&b, "\nfunc main()%s needs Console {\n\t%s\n", row, k.local)
	if list == "" {
		list = "keep(@L)"
	} else {
		fmt.Fprintf(&b, "\tlet zero = 0\n\tlet early = []%s{}\n", typ)
	}
	expr := strings.NewReplacer("@E", "early[10 / zero]", "@T", typ, "@L", k.late).Replace(list)
	fmt.Fprintf(&b, "\t%s\n\tConsole.Println(\"reached\")\n}\n", expr)
	return b.String()
}

// orderCase is one program of the operand-order axis: a case, or a kind's
// control, with the defect it must end with.
type orderCase struct{ name, src, defect string }

// orderKindNamed is the operand-order kind of that name.
func orderKindNamed(name string) orderKind {
	for _, k := range orderKinds {
		if k.name == name {
			return k
		}
	}
	panic("unknown operand-order kind: " + name)
}

// orderCases is the control of k and each case of k in each operand list
// that takes its type.
func orderCases(k orderKind) []orderCase {
	out := []orderCase{{"Order" + k.name + "Alone", orderProgram(k, ""), "index out of range [0] with length 0"}}
	for _, l := range orderLists {
		if l.types != nil && !slices.Contains(l.types, k.typ) {
			continue
		}
		out = append(out, orderCase{"Order" + k.name + l.name, orderProgram(k, l.expr), "integer divide by zero"})
	}
	return out
}

// orderGrid checks and lowers the control and each case of the
// operand-order kind k, and returns the programs with the defect each must
// end with.
func orderGrid(t *testing.T, k orderKind) ([]*lowered, map[string]string) {
	t.Helper()
	var programs []*lowered
	want := map[string]string{}
	for _, l := range orderLists {
		if l.types != nil && !slices.Contains(l.types, k.typ) {
			t.Logf("Order%s%s: skipped, %s takes no operand of type %s", k.name, l.name, l.name, k.typ)
		}
	}
	for _, c := range orderCases(k) {
		name, src, defect := c.name, c.src, c.defect
		o := lowerSource(name, src, true)
		switch {
		case o.err != nil:
			t.Errorf("%s: %v\n%s", name, o.err, src)
		case len(o.diags) > 0:
			t.Errorf("%s: the checker refused %s\n%s", name, diagText(o.diags), src)
		case o.panicked != nil:
			t.Errorf("%s: checked, lowering panicked: %v\n%s", name, o.panicked, src)
		case len(o.refused) > 0:
			for _, r := range o.refused {
				if why := undeferred(r.Message); why != "" {
					t.Errorf("%s: checked, lowering refused at %d:%d with %s: %q\n%s", name, r.Line, r.Column, why, r.Message, src)
				}
			}
			t.Logf("%s: skipped, lowering refuses it naming its owner: %s", name, o.refused[0].Message)
		default:
			programs = append(programs, o)
			want[name] = defect
		}
	}
	t.Logf("order %s: %d programs run (cases and the kind's control)", k.name, len(programs))
	return programs, want
}

func diagText(ds []check.Diagnostic) string {
	parts := make([]string, len(ds))
	for i, d := range ds {
		parts[i] = fmt.Sprintf("%s:%d %s", d.Code, d.Line, d.Message)
	}
	return "[" + strings.Join(parts, "; ") + "]"
}
