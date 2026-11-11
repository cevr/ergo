package regexpplan

import (
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
)

// Typed results and linear find (patterns.md, Lowering 3-5).
//
// A plan whose captures each take part at most once per match (no capture
// under a repeat whose maximum is above one) is a fixed plan. Its parse
// writes each capture's open and close position into fixed slots of one
// result array, [3+2k]int: ok (1 or 0), start (or the failure's At), end,
// then the open and close position of capture t at 1+2t and 2+2t (-1 when
// the capture took no part). The array is returned by value, so a parse of
// a fixed plan allocates nothing. A plan with a repeated capture returns the
// same three header values followed by its ordered events (tag, position),
// in a slice: every occurrence is kept.
//
// Find runs a Pike VM over the canonical NFA: threads are kept in priority
// order, a new start is added behind every live thread at each position, and
// the scan never restarts. The work per byte is bounded by the instruction
// count (TestFindIsLinear counts it).

// Fixed reports a plan whose every capture takes part at most once.
func (p *Plan) Fixed() bool { return !p.repeated }

// Slots is the length of a fixed plan's result array.
func (p *Plan) Slots() int { return 3 + 2*len(p.names) }

// Deterministic reports a plan whose deterministic candidate passed the
// construction check, so its direct and table forms can be emitted (the
// selection picks one; a test may force the other, lower/pattern.go).
func (p *Plan) Deterministic() bool { return p.ir != nil }

// Mode is the match mode the plan was compiled for.
func (p *Plan) Mode() Mode { return p.mode }

// repeatedCapture reports a capture under a repeat whose maximum is above one.
func repeatedCapture(n *Node, under bool) bool {
	if n.Kind == Capture && under {
		return true
	}
	if n.Kind == Repeat && (n.Max == -1 || n.Max > 1) {
		under = true
	}
	for _, c := range n.Children {
		if repeatedCapture(c, under) {
			return true
		}
	}
	return false
}

// Result is a parse or find outcome in the result-array layout above:
// Header holds ok, start (or At) and end; Events the ordered events.
type Result struct {
	OK         bool
	Start, End int
	At         int
	Events     []Event
}

// ParseResult is the in-process parse of the selected engine with the
// failure position At: the longest prefix of s that is a prefix of a word.
func (p *Plan) ParseResult(s string) Result {
	if p.mode != Parse {
		panic("ParseResult of a find plan")
	}
	if p.info.Engine == NFA {
		return p.nfaParse(s)
	}
	q := 0
	var events []Event
	for pos := 0; ; pos++ {
		b := 256
		if pos < len(s) {
			b = int(s[pos])
		}
		e := p.ir.states[q].edges[b]
		if pos == len(s) {
			if !e.accept {
				return Result{At: pos}
			}
			for _, tag := range e.tags {
				events = append(events, Event{tag, pos})
			}
			return Result{OK: true, End: pos, Events: events}
		}
		if e.to < 0 {
			return Result{At: pos}
		}
		for _, tag := range e.tags {
			events = append(events, Event{tag, pos})
		}
		q = e.to
	}
}

// nfaParse is the canonical NFA parse with At.
func (p *Plan) nfaParse(s string) Result {
	current := []thread{{p.start, nil}}
	at := 0
	for pos := 0; ; pos++ {
		ready := p.closure(current, pos)
		if len(ready) > 0 {
			at = pos
		}
		var next []thread
		for _, t := range ready {
			i := p.code[t.pc]
			if i.op == acceptOp && pos == len(s) {
				return Result{OK: true, End: pos, Events: materialize(t.h)}
			}
			if i.op == byteOp && pos < len(s) && i.set[s[pos]/64]&(1<<(s[pos]%64)) != 0 {
				next = append(next, thread{i.x, t.h})
			}
		}
		if pos == len(s) || len(next) == 0 {
			return Result{At: at}
		}
		current = next
	}
}

type pikeThread struct {
	pc, start int
	h         *history
}

// FindFrom is the in-process linear find: the leftmost-first match that
// starts at or after from, and the work it did (closure visits and byte
// tests).
func (p *Plan) FindFrom(s string, from int) (Match, int) {
	work := 0
	if from < 0 || from > len(s) {
		return Match{}, work
	}
	var current []pikeThread
	var best Match
	seen := make([]int, len(p.code))
	for pos := from; ; pos++ {
		if !best.OK {
			current = append(current, pikeThread{p.start, pos, nil})
		}
		var ready []pikeThread
		var visit func(pikeThread)
		visit = func(t pikeThread) {
			work++
			if seen[t.pc] == pos+1 {
				return
			}
			seen[t.pc] = pos + 1
			i := p.code[t.pc]
			switch i.op {
			case splitOp:
				visit(pikeThread{i.x, t.start, t.h})
				visit(pikeThread{i.y, t.start, t.h})
			case jumpOp:
				visit(pikeThread{i.x, t.start, t.h})
			case tagOp:
				visit(pikeThread{i.x, t.start, &history{Event{i.tag, pos}, t.h}})
			default:
				ready = append(ready, t)
			}
		}
		for _, t := range current {
			visit(t)
		}
		var next []pikeThread
		for _, t := range ready {
			work++
			i := p.code[t.pc]
			if i.op == acceptOp {
				best = Match{t.start, pos, materialize(t.h), true}
				break // lower-priority threads cannot win
			}
			if pos < len(s) && i.set[s[pos]/64]&(1<<(s[pos]%64)) != 0 {
				next = append(next, pikeThread{i.x, t.start, t.h})
			}
		}
		if pos == len(s) || (best.OK && len(next) == 0) {
			return best, work
		}
		current = next
	}
}

// Fold writes events into a fixed plan's slots: open of capture t at
// 1+2t, close at 2+2t.
func (p *Plan) Fold(events []Event) []int {
	slots := make([]int, p.Slots())
	for i := 3; i < len(slots); i++ {
		slots[i] = -1
	}
	for _, e := range events {
		if e.Tag > 0 {
			slots[1+2*e.Tag] = e.Pos
		} else {
			slots[2-2*e.Tag] = e.Pos
		}
	}
	return slots
}

// Emitted drivers. NAME is the Go function's name, N the result length.
// Each template is checked by its text after its data literals are checked
// against the plan, as ValidateGo checks RC3's drivers.

const slotFold = `
  if tag > 0 {r[1+2*tag]=pos} else {r[2-2*tag]=pos}`

const eventFold = `
  r = append(r, tag, pos)`

// The deterministic parse: step is RC3's validated direct or table step,
// held as a closure so several patterns share one Go package.
const deterministicParse = `
func NAME(s string) RESULT {
 step := STEP
 INIT
 q := 0
 for pos := 0; ; pos++ {
  b := 256
  if pos < len(s) {b = int(s[pos])}
  to, tags, accept := step(q, b)
  if pos == len(s) {
   if !accept {return FAIL{pos, 0}}
   for j := 0; j < len(tags); j += 2 {
    tag := int(int16(uint16(tags[j]) | uint16(tags[j+1])<<8))FOLD
   }
   r[0], r[2] = 1, pos
   return r
  }
  if to < 0 {return FAIL{pos, 0}}
  for j := 0; j < len(tags); j += 2 {
   tag := int(int16(uint16(tags[j]) | uint16(tags[j+1])<<8))FOLD
  }
  q = to
 }
}`

const nfaProgram = `
 type instruction struct {op int;x int;y int;tag int;set [4]uint64}
 code := [...]instruction{PROGRAM}
 type history struct {tag int;pos int;prev *history}
 type thread struct {pc int;start int;h *history}`

// The canonical NFA parse, with At.
const nfaParseDriver = `
func NAME(s string) RESULT {` + nfaProgram + `
 INIT
 current := []thread{{INITIAL, 0, nil}}
 at := 0
 for pos := 0; ; pos++ {
  seen := make([]bool, len(code))
  var ready []thread
  var visit func(thread)
  visit = func(t thread) {
   if seen[t.pc] {return}
   seen[t.pc] = true
   i := code[t.pc]
   switch i.op {
   case 2: visit(thread{i.x, t.start, t.h}); visit(thread{i.y, t.start, t.h})
   case 4: visit(thread{i.x, t.start, t.h})
   case 3: visit(thread{i.x, t.start, &history{i.tag, pos, t.h}})
   default: ready = append(ready, t)
   }
  }
  for _, t := range current {visit(t)}
  if len(ready) > 0 {at = pos}
  var next []thread
  for _, t := range ready {
   i := code[t.pc]
   if i.op == 0 && pos == len(s) {
    var h []*history
    for x := t.h; x != nil; x = x.prev {h = append(h, x)}
    for j := len(h) - 1; j >= 0; j-- {
     tag, pos := h[j].tag, h[j].posFOLD
    }
    r[0], r[2] = 1, pos
    return r
   }
   if i.op == 1 && pos < len(s) && i.set[s[pos]/64]&(1<<(s[pos]%64)) != 0 {next = append(next, thread{i.x, t.start, t.h})}
  }
  if pos == len(s) || len(next) == 0 {return FAIL{at, 0}}
  current = next
 }
}`

// The linear find (Pike VM), the same algorithm as FindFrom.
const pikeFindDriver = `
func NAME(s string, from int) RESULT {` + nfaProgram + `
 INIT
 if from < 0 || from > len(s) {return FAIL{0, 0}}
 var current []thread
 seen := make([]int, len(code))
 found := false
 var best *history
 for pos := from; ; pos++ {
  if !found {current = append(current, thread{INITIAL, pos, nil})}
  var ready []thread
  var visit func(thread)
  visit = func(t thread) {
   if seen[t.pc] == pos+1 {return}
   seen[t.pc] = pos + 1
   i := code[t.pc]
   switch i.op {
   case 2: visit(thread{i.x, t.start, t.h}); visit(thread{i.y, t.start, t.h})
   case 4: visit(thread{i.x, t.start, t.h})
   case 3: visit(thread{i.x, t.start, &history{i.tag, pos, t.h}})
   default: ready = append(ready, t)
   }
  }
  for _, t := range current {visit(t)}
  var next []thread
  for _, t := range ready {
   i := code[t.pc]
   if i.op == 0 {found = true; r[0], r[1], r[2] = 1, t.start, pos; best = t.h; break}
   if pos < len(s) && i.set[s[pos]/64]&(1<<(s[pos]%64)) != 0 {next = append(next, thread{i.x, t.start, t.h})}
  }
  if pos == len(s) || (found && len(next) == 0) {break}
  current = next
 }
 if !found {return FAIL{0, 0}}
 {
  var h []*history
  for x := best; x != nil; x = x.prev {h = append(h, x)}
  for j := len(h) - 1; j >= 0; j-- {
   tag, pos := h[j].tag, h[j].posFOLD
  }
 }
 return r
}`

// shape is the result type, its initialisation and its fold.
func (p *Plan) shape() (result, init, fold string) {
	if p.Fixed() {
		n := p.Slots()
		return fmt.Sprintf("[%d]int", n), fmt.Sprintf("var r [%d]int\n for i := 3; i < %d; i++ {r[i] = -1}", n, n), slotFold
	}
	return "[]int", fmt.Sprintf("r := make([]int, 3, 3+%d)", 4*len(p.names)), eventFold
}

func (p *Plan) program() string {
	var b strings.Builder
	for _, i := range p.code {
		fmt.Fprintf(&b, "{%d,%d,%d,%d,[4]uint64{%d,%d,%d,%d}},", i.op, i.x, i.y, i.tag, i.set[0], i.set[1], i.set[2], i.set[3])
	}
	return b.String()
}

func (p *Plan) fill(template, name, step, program string) string {
	result, init, fold := p.shape()
	// Some templates fold where tag and pos are locals of a loop body.
	return strings.NewReplacer("NAME", name, "FAIL{", result+"{0, ", "RESULT", result, "INITIAL", strconv.Itoa(p.start), "INIT", init, "FOLD", fold,
		"STEP", step, "PROGRAM", program).Replace(template)
}

// EmitParse is the Go declaration of the parse driver named name for the
// engine: the selected one, NFA for the reference lowering, or a forced
// direct or table form of a deterministic plan (a test's choice). Its step
// (direct or table) is RC3's emission, checked by ValidateGo before use.
func (p *Plan) EmitParse(name string, engine Engine) (string, error) {
	if p.mode != Parse {
		return "", fmt.Errorf("EmitParse of a find plan")
	}
	if engine != NFA && !p.Deterministic() {
		return "", fmt.Errorf("engine %s needs a deterministic candidate, and this plan has none", engine)
	}
	if engine == NFA {
		src := p.fill(nfaParseDriver, name, "", p.program())
		return src, p.validateDriver(src, nfaParseDriver, name)
	}
	rc3, err := p.emit(engine)
	if err != nil {
		return "", err
	}
	if err := p.ValidateGo(rc3); err != nil {
		return "", err
	}
	f, err := parser.ParseFile(token.NewFileSet(), "pattern.go", rc3, 0)
	if err != nil {
		return "", err
	}
	stepDecl := f.Decls[0].(*ast.FuncDecl)
	step := "func" + strings.TrimPrefix(astText(stepDecl.Type), "func") + " " + astText(stepDecl.Body)
	src := p.fill(deterministicParse, name, step, "")
	// The step closure is RC3's validated text; the rest is the template.
	if err := p.validateTemplate(src, deterministicParse, name, step); err != nil {
		return "", err
	}
	return canonicalDecl(src), nil
}

// EmitFind is the Go declaration of the linear find driver named name.
func (p *Plan) EmitFind(name string) (string, error) {
	src := p.fill(pikeFindDriver, name, "", p.program())
	return src, p.validateDriver(src, pikeFindDriver, name)
}

// validateDriver checks an NFA-program driver: its instruction literal is
// the plan's canonical code and the rest is the reviewed template.
func (p *Plan) validateDriver(src, template, name string) error {
	f, err := parser.ParseFile(token.NewFileSet(), "pattern.go", "package pattern\n"+src, 0)
	if err != nil || len(f.Decls) != 1 {
		return fmt.Errorf("invalid driver: %v", err)
	}
	var program *ast.CompositeLit
	ast.Inspect(f, func(n ast.Node) bool {
		a, ok := n.(*ast.AssignStmt)
		if ok && len(a.Lhs) == 1 && astText(a.Lhs[0]) == "code" && len(a.Rhs) == 1 {
			program, _ = a.Rhs[0].(*ast.CompositeLit)
		}
		return true
	})
	if program == nil || len(program.Elts) != len(p.code) {
		return fmt.Errorf("invalid driver program")
	}
	for pc, expr := range program.Elts {
		if !sameInstruction(expr, p.code[pc]) {
			return fmt.Errorf("driver instruction mismatch at %d", pc)
		}
	}
	program.Elts = nil
	if astText(f.Decls[0]) != canonicalDecl(p.fill(template, name, "", "")) {
		return fmt.Errorf("driver control-flow mismatch")
	}
	return nil
}

func sameInstruction(expr ast.Expr, i inst) bool {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok || len(lit.Elts) != 5 {
		return false
	}
	for j, v := range []int{int(i.op), i.x, i.y, i.tag} {
		got, err := intExpr(lit.Elts[j])
		if err != nil || got != v {
			return false
		}
	}
	set, ok := lit.Elts[4].(*ast.CompositeLit)
	if !ok || len(set.Elts) != 4 {
		return false
	}
	for j, v := range i.set {
		got, err := strconv.ParseUint(astText(set.Elts[j]), 10, 64)
		if err != nil || got != v {
			return false
		}
	}
	return true
}

// validateTemplate checks a deterministic driver: with its step closure
// replaced by a marker, it is the reviewed template.
func (p *Plan) validateTemplate(src, template, name, step string) error {
	want := canonicalDecl(p.fill(template, name, "nil", ""))
	got := canonicalDecl(strings.Replace(src, step, "nil", 1))
	if want == "" || got != want {
		return fmt.Errorf("driver control-flow mismatch")
	}
	return nil
}

var identity = sync.OnceValue(func() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown-build"
	}
	id := info.GoVersion + "|" + info.Main.Path + "@" + info.Main.Version + "|" + info.Main.Sum
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" || s.Key == "vcs.modified" || s.Key == "-ldflags" {
			id += "|" + s.Key + "=" + s.Value
		}
	}
	return id
})

// BuildIdentity names the compiler build: a matcher cached under another
// build's identity is never reused (Lowering 5).
func BuildIdentity() string { return identity() }

// CacheKey is the plan's semantic key joined with the compiler's build
// identity: the key of any cache of built matchers.
func (p *Plan) CacheKey() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(p.key+"\x00"+BuildIdentity())))
}
