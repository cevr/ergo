package lower

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestOwnershipGrammarRun is the end-to-end ownership oracle (lang-audit
// F2, change C2; resources R5-R44). Each seed composes one program from a
// grammar of the ownership forms, not from a list of shapes: expressions
// that make a resource (a call, a literal, a moved local, a try that
// fails, a defect, a division, a pass-through, an if, a match, a with or
// a catch used as a value (an arm may hold a local of its own), a catch
// of a direct Go call (its value an id, or a Go file), a generated helper
// whose return converts), consumers at every position (a let, a typed let
// that converts, a let mut and its reassignment, a lend, a take, a Close,
// a return, a store into a field, a push, a capture by a function literal
// or a trailing block), patterns in let, match and for (tuple, record,
// nested, with an omitted part), loops with break and continue, loops over
// a queue's Values() of every element kind, failures and defects at every
// position, over a resource, an operation type with Close, a record, a
// tuple, a record with its own Close, a record holding an operation type
// and a union. Depth is bounded.
//
// The generator does not decide what is admitted: the checker does. A
// drawn program the checker refuses is drawn again (a sub-seed), so a
// checker that admits a wrong program (an omitted part, a lost owner) is
// caught at run time. The only assertions are the ownership promise
// itself: every value opened is closed exactly once, never used after its
// close, released at its reassignment (R41), no Go file is left open (a
// run counts the process's files), and the static and the list release
// lowerings (static.go, releaseLowering "scope") print the same trace and
// exit alike. The batch
// must reach every production of the grammar in an admitted program; one
// not reached fails by name. A failing seed is shrunk (statements
// removed while it still fails), and prints the Ergo source, both outputs
// and the replay command (ERGO_SEED=n).
//
// It builds Go because its subject is the emitted release code on the
// failure, defect and loop edges of both lowerings: go/types cannot run
// them, and Ergo has no reference interpreter yet (test-speed T8).
//
// Mutants it catches that nothing else does (internal/sketch/check and
// lower mutants.json, owgen-*): a conversion at a lending argument with no
// owner (F2); a lent arm's yielded name owned twice (FU0); a returned new
// value lost on a later release's defect (FU1); the value of an if,
// match, with, catch or Go-call catch lost on a later release of its arm
// (FU4); a written Close that
// skips its fields, or a field taken from under it (FU2); a reassigned
// variant's temporary of the wrong Go type. It also kills the historical
// holes found by hand before it: B1 M1, B3q M2, PO1 M3 and PO1 (their
// ledger mutants name it as a killer).
func TestOwnershipGrammarRun(t *testing.T) {
	needsBuild(t)
	first, n := hrSeeds(t, ogSeeds)
	replay := os.Getenv("ERGO_SEED") != ""
	if s := os.Getenv("ERGO_OWNGEN_SEEDS"); s != "" && !replay {
		// A wider search: FIRST:COUNT seeds instead of the gate's batch.
		if _, err := fmt.Sscanf(s, "%d:%d", &first, &n); err != nil {
			t.Fatalf("ERGO_OWNGEN_SEEDS %q: want FIRST:COUNT", s)
		}
	}
	var cases []*ogCase
	covered := map[string]bool{}
	for seed := first; seed < first+n; seed++ {
		c := ogAdmit(t, seed)
		if c == nil {
			continue
		}
		cases = append(cases, c)
		for f := range c.prog.features {
			covered[f] = true
		}
	}
	verdicts := ogRun(t, cases)
	reported := false
	for i, c := range cases {
		v := verdicts[i]
		if v.fault == "" {
			continue
		}
		if !reported {
			// Shrink the first failure only: each round builds once.
			c, v = ogShrink(t, c, v)
			reported = true
		}
		t.Errorf("seed %d (ERGO_SEED=%d go test -run TestOwnershipGrammarRun ./internal/sketch/lower replays): %s\n%s\nstatic:\n%s\nlist:\n%s", c.seed, c.seed, v.fault, numbered(c.src), v.static, v.list)
	}
	if replay {
		return
	}
	var missing []string
	for _, f := range ogFeatures {
		if !covered[f] {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		t.Errorf("no admitted program of the batch reached these productions: %s", strings.Join(missing, ", "))
	}
}

// ogSeeds is the batch: one program per seed, each lowered twice.
const ogSeeds = 64

// ogFeatures is every production the batch must reach.
var ogFeatures = []string{
	"res:open", "res:lit", "res:local", "res:try", "res:boom", "res:div", "res:pass", "res:helper",
	"expr:if", "expr:match", "expr:catch", "catch:bad-local", "expr:with", "with:lend-local", "with:bad-local", "catch:go", "catch:go-bad-local", "catch:go-file", "catch:go-file-bad-local", "arm:local", "if:lend-local", "if:lend-local-bad", "if:lend-local-catch", "tup:give2",
	"conv:lend", "conv:take", "conv:let", "conv:return", "conv:store", "conv:assign",
	"variant:let", "variant:lend",
	"stmt:let", "stmt:letmut", "stmt:assign", "assign:release", "update:Pair", "spread:copy", "spread:none", "part:div-then-local", "call:div-then-local", "stmt:lend-local", "stmt:lend-temp", "stmt:use", "stmt:take", "stmt:close",
	"stmt:if", "stmt:for", "stmt:break", "stmt:continue", "stmt:fail", "stmt:boom", "stmt:return",
	"pat:let:tup", "pat:let:pair", "pat:let:box", "pat:let:nested",
	"loop:values:res", "loop:values:tup", "loop:values:pair",
	"match:slot", "match:pair", "match:tup", "match:local", "match:temp", "match:lent-if",
	"capture:let", "capture:twice",
	"helper:lent", "helper:taken", "helper:bad-arg", "helper:bad-ret-if", "helper:bad-ret-catch", "helper:ret-temp",
	"close:guard",
}

// ogType is a type of the generated program that holds a resource.
type ogType uint8

const (
	oRes    ogType = iota // Res: a record with a checked Close
	oCloser               // Closer: an operation type with Use and Close
	oPair                 // Pair{R Res, S Res}
	oTup                  // (Res, Res)
	oGuard                // Guard{R Res}, with its own Close
	oBox                  // Box{C Usable, n int}: an operation type stored in a field
	oSlot                 // Slot = Full{R Res} | Vacant
	oTypes
)

var ogTypeName = [oTypes]string{"Res", "Usable", "Pair", "(Res, Res)", "Guard", "Box", "Slot"}
var ogTypeKey = [oTypes]string{"Res", "Closer", "Pair", "Tup", "Guard", "Box", "Slot"}

// ogPrelude declares the types and the fixed functions. Every event goes
// to the log, a sync.Ref, because a Close that converts to an operation
// type may need no capability; main prints the log once run has ended,
// in a member of a LogFailures set, so a defect still prints it.
const ogPrelude = `import sync
import tasks

type Res { id int, bad bool, log sync.Ref[string] } implements Usable
type Bad { n int }
type Usable { ..Closer, Use() }
type Pair { R Res, S Res }
type Guard { R Res } implements Closer
type Box { C Usable, n int }
type Full { R Res }
type Vacant {}
type Slot = Full | Vacant

func note(log sync.Ref[string], s string) {
	log.Update(func(t string) string { t + s + "\n" })
}

func Close(r Res) {
	note(r.log, "close \(r.id)")
	if r.bad {
		Unreachable("close \(r.id)")
	}
}

func Use(r Res) {
	note(r.log, "use \(r.id)")
}

func Close(g Guard) {
	g.R.Use()
}

func made(log sync.Ref[string], id int) int {
	note(log, "open \(id)")
	id
}

func open(id int, bad bool, log sync.Ref[string]) Res {
	Res{id: made(log, id), bad: bad, log: log}
}

func openf(id int, bad bool, log sync.Ref[string], n int, c int) Res fails Bad {
	if n > c {
		return Bad{n: n}
	}
	open(id, bad, log)
}

func openb(id int, bad bool, log sync.Ref[string], n int, c int) Res {
	if n > c {
		Unreachable("boom")
	}
	open(id, bad, log)
}

func give2(a int, b int, log sync.Ref[string], n int, c int) (Res, Res) fails Bad {
	return (open(a, false, log), try openf(b, false, log, n, c))
}

func pass(r Res) Res takes r {
	r
}

func swap(r Res, id int, log sync.Ref[string]) Res {
	open(id, false, log)
}

func slotOf(s Slot) Slot takes s {
	s
}

func fail(n int, c int) fails Bad {
	if n > c {
		return Bad{n: n}
	}
}

func boom(n int, c int) {
	if n > c {
		Unreachable("boom")
	}
}

func twice(f func()) {
	f()
	f()
}

func lendRes(r Res) { r.Use() }
func lendCloser(c Usable) { c.Use() }
func lendPair(p Pair) {
	p.R.Use()
	p.S.Use()
}
func lendTup(t (Res, Res)) {}
func lendGuard(g Guard) { g.R.Use() }
func lendBox(b Box) { b.C.Use() }
func lendSlot(s Slot) {}

func keepRes(r Res) takes r {}
func keepCloser(c Usable) takes c {}
func keepPair(p Pair) takes p {}
func keepTup(t (Res, Res)) takes t {}
func keepGuard(g Guard) takes g {}
func keepBox(b Box) takes b {}
func keepSlot(s Slot) takes s {}

type Core { R Res }
type Wide { ..Core, X Res }
type Thin { ..Core, n int }

func wideOf(w Wide) Wide takes w {
	w
}

func lendThin(t Thin) { t.R.Use() }
func keepThin(t Thin) takes t {}
func keepTwo(a Res, b Res) takes a, b {}

`

// ogFilePrelude declares the Go file consumers and fdCount, the number
// of the process's open files (Linux's /proc; 0 where it has none), for
// a program that opens Go files (goFile).
const ogFilePrelude = `func lendFile(f go.Ref[os.File]) {
	_ = f.Name()
}

func keepFile(f go.Ref[os.File]) takes f {}

func fdCount() int needs go.Foreign {
	let mut n = 0
	for i in 0..1024 {
		os.Readlink("/proc/self/fd/\(i)") catch {
		case go.Error:
			n = n - 1
		}
		n = n + 1
	}
	n
}

`

// ogDevNull opens a Go file that always opens.
const ogDevNull = `os.Open("/dev/null") catch { case go.Error: Unreachable("/dev/null") }`

// ogStmt is a generated statement: a line (it may hold several lines),
// or a compound whose line is its header and whose arms hold blocks.
type ogStmt struct {
	line  string
	arms  []ogArm
	after string // a line after the compound's closing brace
	// mutVar is the local a let declares: "\x00" in line is "mut " when
	// a later statement reassigns it (S110 otherwise).
	mutVar *ogVar
}

// ogArm is one block of a compound: head is written at the compound's
// indent before the block ("" for the first block of an if or a for).
type ogArm struct {
	head string
	body []*ogStmt
}

type ogFunc struct {
	sig  string
	body []*ogStmt
	ret  string
	tail bool // ret is the body's value, not a return statement
}

type ogProg struct {
	helpers []*ogFunc
	body    []*ogStmt
	// divIDs are the ids of division literals: made with no open event,
	// each at most once (they are generated only where code runs once).
	divIDs   map[int]bool
	features map[string]bool
}

type ogVar struct {
	name string
	t    ogType
	st   uint8 // 0 owned, 1 moved, 2 maybe moved
	mut  bool
	lent bool // lent to this frame: lend only
	sc   int  // its scope's index
	read bool // some statement reads it (S110 otherwise)
	bad  bool // it may hold a value whose Close raises a defect
	// assigned: a later statement reassigns it, so its let says mut.
	assigned bool
}

// ogGen draws one program. It tracks each local's state as the checker
// would, so most drawn programs are admitted; it is not the judge.
type ogGen struct {
	r      *rand.Rand
	p      *ogProg
	nextID int
	nextV  int
	scopes [][]*ogVar
	floor  int  // scopes below this index are lent here: a loop or a function literal body
	multi  bool // this code may run more than once
	noTry  bool // a function literal: no try
	// badSince: a bad value was drawn since the statement began, so the
	// locals it declares may hold one (ogVar.bad).
	badSince bool
	inLoop   bool // a range loop's body: break and continue
	inRun    bool // run's frame: return n
	ret      ogType
	helpers  int // helper nesting
	pos      string
}

func (g *ogGen) f(name string) { g.p.features[name] = true }

func (g *ogGen) id() int { g.nextID++; return g.nextID }

// bad draws a value whose Close raises a defect.
func (g *ogGen) bad() bool {
	b := g.r.IntN(9) == 0
	g.badSince = g.badSince || b
	return b
}

func (g *ogGen) name(prefix string) string {
	g.nextV++
	return fmt.Sprintf("%s%d", prefix, g.nextV)
}

func (g *ogGen) declare(name string, t ogType, mut, lent bool) *ogVar {
	v := &ogVar{name: name, t: t, mut: mut, lent: lent, sc: len(g.scopes) - 1, bad: g.badSince}
	g.scopes[len(g.scopes)-1] = append(g.scopes[len(g.scopes)-1], v)
	return v
}

// pickOf picks a live local of any of the types ts, or nil.
func (g *ogGen) pickOf(take bool, ts ...ogType) *ogVar {
	var vs []*ogVar
	for _, t := range ts {
		vs = append(vs, g.vars(t, take)...)
	}
	if len(vs) == 0 {
		return nil
	}
	return vs[g.r.IntN(len(vs))]
}

// vars lists the live locals of type t: consumable ones only when take.
func (g *ogGen) vars(t ogType, take bool) []*ogVar {
	var out []*ogVar
	for i, s := range g.scopes {
		for _, v := range s {
			if v.t != t || v.st != 0 {
				continue
			}
			if take && (v.lent || i < g.floor) {
				continue
			}
			out = append(out, v)
		}
	}
	return out
}

func (g *ogGen) pickVar(t ogType, take bool) *ogVar {
	vs := g.vars(t, take)
	if len(vs) == 0 {
		return nil
	}
	v := vs[g.r.IntN(len(vs))]
	v.read = true
	return v
}

// flush lends each local of the innermost scope that nothing read yet,
// so no local is unused (S110).
func (g *ogGen) flush() []*ogStmt {
	var out []*ogStmt
	for _, v := range g.scopes[len(g.scopes)-1] {
		if !v.read && v.st == 0 {
			v.read = true
			out = append(out, &ogStmt{line: fmt.Sprintf("lend%s(%s)", ogTypeKey[v.t], v.name)})
		}
	}
	return out
}

// pin makes every local of the frame lend-only: a function literal bound
// by let may still run while it lives, so nothing it captured may move.
func (g *ogGen) pin() {
	for _, s := range g.scopes {
		for _, v := range s {
			v.lent = true
		}
	}
}

// save and join merge the local states of two paths.
func (g *ogGen) save() map[*ogVar]uint8 {
	m := map[*ogVar]uint8{}
	for _, s := range g.scopes {
		for _, v := range s {
			m[v] = v.st
		}
	}
	return m
}

func (g *ogGen) restore(m map[*ogVar]uint8) {
	for v, st := range m {
		v.st = st
	}
}

func (g *ogGen) join(other map[*ogVar]uint8) {
	for v, st := range other {
		if v.st != st {
			v.st = 2
		}
	}
}

func (g *ogGen) cond() int { return g.r.IntN(9) }

// expr draws an expression that gives a value of type t to its consumer
// (g.pos names the consumer): it moves the locals it uses.
func (g *ogGen) expr(t ogType, d int) string {
	for {
		k := g.r.IntN(10)
		if d <= 0 && k >= 5 {
			k = g.r.IntN(5)
		}
		switch t {
		case oRes:
			switch k {
			case 0:
				g.f("res:open")
				return fmt.Sprintf("open(%s, %v, log)", g.idExpr(), g.bad())
			case 1:
				g.f("res:lit")
				return fmt.Sprintf("Res{id: made(log, %s), bad: %v, log: log}", g.idExpr(), g.bad())
			case 2, 3:
				if v := g.pickVar(oRes, true); v != nil {
					v.st = 1
					g.f("res:local")
					return v.name
				}
			case 4:
				if g.noTry {
					continue
				}
				g.f("res:try")
				return fmt.Sprintf("try openf(%d, %v, log, n, %d)", g.id(), g.bad(), g.cond())
			case 5:
				g.f("res:boom")
				return fmt.Sprintf("openb(%d, %v, log, n, %d)", g.id(), g.bad(), 3+g.r.IntN(9))
			case 6:
				if g.multi {
					continue
				}
				id := g.id()
				g.p.divIDs[id] = true
				g.f("res:div")
				return fmt.Sprintf("Res{id: %d + 0 / (n - %d), bad: false, log: log}", id, 1+g.r.IntN(4))
			case 7:
				g.f("res:pass")
				pos := g.pos
				g.pos = "take"
				e := g.expr(oRes, d-1)
				g.pos = pos
				return "pass(" + e + ")"
			case 8:
				return g.armForm(t, d)
			case 9:
				if h := g.helper(t, d); h != "" {
					g.f("res:helper")
					return h
				}
			}
		case oCloser:
			switch k {
			case 0, 1, 2:
				g.f("conv:" + g.pos)
				return g.expr(oRes, d)
			case 3:
				if v := g.pickVar(oCloser, true); v != nil {
					v.st = 1
					return v.name
				}
			case 4, 5:
				if v := g.pickVar(oRes, true); v != nil {
					v.st = 1
					g.f("conv:" + g.pos)
					return v.name
				}
			case 6, 7:
				return g.armForm(t, d)
			default:
				if h := g.helper(t, d); h != "" {
					return h
				}
			}
		case oPair, oGuard, oBox, oSlot, oTup:
			switch {
			case k < 4:
				return g.literal(t, d)
			case k < 6:
				if v := g.pickVar(t, true); v != nil {
					v.st = 1
					return v.name
				}
			case k < 7 && t == oTup && !g.noTry:
				g.f("tup:give2")
				return fmt.Sprintf("try give2(%d, %d, log, n, %d)", g.id(), g.id(), g.cond())
			case k < 8:
				if t == oSlot && g.r.IntN(2) == 0 {
					return "Vacant"
				}
				return g.armForm(t, d)
			default:
				if h := g.helper(t, d); h != "" {
					return h
				}
			}
		}
	}
}

// literal builds a value of t from parts, left to right.
func (g *ogGen) literal(t ogType, d int) string {
	pos := g.pos
	defer func() { g.pos = pos }()
	g.pos = "store"
	if (t == oPair || t == oGuard) && d > 0 && g.r.IntN(4) == 0 {
		// An update: the base moves into the value, and the field it
		// names is released at the statement's end (R43, Info.Replaced).
		// A Guard has its own Close, so its update is refused (R39).
		g.f("update:" + ogTypeKey[t])
		base := g.expr(t, d-1)
		field := "R"
		if t == oPair && g.r.IntN(2) == 0 {
			field = "S"
		}
		return fmt.Sprintf("%s{..%s, %s: %s}", ogTypeName[t], base, field, g.expr(oRes, d-1))
	}
	if (t == oPair || t == oTup) && !g.multi && g.r.IntN(5) == 0 {
		// A division before a moved local: Go runs the local's hand-off
		// call before the division unless the division is spilled
		// (handsOff; wide seed 1450).
		if v := g.pickVar(oRes, true); v != nil {
			v.st = 1
			g.f("part:div-then-local")
			id := g.id()
			g.p.divIDs[id] = true
			div := fmt.Sprintf("Res{id: %d + 0 / (n - %d), bad: false, log: log}", id, 1+g.r.IntN(4))
			if t == oPair {
				return fmt.Sprintf("Pair{R: %s, S: %s}", div, v.name)
			}
			return fmt.Sprintf("(%s, %s)", div, v.name)
		}
	}
	switch t {
	case oPair:
		a := g.expr(oRes, d-1)
		return fmt.Sprintf("Pair{R: %s, S: %s}", a, g.expr(oRes, d-1))
	case oTup:
		a := g.expr(oRes, d-1)
		return fmt.Sprintf("(%s, %s)", a, g.expr(oRes, d-1))
	case oGuard:
		return fmt.Sprintf("Guard{R: %s}", g.expr(oRes, d-1))
	case oBox:
		return fmt.Sprintf("Box{C: %s, n: %d}", g.expr(oCloser, d-1), g.r.IntN(5))
	case oSlot:
		g.f("variant:" + pos)
		return fmt.Sprintf("Full{R: %s}", g.expr(oRes, d-1))
	}
	panic("literal of a leaf type")
}

// ifExpr is an if used as a value: each branch gives one.
func (g *ogGen) ifExpr(t ogType, d int) string {
	g.f("expr:if")
	c := g.cond()
	before := g.save()
	a := ""
	if g.pos == "lend" && g.r.IntN(2) == 0 {
		// A lent if whose arm yields an enclosing local: the local is
		// lent, not the statement's temporary (R43, the mHold rule).
		if v := g.pickVar(t, true); v != nil {
			v.st = 1
			a = g.lentYield(v.name)
			g.f("if:lend-local")
		}
	}
	if a == "" {
		a = g.arm(t, d)
	}
	then := g.save()
	g.restore(before)
	b := g.arm(t, d)
	g.join(then)
	return fmt.Sprintf("if n > %d { %s } else { %s }", c, a, b)
}

// lentYield is the body of a lent arm that yields the enclosing name: the
// name alone, or after a local of the arm whose release, after the yield,
// raises a defect. The name keeps its owner, which releases it once (FU0,
// FU4).
func (g *ogGen) lentYield(name string) string {
	if g.r.IntN(2) == 0 {
		return name
	}
	a := g.name("a")
	g.badSince = true
	g.f("if:lend-local-bad")
	return fmt.Sprintf("let %s = open(%d, true, log); lendRes(%s); %s", a, g.id(), a, name)
}

// armForm draws a value form whose arms each give a value of t: an if, a
// match, a with, or (for a resource) a catch.
func (g *ogGen) armForm(t ogType, d int) string {
	switch k := g.r.IntN(10); {
	case k < 2:
		return g.matchExpr(t, d)
	case k < 4 && t == oRes:
		return g.catchExpr(d)
	case k >= 4 && k < 6:
		return g.withExpr(t, d)
	}
	return g.ifExpr(t, d)
}

// withExpr is a with used as a value: its block gives the value, after
// a local of the block whose release may raise a defect (FU4), or, lent,
// yields an enclosing local (FU0).
func (g *ogGen) withExpr(t ogType, d int) string {
	g.f("expr:with")
	body := ""
	if g.pos == "lend" && g.r.IntN(2) == 0 {
		if v := g.pickVar(t, true); v != nil {
			v.st = 1
			body = g.lentYield(v.name)
			g.f("with:lend-local")
		}
	}
	if body == "" && g.r.IntN(3) == 0 {
		l := g.name("a")
		g.badSince = true
		g.f("with:bad-local")
		body = fmt.Sprintf("let %s = open(%d, true, log); lendRes(%s); %s", l, g.id(), l, g.expr(t, d-1))
	}
	if body == "" {
		body = g.arm(t, d)
	}
	return "with context {} { " + body + " }"
}

// idExpr is the id of a new resource: a literal, or a catch of a direct
// Go call (the plain catch, catch_plain.go) that fails when n > c, whose
// arm may release a local of its own that raises a defect while the
// statement's earlier temporaries are live.
func (g *ogGen) idExpr() string {
	id := g.id()
	if g.r.IntN(6) != 0 {
		return fmt.Sprint(id)
	}
	g.f("catch:go")
	arm := fmt.Sprint(id)
	if g.r.IntN(2) == 0 {
		g.f("catch:go-bad-local")
		l := g.name("a")
		g.badSince = true
		arm = fmt.Sprintf("let %s = open(%d, true, log); lendRes(%s); %d", l, g.id(), l, id)
	}
	return fmt.Sprintf("strconv.Atoi(if n > %d { \"x\" } else { \"%d\" }) catch { case go.Error: %s }", g.cond(), id, arm)
}

// goFile is a Go resource (interop.md, Resources) made by a plain catch:
// the call's file when it opens, else the arm's, which the arm makes
// before it releases a local of its own that may raise a defect (FU4 at
// a direct Go call).
func (g *ogGen) goFile() string {
	g.f("catch:go-file")
	arm := ogDevNull
	if g.r.IntN(2) == 0 {
		g.f("catch:go-file-bad-local")
		l := g.name("a")
		g.badSince = true
		arm = fmt.Sprintf("let %s = open(%d, true, log); lendRes(%s); %s", l, g.id(), l, arm)
	}
	return fmt.Sprintf("os.Open(if n > %d { \"/nonexistent/loop-probe-x\" } else { \"/dev/null\" }) catch { case go.Error: %s }", g.cond(), arm)
}

// arm is the body of an arm of a value form: its tail, sometimes after a
// local of the arm, which the arm releases after its tail unless the
// tail moves it (R43).
func (g *ogGen) arm(t ogType, d int) string {
	if d <= 0 || g.r.IntN(3) != 0 {
		return g.expr(t, d-1)
	}
	g.scopes = append(g.scopes, nil)
	pre := g.armLocal(d)
	tail := g.expr(t, d-1)
	g.scopes = g.scopes[:len(g.scopes)-1]
	return pre + tail
}

// armLocal declares a local of the arm, in the innermost scope, and lends
// it: the statements before an arm's tail.
func (g *ogGen) armLocal(d int) string {
	g.f("arm:local")
	pos := g.pos
	g.pos = "let"
	init := g.expr(oRes, d-1)
	g.pos = pos
	name := g.name("a")
	g.declare(name, oRes, false, false).read = true
	return fmt.Sprintf("let %s = %s; lendRes(%s); ", name, init, name)
}

// matchExpr is a match used as a value over a Slot: an owned local, whose
// binding the arm may move, or a temporary, whose binding is lent.
func (g *ogGen) matchExpr(t ogType, d int) string {
	g.f("expr:match")
	var subj string
	lent := true
	if v := g.pickVar(oSlot, true); v != nil && g.r.IntN(2) == 0 {
		v.st = 2
		subj, lent = v.name, false
	} else {
		pos := g.pos
		g.pos = "match"
		subj = "slotOf(" + g.literal(oSlot, d-1) + ")"
		g.pos = pos
	}
	before := g.save()
	g.scopes = append(g.scopes, nil)
	m := g.declare(g.name("m"), oRes, false, lent)
	a := g.arm(t, d)
	g.scopes = g.scopes[:len(g.scopes)-1]
	bind := "_"
	if m.read {
		bind = m.name
	}
	then := g.save()
	g.restore(before)
	b := g.arm(t, d)
	g.join(then)
	return fmt.Sprintf("match %s { case Full{R: %s}: %s; case Vacant: %s }", subj, bind, a, b)
}

// catchExpr is a catch used as a value: its arm gives the resource when
// the call fails.
func (g *ogGen) catchExpr(d int) string {
	g.f("expr:catch")
	call := fmt.Sprintf("openf(%d, %v, log, n, %d)", g.id(), g.bad(), g.cond())
	before := g.save()
	a := ""
	if g.r.IntN(2) == 0 {
		// The arm releases a local of its own after its new value, and
		// the release raises a defect (FU4).
		l := g.name("a")
		g.badSince = true
		g.f("catch:bad-local")
		a = fmt.Sprintf("let %s = open(%d, true, log); lendRes(%s); %s", l, g.id(), l, g.expr(oRes, d-1))
	} else {
		a = g.arm(oRes, d)
	}
	g.join(before)
	return fmt.Sprintf("%s catch { case Bad: %s }", call, a)
}

// helper declares a function that returns a value of t, with a lent or a
// taken parameter or none, and returns a call of it.
func (g *ogGen) helper(t ogType, d int) string {
	if g.noTry || g.helpers >= 2 || len(g.p.helpers) >= 4 {
		return ""
	}
	kind := g.r.IntN(3)
	arg, param, takes := "", "", ""
	badArg := false
	switch kind {
	case 1:
		g.f("helper:lent")
		if v := g.pickVar(oRes, false); v != nil && g.r.IntN(2) == 0 {
			arg = ", " + v.name
		} else {
			pos := g.pos
			g.pos = "lend"
			arg = ", " + g.expr(oRes, d-1)
			g.pos = pos
		}
		param = ", a Res"
	case 2:
		g.f("helper:taken")
		if t == oRes && g.r.IntN(2) == 0 || g.r.IntN(3) == 0 {
			// A frame that owns a value whose release raises a defect
			// after its result is made (R10).
			arg = fmt.Sprintf(", open(%d, true, log)", g.id())
			g.badSince = true
			badArg = true
		} else {
			pos := g.pos
			g.pos = "take"
			arg = ", " + g.expr(oRes, d-1)
			g.pos = pos
		}
		param, takes = ", a Res", " takes a"
	}
	saved := *g
	g.scopes = [][]*ogVar{nil}
	g.floor, g.multi, g.inLoop, g.inRun, g.ret = 0, true, false, false, t
	g.helpers++
	if kind != 0 {
		g.declare("a", oRes, false, kind == 1)
	}
	// Its name is taken after its argument and before its body draw
	// others.
	name := fmt.Sprintf("h%d", len(g.p.helpers))
	fn := &ogFunc{sig: fmt.Sprintf("func %s(log sync.Ref[string], n int%s) %s fails Bad%s", name, param, ogTypeName[t], takes)}
	g.p.helpers = append(g.p.helpers, fn)
	fn.body = g.stmts(1+g.r.IntN(3), 1)
	fn.body = append(fn.body, g.flush()...)
	g.pos = "return"
	if badArg && t == oRes && g.r.IntN(3) != 0 {
		// A returned catch: its call's value on success, and its arm's,
		// are the frame's until its exit (R10).
		fn.ret = g.catchExpr(2)
		g.f("helper:bad-ret-catch")
	} else if badArg && g.r.IntN(2) == 0 {
		// A returned if: each arm's value is the frame's until its exit.
		fn.ret = g.ifExpr(t, 2)
		g.f("helper:bad-ret-if")
	} else {
		fn.ret = g.expr(t, 2)
	}
	if badArg {
		g.f("helper:bad-arg")
	}
	if kind == 0 && (t == oRes || t == oCloser) && g.r.IntN(3) == 0 {
		// The frame owns nothing; the return's own temporary is released
		// after the new value it gives, and raises a defect (R10; FU4).
		fn.body = nil
		fn.ret = fmt.Sprintf("swap(open(%d, true, log), %d, log)", g.id(), g.id())
		fn.tail = g.r.IntN(2) == 0
		g.f("helper:ret-temp")
	}
	nextID, nextV := g.nextID, g.nextV
	*g = saved
	g.nextID, g.nextV = nextID, nextV
	return fmt.Sprintf("try %s(log, n%s)", name, arg)
}

func (g *ogGen) stmts(n, d int) []*ogStmt {
	var out []*ogStmt
	for i := 0; i < n; i++ {
		out = append(out, g.stmt(d))
	}
	return out
}

// block draws a block in a scope of its own.
func (g *ogGen) block(n, d int) []*ogStmt {
	g.scopes = append(g.scopes, nil)
	out := g.stmts(n, d)
	out = append(out, g.flush()...)
	g.scopes = g.scopes[:len(g.scopes)-1]
	return out
}

// framed draws a block that runs as a loop body or a function literal:
// it may lend outer locals and consume only its own.
func (g *ogGen) framed(n, d int, loop, lit bool, bind func()) []*ogStmt {
	saved := *g
	g.scopes = append(g.scopes, nil)
	g.floor = len(g.scopes) - 1
	g.multi = true
	g.inLoop = loop
	if lit {
		g.noTry, g.inRun = true, false
	}
	if bind != nil {
		bind()
	}
	out := g.stmts(n, d)
	out = append(out, g.flush()...)
	nextID, nextV := g.nextID, g.nextV
	*g = saved
	g.nextID, g.nextV = nextID, nextV
	return out
}

func (g *ogGen) anyType() ogType { return ogType(g.r.IntN(int(oTypes))) }

func (g *ogGen) stmt(d int) *ogStmt {
	for {
		g.pos = ""
		g.badSince = false
		switch g.r.IntN(29) {
		case 28:
			// A call whose division argument comes before a moved local
			// (handsOff at callArgs).
			v := g.pickVar(oRes, true)
			if v == nil || g.multi {
				continue
			}
			v.st = 1
			g.f("call:div-then-local")
			id := g.id()
			g.p.divIDs[id] = true
			return &ogStmt{line: fmt.Sprintf("keepTwo(Res{id: %d + 0 / (n - %d), bad: false, log: log}, %s)", id, 1+g.r.IntN(4), v.name)}
		case 27:
			// A spread from Wide to Thin: it copies R, or names it and
			// copies nothing; either way it drops X, which the statement
			// releases at its end (Info.Replaced).
			g.pos = "store"
			e := fmt.Sprintf("Wide{R: %s, X: %s}", g.expr(oRes, 1), g.expr(oRes, 1))
			if g.r.IntN(2) == 0 {
				e = "wideOf(" + e + ")"
			}
			var s string
			if g.r.IntN(2) == 0 {
				g.f("spread:none")
				s = fmt.Sprintf("Thin{..%s, R: %s, n: 1}", e, g.expr(oRes, 1))
			} else {
				g.f("spread:copy")
				s = fmt.Sprintf("Thin{..%s, n: 1}", e)
			}
			if g.r.IntN(2) == 0 {
				return &ogStmt{line: "lendThin(" + s + ")"}
			}
			return &ogStmt{line: "keepThin(" + s + ")"}
		case 24:
			// A Go file, in run's frame only (go.Foreign is in its row,
			// and main provides it): bound and lent, lent as a
			// temporary, or taken.
			if !g.inRun || g.noTry {
				continue
			}
			e := g.goFile()
			switch g.r.IntN(3) {
			case 0:
				name := g.name("f")
				return &ogStmt{line: fmt.Sprintf("let %s = %s; lendFile(%s)", name, e, name)}
			case 1:
				return &ogStmt{line: "lendFile(" + e + ")"}
			}
			return &ogStmt{line: "keepFile(" + e + ")"}
		case 0, 1, 2:
			t := g.anyType()
			mut := g.r.IntN(3) == 0
			if mut && g.r.IntN(3) != 0 {
				// Its reassignment converts (conv:assign), or assigns a
				// variant to its union.
				t = []ogType{oCloser, oSlot}[g.r.IntN(2)]
			} else if mut && g.r.IntN(2) == 0 {
				t = oRes // its reassignment's release is traced (assign:release)
			}
			name := g.name("x")
			g.pos = "let"
			e := g.expr(t, 2)
			v := g.declare(name, t, mut, false)
			g.f("stmt:let")
			if mut {
				g.f("stmt:letmut")
			}
			// The let says mut only when a later statement reassigns it.
			line := fmt.Sprintf("let \x00%s %s = %s", name, ogTypeName[t], e)
			if mut && t == oRes && g.r.IntN(2) == 0 {
				line += "; " + g.assign(v)
			}
			return &ogStmt{line: line, mutVar: v}
		case 3, 22, 25, 26:
			var muts []*ogVar
			for i, s := range g.scopes {
				for _, v := range s {
					if v.mut && v.st != 2 && !v.lent && i >= g.floor {
						muts = append(muts, v)
					}
				}
			}
			if len(muts) == 0 {
				continue
			}
			v := muts[g.r.IntN(len(muts))]
			for _, m := range muts {
				if m.t == oRes && m.st == 0 && g.r.IntN(2) == 0 {
					v = m // an owned Res: its release is traced
				}
			}
			return &ogStmt{line: g.assign(v)}
		case 4, 5:
			return g.patternLet()
		case 6, 7, 8:
			t := g.anyType()
			g.pos = "lend"
			arg := ""
			from := t
			if t == oCloser && g.r.IntN(2) == 0 {
				from = oRes
			}
			// A conversion moves the local into the interface value the
			// statement owns (R23): only a local this frame may move.
			if v := g.pickVar(from, from != t); v != nil && g.r.IntN(3) != 0 {
				arg = v.name
				g.f("stmt:lend-local")
				if from != t {
					v.st = 1
					g.f("conv:lend")
				}
			} else if v := g.pickOf(true, oRes, oPair, oGuard); v != nil && g.r.IntN(2) == 0 {
				// A lent if whose arm yields an enclosing local, which
				// keeps its owner (R43, the mHold rule).
				t = v.t
				if g.r.IntN(4) == 0 {
					// A lent with whose block yields the local.
					v.st = 1
					g.f("expr:with")
					g.f("with:lend-local")
					g.f("stmt:lend-temp")
					arg = "with context {} { " + g.lentYield(v.name) + " }"
					return &ogStmt{line: fmt.Sprintf("lend%s(%s)", ogTypeKey[t], arg)}
				}
				g.f("expr:if")
				g.f("if:lend-local")
				g.f("stmt:lend-temp")
				c := g.cond()
				before := g.save()
				v.st = 1
				then := g.save()
				g.restore(before)
				b := ""
				if t == oRes && g.r.IntN(2) == 0 {
					// The other arm is a catch that makes the value on
					// success and lends the local when its call fails:
					// the catch's paths decide whether the statement
					// owns the value (FU0).
					g.f("if:lend-local-catch")
					b = fmt.Sprintf("openf(%d, false, log, n, %d) catch { case Bad: %s }", g.id(), g.r.IntN(2), v.name)
				} else {
					b = g.expr(t, 1)
				}
				g.join(then)
				a := g.lentYield(v.name)
				if g.r.IntN(2) == 0 {
					// The lending arm first or second: the second runs
					// at n = 0, which most runs reach.
					a, b = b, a
				}
				arg = fmt.Sprintf("if n > %d { %s } else { %s }", c, a, b)
			} else {
				arg = g.expr(t, 2)
				g.f("stmt:lend-temp")
			}
			return &ogStmt{line: fmt.Sprintf("lend%s(%s)", ogTypeKey[t], arg)}
		case 9:
			t := oRes
			if g.r.IntN(2) == 0 {
				t = oCloser
			}
			v := g.pickVar(t, false)
			if v == nil {
				continue
			}
			g.f("stmt:use")
			return &ogStmt{line: v.name + ".Use()"}
		case 10, 11:
			t := g.anyType()
			g.pos = "take"
			g.f("stmt:take")
			return &ogStmt{line: fmt.Sprintf("keep%s(%s)", ogTypeKey[t], g.expr(t, 2))}
		case 12:
			// Close a local of a type that has one: a resource, an
			// operation type, a record with its own Close.
			v := g.pickOf(true, oRes, oCloser, oGuard)
			if v == nil || g.r.IntN(3) == 0 {
				// A record with its own Close, made and closed here.
				name := g.name("x")
				g.pos = "let"
				e := g.expr(oGuard, 1)
				if g.r.IntN(4) == 0 {
					// Its field taken in place, which its Close reads:
					// refused (R3, R39), so the program is redrawn.
					return &ogStmt{line: fmt.Sprintf("let %s Guard = %s\n%s.R.Close()", name, e, name)}
				}
				g.f("stmt:close")
				g.f("close:guard")
				return &ogStmt{line: fmt.Sprintf("let %s Guard = %s\n%s.Close()", name, e, name)}
			}
			t := v.t
			v.st = 1
			if t == oGuard && g.r.IntN(4) == 0 {
				// A field taken in place from a record with its own Close,
				// which reads the field: refused (R3, R39), so the drawing
				// is redrawn; no feature names it.
				return &ogStmt{line: v.name + ".R.Close()"}
			}
			g.f("stmt:close")
			if t == oGuard {
				g.f("close:guard") // its own Close, then its field (R3)
			}
			return &ogStmt{line: v.name + ".Close()"}
		case 13:
			if d <= 0 {
				continue
			}
			g.f("stmt:if")
			s := &ogStmt{line: fmt.Sprintf("if n > %d", g.cond())}
			before := g.save()
			s.arms = append(s.arms, ogArm{body: g.block(1+g.r.IntN(3), d-1)})
			then := g.save()
			g.restore(before)
			if g.r.IntN(2) == 0 {
				s.arms = append(s.arms, ogArm{head: "} else {", body: g.block(1+g.r.IntN(2), d-1)})
			}
			g.join(then)
			return s
		case 14:
			if d <= 0 {
				continue
			}
			g.f("stmt:for")
			return &ogStmt{line: fmt.Sprintf("for _ in 0..%d", 1+g.r.IntN(3)), arms: []ogArm{{body: g.framed(1+g.r.IntN(3), d-1, true, false, nil)}}}
		case 15:
			if d <= 0 || g.noTry {
				continue
			}
			return g.valuesLoop(d)
		case 16, 23:
			if d <= 0 {
				continue
			}
			return g.match(d)
		case 17:
			if g.noTry {
				continue
			}
			g.f("stmt:fail")
			return &ogStmt{line: fmt.Sprintf("try fail(n, %d)", g.cond())}
		case 18:
			g.f("stmt:boom")
			return &ogStmt{line: fmt.Sprintf("boom(n, %d)", 2+g.r.IntN(9))}
		case 19:
			switch {
			case g.inLoop:
				kw := "break"
				if g.r.IntN(2) == 0 {
					kw = "continue"
				}
				g.f("stmt:" + kw)
				return &ogStmt{line: fmt.Sprintf("if n > %d {\n\t%s\n}", g.cond(), kw)}
			case g.inRun:
				g.f("stmt:return")
				return &ogStmt{line: fmt.Sprintf("if n > %d {\n\treturn n\n}", g.cond())}
			case !g.noTry && g.helpers > 0:
				// A helper's early return: what it moves is moved only on
				// that path.
				before := g.save()
				g.pos = "return"
				e := g.expr(g.ret, 1)
				g.restore(before)
				g.f("stmt:return")
				return &ogStmt{line: fmt.Sprintf("if n > %d {\n\treturn %s\n}", g.cond(), e)}
			}
		case 20, 21:
			if d <= 0 {
				continue
			}
			if g.r.IntN(2) == 0 {
				g.f("capture:twice")
				return &ogStmt{line: "twice()", arms: []ogArm{{body: g.framed(1+g.r.IntN(3), d-1, false, true, nil)}}}
			}
			g.f("capture:let")
			name := g.name("f")
			body := g.framed(1+g.r.IntN(3), d-1, false, true, nil)
			g.pin()
			return &ogStmt{line: fmt.Sprintf("let %s = func()", name), arms: []ogArm{{body: body}}, after: name + "()"}
		}
	}
}

// omit reports that a pattern drawn now leaves a part out (S108 unless
// the checker has a hole).
// assign reassigns the local v. The old value of an owned Res is released
// at the assignment (R41), not at its scope's end: the trace names it
// before, and the judge wants its close before "assigned".
func (g *ogGen) assign(v *ogVar) string {
	g.pos = "assign"
	owned := v.t == oRes && v.st == 0
	e := g.expr(v.t, 2)
	owned = owned && v.st == 0 // e did not move the old value
	v.st = 0
	v.assigned = true
	g.f("stmt:assign")
	if !owned {
		return fmt.Sprintf("%s = %s", v.name, e)
	}
	g.f("assign:release")
	o := g.name("o")
	return fmt.Sprintf("let %s = %s.id; note(log, \"old \\(%s)\"); %s = %s; note(log, \"assigned \\(%s)\")", o, v.name, o, v.name, e, o)
}

func (g *ogGen) omit() bool { return g.r.IntN(10) == 0 }

// patternLet destructures a value by a let.
func (g *ogGen) patternLet() *ogStmt {
	g.pos = "let"
	a, b := g.name("x"), g.name("x")
	switch g.r.IntN(4) {
	case 0:
		e := g.expr(oTup, 2)
		g.f("pat:let:tup")
		pa, pb := a, b
		if g.omit() {
			if g.r.IntN(2) == 0 {
				pa = "_"
			} else {
				pb = "_"
			}
		}
		g.bind(pa, oRes)
		g.bind(pb, oRes)
		return &ogStmt{line: fmt.Sprintf("let (%s, %s) = %s", pa, pb, e)}
	case 1:
		e := g.expr(oPair, 2)
		g.f("pat:let:pair")
		g.declare(a, oRes, false, false)
		pat := fmt.Sprintf("Pair{R: %s, S: %s}", a, b)
		switch {
		case g.omit():
			pat = fmt.Sprintf("Pair{R: %s}", a)
		case g.r.IntN(3) == 0:
			pat = fmt.Sprintf("Pair{S: %s, R: %s}", b, a)
			g.declare(b, oRes, false, false)
		default:
			g.declare(b, oRes, false, false)
		}
		return &ogStmt{line: fmt.Sprintf("let %s = %s", pat, e)}
	case 2:
		e := g.expr(oBox, 2)
		g.f("pat:let:box")
		if g.omit() {
			return &ogStmt{line: fmt.Sprintf("let Box{n: _} = %s", e)}
		}
		g.declare(a, oCloser, false, false)
		return &ogStmt{line: fmt.Sprintf("let Box{C: %s} = %s", a, e)}
	default:
		g.f("pat:let:nested")
		g.pos = "store"
		x := g.expr(oRes, 1)
		y := g.expr(oPair, 1)
		c := g.name("x")
		pat := fmt.Sprintf("(%s, Pair{R: %s, S: %s})", a, b, c)
		if g.omit() {
			pat = fmt.Sprintf("(%s, Pair{R: %s})", a, b)
		} else {
			g.declare(c, oRes, false, false)
		}
		g.declare(a, oRes, false, false)
		g.declare(b, oRes, false, false)
		return &ogStmt{line: fmt.Sprintf("let %s = (%s, %s)", pat, x, y)}
	}
}

func (g *ogGen) bind(name string, t ogType) {
	if name != "_" {
		g.declare(name, t, false, false)
	}
}

// valuesLoop fills a queue it makes, ends it, and iterates its Values():
// each element is the iteration's own (R35, Q7).
func (g *ogGen) valuesLoop(d int) *ogStmt {
	q := g.name("q")
	m := 1 + g.r.IntN(3)
	elt := []ogType{oRes, oTup, oPair}[g.r.IntN(3)]
	var lines []string
	lines = append(lines, fmt.Sprintf("let %s = queue.New[%s](%d)", q, ogTypeName[elt], m))
	for i := 0; i < m; i++ {
		g.pos = "push"
		lines = append(lines, fmt.Sprintf("%s.Push(%s) catch {\ncase queue.Closed:\n}", q, g.expr(elt, 1)))
	}
	lines = append(lines, q+".End()")
	a, b := g.name("e"), g.name("e")
	var pat string
	var binds []string
	switch elt {
	case oRes:
		g.f("loop:values:res")
		pat, binds = a, []string{a}
	case oTup:
		g.f("loop:values:tup")
		pat, binds = fmt.Sprintf("(%s, %s)", a, b), []string{a, b}
		if g.r.IntN(4) == 0 { // an omitted part: refused (R39), drawn again
			pat, binds = fmt.Sprintf("(%s, _)", a), []string{a}
		}
	case oPair:
		g.f("loop:values:pair")
		pat, binds = fmt.Sprintf("Pair{R: %s, S: %s}", a, b), []string{a, b}
		if g.r.IntN(4) == 0 { // an omitted part: refused (R39), drawn again
			pat, binds = fmt.Sprintf("Pair{S: %s}", b), []string{b}
		}
	}
	body := g.framed(1+g.r.IntN(3), d-1, true, false, func() {
		for _, n := range binds {
			g.declare(n, oRes, false, false)
		}
	})
	lines = append(lines, fmt.Sprintf("for %s in %s.Values()", pat, q))
	return &ogStmt{line: strings.Join(lines, "\n"), arms: []ogArm{{body: body}}}
}

// match matches an owned local or a temporary by patterns that bind its
// parts.
func (g *ogGen) match(d int) *ogStmt {
	t := []ogType{oSlot, oPair, oTup}[g.r.IntN(3)]
	var subj, pre string
	local := false
	if v := g.pickOf(true, oSlot, oPair, oTup); v != nil && g.r.IntN(4) != 0 {
		t = v.t
		subj, local = v.name, true
		v.st = 2 // consumed by a binding arm, or not: unused after
		g.f("match:local")
	} else if g.r.IntN(3) == 0 {
		// A lent if whose arm yields an enclosing local, made just
		// before: on that path the match binds views of the local (R43,
		// FU0). The local's arm runs at n = 0, which most runs reach.
		t = []ogType{oPair, oTup}[g.r.IntN(2)]
		g.f("match:lent-if")
		name := g.name("t")
		init := fmt.Sprintf("(open(%d, false, log), open(%d, false, log))", g.id(), g.id())
		if t == oPair {
			init = fmt.Sprintf("Pair{R: open(%d, false, log), S: open(%d, false, log)}", g.id(), g.id())
		}
		g.pos = "match"
		pre = fmt.Sprintf("let %s = %s\n", name, init)
		subj = fmt.Sprintf("if n > %d { %s } else { %s }", g.cond(), g.expr(t, 0), name)
	} else {
		g.pos = "match"
		subj = g.expr(t, 1)
		if t == oSlot {
			// A variant literal's own type is the variant: slotOf gives
			// the union.
			subj = "slotOf(" + subj + ")"
		}
		g.f("match:temp")
	}
	s := &ogStmt{line: pre + "match " + subj}
	before := g.save()
	var states []map[*ogVar]uint8
	arm := func(head string, binds ...string) {
		g.restore(before)
		body := g.framedArm(1+g.r.IntN(2), d-1, binds, !local)
		s.arms = append(s.arms, ogArm{head: head, body: body})
		states = append(states, g.save())
	}
	a, b := g.name("m"), g.name("m")
	switch t {
	case oSlot:
		g.f("match:slot")
		if g.omit() {
			arm("case Full:")
		} else {
			arm(fmt.Sprintf("case Full{R: %s}:", a), a)
		}
		arm("case Vacant:")
	case oPair:
		g.f("match:pair")
		if g.omit() {
			arm(fmt.Sprintf("case Pair{R: %s}:", a), a)
		} else {
			arm(fmt.Sprintf("case Pair{R: %s, S: %s}:", a, b), a, b)
		}
	case oTup:
		g.f("match:tup")
		if g.omit() {
			arm(fmt.Sprintf("case (_, %s):", b), b)
		} else {
			arm(fmt.Sprintf("case (%s, %s):", a, b), a, b)
		}
	}
	g.restore(before)
	for _, st := range states {
		g.join(st)
	}
	return s
}

// framedArm draws a match arm's block with its bindings: owned when the
// subject is an owned local, lent when it is a temporary.
func (g *ogGen) framedArm(n, d int, binds []string, lent bool) []*ogStmt {
	g.scopes = append(g.scopes, nil)
	for _, b := range binds {
		g.declare(b, oRes, false, lent)
	}
	out := g.stmts(n, d)
	out = append(out, g.flush()...)
	g.scopes = g.scopes[:len(g.scopes)-1]
	return out
}

// genOg draws the program of one seed and sub-seed.
func genOg(seed, sub int) *ogProg {
	r := rand.New(rand.NewPCG(uint64(seed), 0x6f67+uint64(sub)))
	p := &ogProg{divIDs: map[int]bool{}, features: map[string]bool{}}
	g := &ogGen{r: r, p: p, scopes: [][]*ogVar{nil}, inRun: true}
	p.body = g.stmts(4+r.IntN(6), 3)
	p.body = append(p.body, g.flush()...)
	return p
}

// text is the generated statements alone.
func (p *ogProg) text() string {
	var b strings.Builder
	for _, h := range p.helpers {
		ogWrite(&b, h.body, 1)
		b.WriteString(h.ret + "\n")
	}
	ogWrite(&b, p.body, 1)
	return b.String()
}

// source writes the program.
func (p *ogProg) source() string {
	var b strings.Builder
	if strings.Contains(p.text(), "queue.") {
		b.WriteString("import queue\n")
	}
	if strings.Contains(p.text(), "strconv.") {
		b.WriteString("import go strconv\n")
	}
	files := strings.Contains(p.text(), "os.Open")
	if files {
		b.WriteString("import go os\n")
	}
	b.WriteString(ogPrelude)
	needs, fd0, leak := "", "", ""
	if files {
		b.WriteString(ogFilePrelude)
		needs = " needs go.Foreign"
		fd0 = "\t\tlet fd0 = fdCount()\n"
		leak = "\t\tlet leaked = fdCount() - fd0\n\t\tif leaked != 0 {\n\t\t\tnote(log, \"leak \\(leaked)\")\n\t\t}\n"
	}
	for _, h := range p.helpers {
		b.WriteString(h.sig + " {\n")
		ogWrite(&b, h.body, 1)
		if h.tail {
			fmt.Fprintf(&b, "\t%s\n}\n\n", h.ret)
		} else {
			fmt.Fprintf(&b, "\treturn %s\n}\n\n", h.ret)
		}
	}
	b.WriteString("func run(n int, log sync.Ref[string]) int fails Bad" + needs + " {\n")
	ogWrite(&b, p.body, 1)
	b.WriteString("\tn\n}\n\n")
	// Each n of 0..9 takes every branch `n > c` (c in 0..8) one way or
	// the other, so each production a program reaches also runs; each
	// run's trace is judged alone (ogSegment). With Go files, each run
	// also counts the process's open files before and after it: a Go
	// file never closed is a "leak" line (ogJudgeRun). The first open
	// comes first, as Go's poller keeps files of its own from it on.
	warm := ""
	if files {
		needs = " | go.Foreign"
		warm = "\tlendFile(" + ogDevNull + ")\n"
	}
	b.WriteString(`func main() needs Console` + needs + ` {
` + warm + `	for k in 0..10 {
` + fd0 + `		let log = sync.NewRef[string]("")
		let set = tasks.NewSet[Bad](tasks.LogFailures)`)
	b.WriteString(`
		tasks.Go(set, func() {
			let v = run(k, log) catch {
			case Bad:
				note(log, "failure")
				-1
			}
			note(log, "value \(v)")
		}) catch {
		case tasks.Closed:
		}
		set.Wait() catch {
		case Bad:
		}
` + leak + `		Console.Println(log.Get() + "` + ogSegment + `")
	}
}
`)
	return b.String()
}

func ogWrite(b *strings.Builder, list []*ogStmt, depth int) {
	ind := strings.Repeat("\t", depth)
	line := func(s string, ind string) {
		for _, l := range strings.Split(s, "\n") {
			b.WriteString(ind + l + "\n")
		}
	}
	for _, s := range list {
		text := s.line
		if s.mutVar != nil {
			mut := ""
			if s.mutVar.assigned {
				mut = "mut "
			}
			text = strings.Replace(text, "\x00", mut, 1)
		}
		if s.arms == nil {
			line(text, ind)
			continue
		}
		line(text+" {", ind)
		for _, a := range s.arms {
			if a.head != "" {
				line(a.head, ind)
			}
			ogWrite(b, a.body, depth+1)
		}
		line("}", ind)
		if s.after != "" {
			line(s.after, ind)
		}
	}
}

// count is how many statements p holds; without is p with statement k
// (in a walk of the helpers' bodies, then run's) removed.
func (p *ogProg) count() int {
	n := 0
	var walk func([]*ogStmt)
	walk = func(l []*ogStmt) {
		for _, s := range l {
			n++
			for _, a := range s.arms {
				walk(a.body)
			}
		}
	}
	for _, h := range p.helpers {
		walk(h.body)
	}
	walk(p.body)
	return n
}

func (p *ogProg) without(k int) *ogProg {
	i := 0
	var copyList func([]*ogStmt) []*ogStmt
	copyList = func(l []*ogStmt) []*ogStmt {
		var out []*ogStmt
		for _, s := range l {
			if i == k {
				i++
				for _, a := range s.arms {
					i += (&ogProg{body: a.body}).count()
				}
				continue
			}
			i++
			c := &ogStmt{line: s.line, after: s.after, mutVar: s.mutVar}
			for _, a := range s.arms {
				c.arms = append(c.arms, ogArm{head: a.head, body: copyList(a.body)})
			}
			out = append(out, c)
		}
		return out
	}
	q := &ogProg{divIDs: p.divIDs, features: p.features}
	for _, h := range p.helpers {
		q.helpers = append(q.helpers, &ogFunc{sig: h.sig, ret: h.ret, body: copyList(h.body)})
	}
	q.body = copyList(p.body)
	return q
}

type ogCase struct {
	seed, sub    int
	prog         *ogProg
	src          string
	static, list *lowered
}

type ogVerdict struct {
	fault, static, list string
}

// ogLower checks and lowers p in both release forms; nil when the checker
// refuses it (its diagnostics are returned). A lowering error or an
// internal compiler error on an admitted program fails the test.
func ogLower(t *testing.T, name string, seed int, p *ogProg) (*ogCase, []string) {
	src := p.source()
	static := lowerSource(name+"_static", src, true)
	if len(static.diags) > 0 || static.err != nil {
		var ds []string
		for _, d := range static.diags {
			ds = append(ds, d.Code+" "+d.Message)
		}
		if static.err != nil {
			ds = append(ds, static.err.Error())
		}
		return nil, ds
	}
	if static.panicked != nil || len(static.refused) > 0 {
		t.Errorf("seed %d (ERGO_SEED=%d replays): an admitted program does not lower: %v %+v\n%s", seed, seed, static.panicked, static.refused, numbered(src))
		return nil, nil
	}
	prev := SetReleaseLoweringForTest("scope")
	list := lowerSource(name+"_list", src, true)
	SetReleaseLoweringForTest(prev)
	if list.err != nil || len(list.diags) > 0 || list.panicked != nil || len(list.refused) > 0 {
		t.Errorf("seed %d (ERGO_SEED=%d replays): the list form does not lower: %v %v %v %+v\n%s", seed, seed, list.err, list.diags, list.panicked, list.refused, numbered(src))
		return nil, nil
	}
	return &ogCase{seed: seed, prog: p, src: src, static: static, list: list}, nil
}

// ogAdmit draws sub-seeds of seed until the checker admits one.
func ogAdmit(t *testing.T, seed int) *ogCase {
	var first []string
	for sub := 0; sub < 64; sub++ {
		p := genOg(seed, sub)
		c, refusal := ogLower(t, fmt.Sprintf("og_%d", seed), seed, p)
		if c != nil {
			c.sub = sub
			return c
		}
		if refusal == nil {
			return nil // reported
		}
		if first == nil {
			first = refusal
		}
		if os.Getenv("ERGO_OWNGEN_REFUSALS") != "" {
			t.Logf("seed %d sub %d refused: %s", seed, sub, strings.Join(refusal, "; "))
		}
	}
	t.Errorf("seed %d: the checker refused 64 drawn programs; the first: %s", seed, strings.Join(first, "; "))
	return nil
}

// ogRun builds and runs each case in both forms and judges each. It
// builds Go because the release code on the failure, defect and loop
// edges must run; go/types cannot run it (TestOwnershipGrammarRun).
func ogRun(t *testing.T, cases []*ogCase) []ogVerdict {
	var programs []*lowered
	for _, c := range cases {
		programs = append(programs, c.static, c.list)
	}
	exes := buildAll(t, programs, true)
	out := make([]ogVerdict, len(cases))
	if exes == nil {
		return out
	}
	run := func(name string) string {
		cmd := exec.Command(exes[name])
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := runProgram(t, cmd)
		got := stdout.String()
		if err != nil {
			got += "exit " + strconv.Itoa(cmd.ProcessState.ExitCode()) + "\n"
		}
		return got
	}
	for i, c := range cases {
		v := ogVerdict{static: run(c.static.name), list: run(c.list.name)}
		switch {
		case v.static != v.list:
			v.fault = "the static and the list release lowerings differ"
		default:
			v.fault = ogJudge(v.static, c.prog.divIDs)
		}
		out[i] = v
	}
	return out
}

// ogJudge checks a trace against the ownership promise: each value
// opened is closed exactly once and never used after its close. Ids are
// static (a loop opens one id again), so it counts instances: a close
// needs an open instance, a use needs a live one, and none is left. A
// division literal has no open event and is made at most once.
func ogJudge(out string, div map[int]bool) string {
	var faults []string
	for i, run := range strings.Split(out, ogSegment) {
		if f := ogJudgeRun(run, div); f != "" {
			faults = append(faults, fmt.Sprintf("n=%d: %s", i, f))
		}
	}
	return strings.Join(faults, "; ")
}

// ogSegment ends the trace of one run(n) in the program's output.
const ogSegment = "--- end of run"

// ogJudgeRun judges the trace of one run(n).
func ogJudgeRun(out string, div map[int]bool) string {
	live := map[int]int{}
	closes := map[int]int{}
	closedAt := map[int]int{} // closes of an id when its local was reassigned
	var faults []string
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) != 2 {
			continue
		}
		id, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		switch f[0] {
		case "old":
			closedAt[id] = closes[id]
		case "assigned":
			if closes[id] <= closedAt[id] {
				faults = append(faults, fmt.Sprintf("%d is not released at its reassignment", id))
			}
		case "leak":
			faults = append(faults, fmt.Sprintf("%d Go files are never closed", id))
		case "open":
			live[id]++
		case "use":
			if div[id] && closes[id] > 0 || !div[id] && live[id] <= 0 {
				faults = append(faults, fmt.Sprintf("%d is used after its close", id))
			}
		case "close":
			closes[id]++
			if div[id] {
				if closes[id] > 1 {
					faults = append(faults, fmt.Sprintf("%d is closed twice", id))
				}
				continue
			}
			live[id]--
			if live[id] < 0 {
				faults = append(faults, fmt.Sprintf("%d is closed more often than opened", id))
			}
		}
	}
	var ids []int
	for id, n := range live {
		if n > 0 {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	for _, id := range ids {
		faults = append(faults, fmt.Sprintf("%d is never closed", id))
	}
	return strings.Join(faults, "; ")
}

// ogShrink removes statements from a failing case while it still fails,
// one round of candidates per build, and returns the smallest it found.
func ogShrink(t *testing.T, c *ogCase, v ogVerdict) (*ogCase, ogVerdict) {
	for round := 0; round < 16; round++ {
		var cands []*ogCase
		for k := 0; k < c.prog.count(); k++ {
			if d, _ := ogLower(t, fmt.Sprintf("ogs_%d_%d", round, k), c.seed, c.prog.without(k)); d != nil {
				cands = append(cands, d)
			}
		}
		if len(cands) == 0 {
			break
		}
		vs := ogRun(t, cands)
		next := -1
		for i := range cands {
			if vs[i].fault != "" {
				next = i
				break
			}
		}
		if next < 0 {
			break
		}
		cands[next].sub = c.sub
		c, v = cands[next], vs[next]
	}
	return c, v
}
