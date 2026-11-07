package sketchtest

import (
	"fmt"
	"math/rand"
	"strings"
)

// The law corpus of law-top (top-level laws): the rule programs of the
// checker's TestTopLevelLawRules (spec A2) and the generated law bodies of
// TestTopLevelLawMatchesMember (spec A3). The lowering's erasure property
// (lower/stage_lawtop_test.go, spec A5) reads the same programs, so the
// corpus lives here, once, for both test packages.

// LawTopDecls are the declarations the top-level law programs share: a
// record and a function over it, a fallible function, and a capability
// with a provider and a function that charges it.
const LawTopDecls = `type Money { cents int }

func add(a Money, b Money) Money {
	Money{cents: a.cents + b.cents}
}

type Bad {}

func valid(n int) bool fails Bad {
	if n < 0 {
		return Bad
	}
	true
}

type Store {
	Get() string,
}

type Mem {}

func Get(_ Mem) string {
	"ok"
}

func read() string {
	Store.Get()
}

`

// lawTopSiteDecls are the frame cases' declarations: an owning context of
// a capability A, and a context of a capability R with a Close.
const lawTopSiteDecls = `type A { Use() int }

type Conn { V int }

func Use(c Conn) int { c.V }

func Close(c Conn) {}

func get() Conn { Conn{V: 1} }

let Owned = context { A: get() }

type R { Close() }

type First {}

func Close(_ First) {}

let Releasing = context { R: First }

`

// LawTopProbe and LawWrapperProbe are the planner's probes
// (check-v1-wave2-plan/probe/law-top.ergo and law-wrapper.ergo), verbatim
// but for the want marker.
const LawTopProbe = `// law-top probe: a top-level law beside test (plan.md 11:45Z).
type Money { cents int }

func add(a Money, b Money) Money {
	Money{cents: a.cents + b.cents}
}

law AddCommutes(a Money, b Money) {
	add(a, b) == add(b, a)
}

test Adds {
	assert add(Money{cents: 1}, Money{cents: 2}) == Money{cents: 3}
}

func main() {}
`

const LawWrapperProbe = `// law-top probe: a type with laws and no operations (types.md Laws rule 1
// admits it today; law-top makes it S108 "write it as a top-level law").
type Money { cents int }

func add(a Money, b Money) Money {
	Money{cents: a.cents + b.cents}
}

type MoneyLaws { // want S108
	law AddCommutes(a Money, b Money) { add(a, b) == add(b, a) },
}

func main() {}
`

// LawTopCases are the programs of TestTopLevelLawRules (spec A2). Each
// marks every diagnostic it must get with `// want CODE`, and gets nothing
// else. A program with no want line is clean. The rule numbers are
// types.md Laws'.
var LawTopCases = []Input{
	{"probe law-top", LawTopProbe},
	{"probe law-wrapper", LawWrapperProbe},
	{"a documented top-level law between two declarations", LawTopDecls + `func sub(a Money, b Money) Money {
	Money{cents: a.cents - b.cents}
}

// AddThenSub: subtracting what was added gives the start back.
// It holds for every pair of amounts.
law AddThenSub(a Money, b Money) {
	sub(add(a, b), b) == a
}
test Adds {
	assert add(Money{cents: 1}, Money{cents: 2}) == Money{cents: 3}
}
`},

	{"a documented top-level law first in the file", `// Reflexive: every int equals itself.
law Reflexive(n int) { n == n }

// Symmetric is the second law.
law Symmetric(a int, b int) {
	(a == b) == (b == a)
}
`},

	// Names (rule 2): one namespace for top-level laws and tests, none for
	// values.
	{"two top-level laws of one name", LawTopDecls + `law Same(a Money) { a == a }

law Same(a Money) { a == a } // want S108
`},
	{"a law, then a test of its name", LawTopDecls + `law Same(a Money) { a == a }

test Same { // want S108
	assert true
}
`},
	{"a test, then a law of its name", LawTopDecls + `test Same {
	assert true
}

law Same(a Money) { a == a } // want S108
`},
	{"a law named like a function, a type and a member law", LawTopDecls + `law add(a Money, b Money) { add(a, b) == add(b, a) }

law Money(a Money) { a == a }

type Codec {
	Encode(v int) string,

	law RoundTrip(v int) { Encode(v) == Encode(v) },
}

law RoundTrip(v int) { v == v }
`},
	{"a law named like a prelude function", `law len(xs []int) { len(xs) >= 0 }
`},
	{"a parameter named like a package function", LawTopDecls + `law Shadows(add Money) { add == add }
`},
	{"a law's name called from a function", LawTopDecls + `law Holds(a Money) { a == a }

func f() bool {
	Holds(Money{cents: 1}) // want S104
}
`},
	{"a law's name called from a law", LawTopDecls + `law A(a Money) { a == a }

law B(a Money) { A(a) } // want S104
`},
	{"a law's name used as a value", LawTopDecls + `law Holds(a Money) { a == a }

func f() {
	let g = Holds // want S104
}
`},

	// Type parameters: refused by the parser at the bracket.
	{"a top-level law with type parameters", `law L[T](x T) { true } // want S001
`},

	// The body rules, as a member law's (rules 4, 7, 8, 9).
	{"a non-bool tail", LawTopDecls + `law Sum(a Money, b Money) { add(a, b) } // want S108
`},
	{"a tail of a type over bool", LawTopDecls + `type Flag bool

law Named(a Money) { Flag(a == a) }
`},
	{"a body ending in a statement", LawTopDecls + `law Stmt(a Money) {
	assert a == a
} // want S108
`},
	{"an empty body", `law Empty(n int) {
} // want S108
`},
	{"a value if of another type", `law Branches(n int) {
	if n > 0 { 1 } else { 2 } // want S108
}
`},
	{"return in a law", `law Early(n int) {
	if n < 0 {
		return true
	}
	n >= 0
}
`},
	{"a return of another type", `law Early(n int) {
	if n < 0 {
		return 1 // want S108
	}
	n >= 0
}
`},
	{"an untried fallible tail", LawTopDecls + `law Untried(n int) { valid(n) } // want S105
`},
	{"a tried fallible tail", LawTopDecls + `law Tried(n int) { try valid(n) }
`},
	{"a caught fallible tail", LawTopDecls + `law Caught(n int) {
	valid(n) catch {
	case Bad:
		false
	}
}
`},
	{"a charging call outside a with", LawTopDecls + `law Charged() { read() == "ok" } // want S102
`},
	{"a capability operation outside a with", LawTopDecls + `law Direct() { Store.Get() == "ok" } // want S102
`},
	{"a charging call inside a with", LawTopDecls + `law Discharged() { with context { Store: Mem } { read() } == "ok" }
`},
	{"assert in the body", LawTopDecls + `law Asserts(a Money) {
	assert a == a
	true
}
`},
	{"assert of a non-bool", `law Asserts(n int) {
	assert 1 // want S108
	true
}
`},
	{"assert in a function literal in the body", `law Lit(n int) {
	let f = func() {
		assert true // want S108
	}
	f()
	true
}
`},
	{"a parameter named twice", `law Twice(n int, n int) { true } // want S108
`},
	{"a parameter without a type", `law Untyped(n) { true } // want S104
`},
	{"an unnamed parameter", `law Unnamed(int) { true }
`},
	{"an unused local", `law Unused(n int) {
	let x = 1 // want S110
	n == n
}
`},
	{"a let mut never reassigned", `law Never(n int) {
	let mut x = n // want S110
	x == n
}
`},
	{"a non-escaping function parameter", `func keep(f escaping func(int) int) bool {
	true
}

func apply(f func(int) int) bool {
	f(1) == f(1)
}

law Applies(f func(int) int) { apply(f) }

law Keeps(f func(int) int) { keep(f) } // want S108

law Escapes(f escaping func(int) int) { keep(f) }
`},

	// Frames: a top-level law is a frame of its own, as a test is.
	{"a task and an owning with in a law", "import task\n\n" + lawTopSiteDecls + `law Spawns(n int) {
	let t = task.Go() { n }
	t.Wait() == n
}

law Owns() { with Owned { A.Use() == 1 } }
`},
	{"a capability's Close in a law", lawTopSiteDecls + `law Twice() { with Releasing { R.Close(); true } } // want S108
`},

	// Alone in the program: an error type, a generic instance and a std
	// package only a top-level law uses (spec Risk 2).
	{"a law alone uses an error type, a generic instance and a std package", "import slices\n\n" + `type Gone {}

type Box[T] { V T }

func wrap[T](v T) Box[T] {
	Box{V: v}
}

law Alone(n int, s string) {
	let f = func() int fails Gone { return Gone }
	let v = f() catch {
	case Gone:
		n
	}
	let xs = slices.WithCapacity[int](n)
	v == n && wrap(s).V == s && len(xs) == 0
}
`},

	// Laws-only types: S108 once at the type, and the laws are still
	// checked, with nothing more.
	{"a laws-only type whose law has its own error", LawTopDecls + `type Wrapper { // want S108
	law Sum(a Money, b Money) { add(a, b) }, // want S108
	law Fine(a Money) { a == a },
}
`},
	{"a laws-only generic type", `type L[T] { // want S108
	law Holds(x T) { true },
}
`},
	// The S104 at the use is rows.md Capability rows 3 (a capability has
	// operations), reported at base 6742cc1c for this program too: a second
	// mistake, not a cascade of the S108.
	{"a laws-only type used as a capability", LawTopDecls + `type Wrapper { // want S108
	law Fine(a Money) { a == a },
}

func uses() needs Wrapper {} // want S104
`},
	{"a laws-only type declared twice", `type W { // want S108
	law A() { true },
}

type W { law B() { true } } // want S108
`},
}

// LawDomainHelpers are the fixed package declarations of the A3 programs.
// No generated name collides with an operation name: the bodies call the
// package function Get, which Store's operation Get shares a name with,
// and never call Op, the member form's operation. A checker that gave a
// top-level law an operation scope (Store's, the package's first operation
// type) would resolve Get to the operation and so differ from the member
// form, whose only operation is Op.
const LawDomainHelpers = `import task

type Money { cents int }

type Flag bool

type Bad {}

type Store {
	Get() string,
}

type Mem {}

func Get(_ Mem) string {
	"ok"
}

func read() string {
	Store.Get()
}

func valid(n int) bool fails Bad {
	if n < 0 {
		return Bad
	}
	true
}

func count(n int) int fails Bad {
	if n < 0 {
		return Bad
	}
	n
}

func keep(f escaping func(int) int) bool {
	true
}

func apply(f func(int) int) bool {
	f(1) == f(1)
}

func noop() {}

func spawn() int {
	let t = task.Go() { 1 }
	t.Wait()
}

`

// LawKind is a generated expression's type, as the generator knows it: the
// test's own account of the tail rule, independent of the checker.
type LawKind int

const (
	KindBool LawKind = iota
	KindFlag
	KindInt
	KindString
	KindNone
)

// Boolish is the second oracle's tail rule (types.md Laws 8): a law's exit
// is a bool when its type's underlying type is bool.
func (k LawKind) Boolish() bool { return k == KindBool || k == KindFlag }

// GenLaw is one generated law: its parameter list, its body lines (one tab
// deep) and the kind of every value exit, the tail last.
type GenLaw struct {
	Params string
	Body   []string
	Exits  []LawKind
}

// GenLaws generates n laws from seed, deterministically.
func GenLaws(seed int64, n int) []GenLaw {
	r := rand.New(rand.NewSource(seed))
	out := make([]GenLaw, n)
	for i := range out {
		out[i] = genLaw(r)
	}
	return out
}

// PlaceLaw is one placement of l: top-level, or a member of an operation
// type T whose one operation Op the body never names. lawLine is the line
// of the law keyword.
func PlaceLaw(member bool, l GenLaw) (src string, lawLine int) {
	var b strings.Builder
	b.WriteString(LawDomainHelpers)
	indent := ""
	if member {
		b.WriteString("type T {\n\tOp() int,\n\n")
		indent = "\t"
	}
	lawLine = strings.Count(b.String(), "\n") + 1
	b.WriteString(indent + "law L(" + l.Params + ") {\n")
	for _, line := range l.Body {
		b.WriteString(indent + line + "\n")
	}
	if member {
		b.WriteString("\t},\n}\n")
	} else {
		b.WriteString("}\n")
	}
	return b.String(), lawLine
}

// lawGen generates one law.
type lawGen struct {
	r      *rand.Rand
	params []genParam
	locals []genLocal
	next   int
	lines  []string
	exits  []LawKind
}

type genParam struct {
	name, typ string
	k         LawKind // KindInt, KindString, KindBool, or KindNone for the rest
	shape     string  // "money", "slice", "func", "escfunc" or ""
}

type genLocal struct {
	name string
	k    LawKind
}

var paramMenu = []genParam{
	{"n", "int", KindInt, ""},
	{"s", "string", KindString, ""},
	{"b", "bool", KindBool, ""},
	{"m", "Money", KindNone, "money"},
	{"xs", "[]int", KindNone, "slice"},
	{"f", "func(int) int", KindNone, "func"},
	{"g", "escaping func(int) int", KindNone, "escfunc"},
}

func genLaw(r *rand.Rand) GenLaw {
	g := &lawGen{r: r}
	perm := r.Perm(len(paramMenu))
	for _, i := range perm[:r.Intn(4)] {
		g.params = append(g.params, paramMenu[i])
	}
	var ps []string
	for _, p := range g.params {
		ps = append(ps, p.name+" "+p.typ)
	}
	for i, n := 0, r.Intn(5); i < n; i++ {
		g.stmt(1)
	}
	g.tail()
	return GenLaw{Params: strings.Join(ps, ", "), Body: g.lines, Exits: g.exits}
}

func (g *lawGen) pick(n int) int { return g.r.Intn(n) }

func (g *lawGen) fresh() string {
	g.next++
	return fmt.Sprintf("x%d", g.next)
}

func (g *lawGen) param(shape string) (genParam, bool) {
	var out []genParam
	for _, p := range g.params {
		if p.shape == shape {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return genParam{}, false
	}
	return out[g.pick(len(out))], true
}

// operand is a name of kind k in scope, a parameter or a local, if any.
func (g *lawGen) operand(k LawKind) (string, bool) {
	var names []string
	for _, p := range g.params {
		if p.k == k && p.shape == "" {
			names = append(names, p.name)
		}
	}
	for _, l := range g.locals {
		if l.k == k {
			names = append(names, l.name)
		}
	}
	if len(names) == 0 {
		return "", false
	}
	return names[g.pick(len(names))], true
}

// expr is a plain (infallible, non-charging) expression of kind k.
func (g *lawGen) expr(k LawKind) string {
	switch k {
	case KindBool:
		switch g.pick(6) {
		case 0:
			return "true"
		case 1:
			if x, ok := g.operand(KindInt); ok {
				return x + " > 0"
			}
		case 2:
			if p, ok := g.param("money"); ok {
				return p.name + " == " + p.name
			}
		case 3:
			if p, ok := g.param("func"); ok {
				return "apply(" + p.name + ")"
			}
			if p, ok := g.param("escfunc"); ok {
				return "keep(" + p.name + ")"
			}
		case 4:
			if x, ok := g.operand(KindBool); ok {
				return x
			}
		case 5:
			return g.expr(KindString) + ` == "ok"`
		}
		return "false"
	case KindFlag:
		return "Flag(" + g.expr(KindBool) + ")"
	case KindInt:
		switch g.pick(5) {
		case 0:
			if x, ok := g.operand(KindInt); ok {
				return x
			}
		case 1:
			if p, ok := g.param("slice"); ok {
				return "len(" + p.name + ")"
			}
		case 2:
			if p, ok := g.param("money"); ok {
				return p.name + ".cents"
			}
		case 3:
			if p, ok := g.param("func"); ok {
				return p.name + "(1)"
			}
		}
		return "1"
	case KindString:
		switch g.pick(3) {
		case 0:
			if x, ok := g.operand(KindString); ok {
				return x
			}
		case 1:
			// The package function Get, never an operation (LawDomainHelpers).
			return "Get(Mem{})"
		}
		return `"a"`
	}
	return "noop()"
}

func (g *lawGen) line(indent int, s string) {
	g.lines = append(g.lines, strings.Repeat("\t", indent)+s)
}

func (g *lawGen) bind(indent int, k LawKind, value string, typed bool) {
	name := g.fresh()
	typ := ""
	if typed {
		typ = map[LawKind]string{KindBool: " bool", KindFlag: " Flag", KindInt: " int", KindString: " string"}[k]
	}
	g.line(indent, "let "+name+typ+" = "+value)
	g.locals = append(g.locals, genLocal{name: name, k: k})
}

// stmt adds one statement at indent.
func (g *lawGen) stmt(indent int) {
	switch g.pick(16) {
	case 0, 1:
		k := []LawKind{KindBool, KindInt, KindString, KindFlag}[g.pick(4)]
		g.bind(indent, k, g.expr(k), g.pick(2) == 0)
	case 2:
		// let mut, with or without its assignment (S110 when without).
		name := g.fresh()
		g.line(indent, "let mut "+name+" = "+g.expr(KindInt))
		if g.pick(3) > 0 {
			g.line(indent, name+" = "+name+" + "+g.expr(KindInt))
		}
		g.locals = append(g.locals, genLocal{name: name, k: KindInt})
	case 3:
		// An early return.
		k := g.exitKind()
		g.line(indent, "if "+g.expr(KindBool)+" {")
		g.line(indent+1, "return "+g.expr(k))
		g.line(indent, "}")
		g.exits = append(g.exits, k)
	case 4:
		g.bind(indent, KindBool, "try valid("+g.expr(KindInt)+")", false)
	case 5:
		g.bind(indent, KindInt, "try count("+g.expr(KindInt)+")", false)
	case 6:
		// An untried fallible call as a statement.
		g.line(indent, "valid("+g.expr(KindInt)+")")
	case 7:
		name := g.fresh()
		g.line(indent, "let "+name+" = valid("+g.expr(KindInt)+") catch {")
		g.line(indent, "case Bad:")
		g.line(indent+1, "false")
		g.line(indent, "}")
		g.locals = append(g.locals, genLocal{name: name, k: KindBool})
	case 8:
		// A charging call, discharged or not.
		if g.pick(2) == 0 {
			g.bind(indent, KindString, "read()", false)
		} else {
			g.bind(indent, KindString, "with context { Store: Mem } { read() }", false)
		}
	case 9:
		g.line(indent, "assert "+g.expr(KindBool))
	case 10:
		// assert in a function literal (S108), called or not.
		name := g.fresh()
		g.line(indent, "let "+name+" = func() {")
		g.line(indent+1, "assert "+g.expr(KindBool))
		g.line(indent, "}")
		if g.pick(2) == 0 {
			g.line(indent, name+"()")
		}
	case 11:
		// A task in the law's frame.
		name := g.fresh()
		g.line(indent, "let "+name+" = task.Go() { "+g.expr(KindInt)+" }")
		if g.pick(2) == 0 {
			g.bind(indent, KindInt, name+".Wait()", false)
		}
	case 12:
		// An unused local.
		g.line(indent, "let "+g.fresh()+" = "+g.expr(KindInt))
	case 13:
		// A function-type parameter passed where it may escape, or not.
		if p, ok := g.param("func"); ok && g.pick(2) == 0 {
			g.bind(indent, KindBool, "keep("+p.name+")", false)
		} else if p, ok := g.param("escfunc"); ok {
			g.bind(indent, KindBool, "keep("+p.name+")", false)
		} else {
			g.line(indent, "noop()")
		}
	case 14:
		g.line(indent, "noop()")
	case 15:
		// A with block as a statement.
		g.line(indent, "with context { Store: Mem } {")
		g.line(indent+1, "assert read() == "+g.expr(KindString))
		g.line(indent, "}")
	}
}

// exitKind draws the kind of a value exit: mostly bool, so most bodies
// pass the tail rule.
func (g *lawGen) exitKind() LawKind {
	switch g.pick(10) {
	case 0:
		return KindInt
	case 1:
		return KindString
	case 2:
		return KindFlag
	}
	return KindBool
}

// tail ends the body.
func (g *lawGen) tail() {
	switch g.pick(10) {
	case 0:
		// No value: the body ends in a statement.
		if g.pick(2) == 0 {
			g.line(1, "assert "+g.expr(KindBool))
		} else {
			g.line(1, "noop()")
		}
		g.exits = append(g.exits, KindNone)
	case 1:
		k := g.exitKind()
		g.line(1, "if "+g.expr(KindBool)+" {")
		g.line(2, g.expr(k))
		g.line(1, "} else {")
		g.line(2, g.expr(k))
		g.line(1, "}")
		g.exits = append(g.exits, k)
	case 2:
		k := g.exitKind()
		g.line(1, "match "+g.expr(KindInt)+" {")
		g.line(1, "case 1: "+g.expr(k))
		g.line(1, "case _: "+g.expr(k))
		g.line(1, "}")
		g.exits = append(g.exits, k)
	case 3:
		k := g.exitKind()
		g.line(1, "return "+g.expr(k))
		g.exits = append(g.exits, k)
	case 4:
		// An untried fallible tail of kind bool: S105, and no rule-8 S108.
		g.line(1, "valid("+g.expr(KindInt)+")")
		g.exits = append(g.exits, KindBool)
	case 5:
		g.line(1, "try valid("+g.expr(KindInt)+")")
		g.exits = append(g.exits, KindBool)
	case 6:
		g.line(1, "with context { Store: Mem } { read() } == "+g.expr(KindString))
		g.exits = append(g.exits, KindBool)
	case 7:
		g.line(1, "read() == "+g.expr(KindString))
		g.exits = append(g.exits, KindBool)
	default:
		k := g.exitKind()
		g.line(1, g.expr(k))
		g.exits = append(g.exits, k)
	}
}

// LawDomainSeed and LawDomainSize fix the A3 domain: the checker's
// differential property and the lowering's erasure property draw the same
// programs.
const (
	LawDomainSeed = 20261104
	LawDomainSize = 2400
)
