package sketchtest

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
)

// RelNeeds generates the programs of the release-needs property (resources
// R5, check/releaseneeds.go): a package of functions whose bodies acquire
// resources whose Close requires the capabilities A, B, both or none,
// directly or through a part (a record field, a slice element, an
// Option's variant, a queue's values left, a written Close's parts), and
// release them at every kind of release point (a scope's end, an early
// return, a statement's end for a temporary, a handoff into a function
// that takes the value), under with blocks that provide A, B or both, in
// functions whose capability row is inferred (unexported, no row written),
// written, or empty (exported, no row written). A small effects model
// (Model) computes each inferred row and every line where a written row
// misses a capability; the check property holds the checker to it, and
// the run property runs a sample of the programs that check, which must
// reach no nil service.
type RelNeeds struct {
	Funcs []*RelFunc
	// Main calls functions under a with that provides one capability or both, or
	// under none: a program whose main misses a need does not check.
	Main     []RelMainCall
	src      string
	line     map[*RelStmt]int
	mainLine []int
}

// The capabilities of the model, a bit each.
const (
	RelA = 1 << iota
	RelB
)

// relKinds are the resource shapes: the type, what its release needs, a
// value of it (%d is an id), what making that value needs (qA's written
// row: its own queue's release), and the part of the Drop graph
// (check/drop.go) through which the release reaches a Close that needs a
// capability: the type's own Close, a record field, a slice element, a
// union's variant, a queue's values left (Drain). The part "own" (a
// written Close's parts, Drop.Own) is reached by the W<k> of a conversion.
var relKinds = []struct {
	name    string
	needs   int
	mk      string
	mkNeeds int
	part    string
}{
	{"R0", 0, "R0{id: %d}", 0, "close"},
	{"RA", RelA, "RA{id: %d}", 0, "close"},
	{"RB", RelB, "RB{id: %d}", 0, "close"},
	{"RAB", RelA | RelB, "RAB{id: %d}", 0, "close"},
	{"Box", RelB, "Box{r: RB{id: %d}, n: 1}", 0, "fields"},
	{"[]RA", RelA, "[]RA{RA{id: %d}}", 0, "elem"},
	{"Option[RB]", RelB, "optB(%d)", 0, "variants"},
	{"queue.Queue[RA]", RelA, "qA(%d)", RelA, "drain"},
}

// RelParts are the parts of a Drop graph (check/drop.go) a release walks.
var RelParts = []string{"close", "fields", "elem", "variants", "drain", "own"}

// RelPrelude declares the capabilities, their providers, the resources,
// and for each kind k a lending lend<k> and a taking keep<k>.
var RelPrelude = func() string {
	var b strings.Builder
	b.WriteString(`import codec
import queue

type A { Use() }
type B { Use() }
type Pa {} implements A
func Use(_ Pa) {}
type Pb {} implements B
func Use(_ Pb) {}
type R0 { id int } implements Closer
func Close(r R0) {}
type RA { id int } implements Closer
func Close(r RA) needs A { A.Use() }
type RB { id int } implements Closer
func Close(r RB) needs B { B.Use() }
type RAB { id int } implements Closer
func Close(r RAB) needs A | B {
	A.Use()
	B.Use()
}
type Box { r RB, n int }
func optB(id int) Option[RB] { Some{RB{id: id}} }
func qA(id int) queue.Queue[RA] needs A {
	let q = queue.New[RA](2)
	q.Push(RA{id: id}) catch {
	case queue.Closed:
		return q
	}
	q
}
type Bad { Text string }
type Lv { name string }
func ParseLv(s string) Lv fails Bad needs A {
	A.Use()
	Lv{name: s}
}
func Wire(l Lv) string needs B {
	B.Use()
	l.name
}
type lw { name string }
func Wire(l lw) string needs B {
	B.Use()
	l.name
}
func lendC(c Closer) {}
`)
	for k, kd := range relKinds {
		fmt.Fprintf(&b, "func lend%d(r %s) {}\nfunc keep%d(r %s) takes r {}\n", k, kd.name, k, kd.name)
		fmt.Fprintf(&b, "type W%d { inner %s } implements Closer\nfunc Close(w W%d) {}\n", k, kd.name, k)
	}
	return b.String()
}()

// RelStmt kinds.
const (
	RelLet     = iota // let vV = MK
	RelTemp           // lendK(MK): a temporary of the statement
	RelKeep           // keepK(MK): the callee owns and releases it
	RelLendVar        // lendK(vV)
	RelKeepVar        // keepK(vV): vV is handed off
	RelReturn         // if n > 2 { return }
	RelCall           // if n > 0 { fJ(n - 1) }
	RelWith           // with context { … } { Body }
	RelIf             // if n > 1 { Body }
	RelEncode         // encode(X{name: "x"}, ev): Wire needs B (X: K%2, Lv or lw)
	RelDecode         // let _ = decode[X](…) catch { … }: ParseX needs A
	RelConvert        // let cV Closer = WK{inner: MK}; lendC(cV): S108 when K's release needs a capability
)

type RelStmt struct {
	Kind, K, V, Callee, ID int
	Provides               int // RelWith: the capabilities its context binds
	Body                   []*RelStmt
}

type RelFunc struct {
	Exported bool
	// Written is the written row, -1 for none (inferred when unexported,
	// empty when exported).
	Written int
	Body    []*RelStmt
}

type RelMainCall struct {
	Provides, Callee int
}

// NewRelNeeds generates one program from r.
func NewRelNeeds(r *rand.Rand) *RelNeeds {
	g := &RelNeeds{}
	n := 1 + r.IntN(4)
	id := 0
	for range n {
		f := &RelFunc{Exported: r.IntN(3) == 0, Written: -1}
		if r.IntN(3) == 0 {
			f.Written = r.IntN(4)
		}
		g.Funcs = append(g.Funcs, f)
	}
	var vars []int // owned, in scope
	nextV := 0
	var block func(depth, size int) []*RelStmt
	block = func(depth, size int) []*RelStmt {
		var out []*RelStmt
		mark := len(vars)
		for range size {
			s := &RelStmt{Kind: r.IntN(12), K: r.IntN(len(relKinds))}
			id++
			s.ID = id
			switch s.Kind {
			case RelLet:
				s.V = nextV
				nextV++
				vars = append(vars, s.V<<8|s.K)
			case RelLendVar, RelKeepVar:
				if len(vars) == 0 {
					s.Kind = RelTemp
					break
				}
				i := r.IntN(len(vars))
				s.V, s.K = vars[i]>>8, vars[i]&0xff
				if s.Kind == RelKeepVar {
					vars = slices.Delete(vars, i, i+1)
					if i < mark {
						mark--
					}
				}
			case RelCall:
				s.Callee = r.IntN(n)
			case RelDecode, RelConvert:
				s.V = nextV
				nextV++
			case RelWith, RelIf:
				if depth >= 2 {
					s.Kind = RelCall
					s.Callee = r.IntN(n)
					break
				}
				s.Provides = 1 + r.IntN(3)
				s.Body = block(depth+1, 1+r.IntN(3))
			}
			out = append(out, s)
		}
		if mark < len(vars) {
			vars = vars[:mark]
		}
		return out
	}
	for _, f := range g.Funcs {
		vars, nextV = vars[:0], 0
		f.Body = block(0, 1+r.IntN(5))
	}
	for range 1 + r.IntN(3) {
		g.Main = append(g.Main, RelMainCall{Provides: r.IntN(4), Callee: r.IntN(n)})
	}
	g.print()
	return g
}

// codecType is the codec type of statement kind k: Lv, whose ParseLv
// writes needs A; lw, whose parseLw infers it. Each Wire needs B.
func codecType(k int) string {
	if k%2 == 0 {
		return "Lv"
	}
	return "lw"
}

// FuncName is the name of function i.
func (g *RelNeeds) FuncName(i int) string {
	if g.Funcs[i].Exported {
		return fmt.Sprintf("F%d", i)
	}
	return fmt.Sprintf("f%d", i)
}

func rowText(row int) string {
	var caps []string
	if row&RelA != 0 {
		caps = append(caps, "A")
	}
	if row&RelB != 0 {
		caps = append(caps, "B")
	}
	return strings.Join(caps, " | ")
}

func providers(p int) string {
	var es []string
	if p&RelA != 0 {
		es = append(es, "A: Pa")
	}
	if p&RelB != 0 {
		es = append(es, "B: Pb")
	}
	return strings.Join(es, ", ")
}

// Source is the program's Ergo source.
func (g *RelNeeds) Source() string { return g.src }

func (g *RelNeeds) print() {
	var b strings.Builder
	b.WriteString(RelPrelude)
	line := strings.Count(RelPrelude, "\n") + 1
	g.line = map[*RelStmt]int{}
	emit := func(depth int, format string, args ...any) {
		b.WriteString(strings.Repeat("\t", depth))
		fmt.Fprintf(&b, format, args...)
		b.WriteString("\n")
		line++
	}
	var stmts func(depth int, ss []*RelStmt)
	stmts = func(depth int, ss []*RelStmt) {
		for _, s := range ss {
			g.line[s] = line
			mk := fmt.Sprintf(relKinds[s.K].mk, s.ID)
			switch s.Kind {
			case RelLet:
				emit(depth, "let v%d = %s", s.V, mk)
			case RelTemp:
				emit(depth, "lend%d(%s)", s.K, mk)
			case RelKeep:
				emit(depth, "keep%d(%s)", s.K, mk)
			case RelLendVar:
				emit(depth, "lend%d(v%d)", s.K, s.V)
			case RelKeepVar:
				emit(depth, "keep%d(v%d)", s.K, s.V)
			case RelReturn:
				emit(depth, "if n > 2 {")
				emit(depth+1, "return")
				emit(depth, "}")
			case RelCall:
				emit(depth, "if n > 0 { %s(n - 1) }", g.FuncName(s.Callee))
			case RelWith:
				emit(depth, "with context { %s } {", providers(s.Provides))
				stmts(depth+1, s.Body)
				emit(depth, "}")
			case RelIf:
				emit(depth, "if n > 1 {")
				stmts(depth+1, s.Body)
				emit(depth, "}")
			case RelEncode:
				emit(depth, "encode(%s{name: \"x\"}, ev)", codecType(s.K))
			case RelDecode:
				emit(depth, "let _ = decode[%s](codec.NewReplay(ev.Recorded())) catch {", codecType(s.K))
				emit(depth, "case _:")
				emit(depth+1, "%s{name: \"\"}", codecType(s.K))
				emit(depth, "}")
			case RelConvert:
				emit(depth, "let c%d Closer = W%d{inner: %s}", s.V, s.K, mk)
				emit(depth, "lendC(c%d)", s.V)
			}
		}
	}
	for i, f := range g.Funcs {
		row := ""
		if f.Written >= 0 {
			row = " needs " + rowText(f.Written)
			if f.Written == 0 {
				row = ""
			}
		}
		emit(0, "")
		emit(0, "func %s(n int)%s {", g.FuncName(i), row)
		emit(1, "let ev = codec.NewEvents()")
		emit(1, "let _ = ev.Recorded()")
		stmts(1, f.Body)
		emit(0, "}")
	}
	emit(0, "")
	emit(0, "func main() {")
	g.mainLine = g.mainLine[:0]
	for _, m := range g.Main {
		if m.Provides == 0 {
			g.mainLine = append(g.mainLine, line)
			emit(1, "%s(3)", g.FuncName(m.Callee))
			continue
		}
		emit(1, "with context { %s } {", providers(m.Provides))
		g.mainLine = append(g.mainLine, line)
		emit(2, "%s(3)", g.FuncName(m.Callee))
		emit(1, "}")
	}
	emit(0, "}")
	// Last, so no declaration order checks it before the bodies that
	// decode lw: only the edge a decode adds orders it first.
	b.WriteString("\nfunc parseLw(s string) lw fails Bad {\n\tA.Use()\n\tlw{name: s}\n}\n")
	g.src = b.String()
}

// written reports whether f's row is written (or empty for an exported
// function), and that row. A written empty row prints no clause, so an
// unexported function with Written 0 is inferred.
func (f *RelFunc) written() (bool, int) {
	switch {
	case f.Written > 0:
		return true, f.Written
	case f.Exported:
		return true, 0
	}
	return false, 0
}

// Model is the effects model: each function's row ("A | B", "" for none),
// in order, and each "line cap" where a written row misses a need (S102),
// sorted. A charge is what a call of the release's Close there would
// charge: the needs of the acquired kind at a let, a temporary or a
// handoff into keep<k> (whose taken parameter charges its row), and the
// needs of making the kind's value (qA's row) wherever it is made; the
// callee's row at a call; B (Wire's) at an encode and A (the ParseX's,
// written or inferred) at a decode; nothing at a lend of a local or a
// return. A with removes what it provides. refused is each "line S108" of
// a conversion to Closer whose release needs a capability, which an
// adapter could not carry (G7), sorted.
func (g *RelNeeds) Model() (rows, missing, refused []string) {
	row := make([]int, len(g.Funcs))
	for i, f := range g.Funcs {
		if ok, w := f.written(); ok {
			row[i] = w
		}
	}
	var charges func(ss []*RelStmt, provided int, at func(*RelStmt, int))
	charges = func(ss []*RelStmt, provided int, at func(*RelStmt, int)) {
		for _, s := range ss {
			need := 0
			switch s.Kind {
			case RelLet, RelTemp, RelKeep:
				need = relKinds[s.K].needs | relKinds[s.K].mkNeeds
			case RelKeepVar:
				need = relKinds[s.K].needs
			case RelConvert:
				need = relKinds[s.K].mkNeeds
			case RelEncode:
				need = RelB
			case RelDecode:
				need = RelA
			case RelCall:
				need = row[s.Callee]
			case RelWith:
				charges(s.Body, provided|s.Provides, at)
			case RelIf:
				charges(s.Body, provided, at)
			}
			if need &^= provided; need != 0 {
				at(s, need)
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for i, f := range g.Funcs {
			if ok, _ := f.written(); ok {
				continue
			}
			charges(f.Body, 0, func(_ *RelStmt, need int) {
				if row[i]|need != row[i] {
					row[i] |= need
					changed = true
				}
			})
		}
	}
	seen := map[string]bool{}
	for i, f := range g.Funcs {
		rows = append(rows, rowText(row[i]))
		ok, w := f.written()
		if !ok {
			continue
		}
		charges(f.Body, 0, func(s *RelStmt, need int) {
			for _, c := range []int{RelA, RelB} {
				if need&c != 0 && w&c == 0 {
					k := fmt.Sprintf("%d %s", g.line[s], rowText(c))
					if !seen[k] {
						seen[k] = true
						missing = append(missing, k)
					}
				}
			}
		})
	}
	// main writes no row: each call outside a with that provides the
	// callee's row misses it.
	for i, m := range g.Main {
		for _, c := range []int{RelA, RelB} {
			if row[m.Callee]&c != 0 && m.Provides&c == 0 {
				missing = append(missing, fmt.Sprintf("%d %s", g.mainLine[i], rowText(c)))
			}
		}
	}
	slices.Sort(missing)
	missing = slices.Compact(missing)
	var convert func(ss []*RelStmt)
	convert = func(ss []*RelStmt) {
		for _, s := range ss {
			if s.Kind == RelConvert && relKinds[s.K].needs != 0 {
				refused = append(refused, fmt.Sprintf("%d S108", g.line[s]))
			}
			convert(s.Body)
		}
	}
	for _, f := range g.Funcs {
		convert(f.Body)
	}
	slices.Sort(refused)
	return rows, missing, refused
}

// Parts adds to reached each part of a Drop graph (RelParts) through which
// a release the program charges, or a conversion it refuses, reaches a
// Close that needs a capability: the property asserts that its seeds
// reach every part, so no part's walk goes unchecked.
func (g *RelNeeds) Parts(reached map[string]bool) {
	var walk func(ss []*RelStmt)
	walk = func(ss []*RelStmt) {
		for _, s := range ss {
			switch s.Kind {
			case RelLet, RelTemp, RelKeep, RelKeepVar:
				if relKinds[s.K].needs != 0 {
					reached[relKinds[s.K].part] = true
				}
			case RelConvert:
				if relKinds[s.K].needs != 0 {
					reached["own"] = true
					reached[relKinds[s.K].part] = true
				}
			}
			walk(s.Body)
		}
	}
	for _, f := range g.Funcs {
		walk(f.Body)
	}
}

// Releases reports whether the program releases a resource whose Close
// needs a capability: the run property needs such programs.
func (g *RelNeeds) Releases() bool {
	var rel func(ss []*RelStmt) bool
	rel = func(ss []*RelStmt) bool {
		for _, s := range ss {
			switch s.Kind {
			case RelLet, RelTemp, RelKeep:
				if relKinds[s.K].needs != 0 {
					return true
				}
			case RelWith, RelIf:
				if rel(s.Body) {
					return true
				}
			}
		}
		return false
	}
	for _, f := range g.Funcs {
		if rel(f.Body) {
			return true
		}
	}
	return false
}
