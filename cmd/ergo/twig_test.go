package main

import (
	"bytes"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/proclimit"
)

// TestTwigAgreesWithControl is the correctness property of examples/twig
// (a lexer, a Pratt parser, a resolver and a tree-walking interpreter):
// on seeded generated programs, the Ergo program and the hand Go control
// (testdata/twig/control) print exactly what a reference model computes
// (twigReference: the generated tree walked with a map per call, no
// resolver and no slots), stdout, stderr with the -stats counters, and
// exit status. The programs use closures that outlive their call,
// functions a block binds before their text, loops with break and
// continue, shared lists and records, every operator on ints at the
// extremes, floats, strings and mixed kinds (which fail at a position),
// and some carry one resolve error (a name not declared, a name declared
// twice, a jump out of place). Each program, with a few bytes changed,
// is then checked by both, which must print the same: the lexer's,
// parser's and resolver's failures agree. The workload of `twig gen` at
// two sizes runs on both. A failing seed prints the program and its
// replay command.
//
// It builds Go because the programs under test are the linked binaries:
// the Ergo program reads its source through the host's files and ends
// with os.Exit, and the control is Go; no in-process check runs either.
// Each run is bounded by proclimit, and the test waits for its exit.
//
// It kills the example's mutants that no law sees (examples/twig/
// testdata/mutants.json).
func TestTwigAgreesWithControl(t *testing.T) {
	needsBuild(t)
	ergoBin, work := buildTwig(t)
	control := buildTwigControl(t, work)
	dir := t.TempDir()
	path := filepath.Join(dir, "p.tw")
	seeds := 200
	first := uint64(1)
	if s := os.Getenv("TWIG_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("TWIG_SEED=%q is not a seed", s)
		}
		first, seeds = n, 1
	}
	replay := func(seed uint64) string {
		return fmt.Sprintf("replay: TWIG_SEED=%d go test -count=1 -run '^TestTwigAgreesWithControl$' ./cmd/ergo", seed)
	}
	for seed := first; seed < first+uint64(seeds); seed++ {
		src, want := twigProgram(seed)
		writeFile(t, path, src)
		for _, bin := range []string{ergoBin, control} {
			if got := runTwig(t, bin, "run", "-stats", path); got != want {
				t.Fatalf("seed %d: %s printed\n%s\nthe reference printed\n%s\nthe program:\n%s\n%s",
					seed, filepath.Base(bin), clip(got), clip(want), clip(src), replay(seed))
			}
		}
		broken := twigBreak(rand.New(rand.NewPCG(seed, 0xb40c)), src)
		writeFile(t, path, broken)
		a, b := runTwig(t, ergoBin, "check", path), runTwig(t, control, "check", path)
		if a != b {
			t.Fatalf("seed %d, with bytes changed: twig check printed\n%s\nthe control printed\n%s\nthe program:\n%s\n%s",
				seed, clip(a), clip(b), clip(broken), replay(seed))
		}
	}
	for _, n := range []string{"2", "30"} {
		gen := exec.Command(ergoBin, "gen", "-seed", strconv.FormatUint(first, 10), "-n", n)
		var out bytes.Buffer
		gen.Stdout = &out
		if err := proclimit.Run(gen, 1<<30); err != nil {
			t.Fatalf("twig gen: %v", err)
		}
		writeFile(t, path, out.String())
		a, b := runTwig(t, ergoBin, "run", "-stats", path), runTwig(t, control, "run", "-stats", path)
		if a != b || !strings.HasSuffix(a, "status 0\n") {
			t.Fatalf("the workload of seed %d, %s rounds: twig printed\n%s\nthe control printed\n%s", first, n, clip(a), clip(b))
		}
	}
}

// buildTwig links examples/twig with `ergo build` and returns the binary
// and the emitted module's directory.
func buildTwig(t *testing.T) (bin, work string) {
	t.Helper()
	program := filepath.Join(smokeWorkspace(t, "examples/twig"), "examples", "twig", "cmd", "twig")
	bin = filepath.Join(t.TempDir(), "twig")
	command := exec.Command(buildTestCLI(t), "build", "-work", "-o", bin, program)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := proclimit.Run(command, 4<<30); err != nil {
		t.Fatalf("ergo build: %v\n%s%s", err, stdout.Bytes(), stderr.Bytes())
	}
	return bin, workDir(t, stderr.String())
}

// buildTwigControl links testdata/twig/control in a module with the go
// line of the example's emitted module in work, so one toolchain compiles
// both.
func buildTwigControl(t *testing.T, work string) string {
	t.Helper()
	mod, err := os.ReadFile(filepath.Join(work, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	goLine := regexp.MustCompile(`(?m)^go [0-9.]+$`).Find(mod)
	if goLine == nil {
		t.Fatalf("the emitted go.mod has no go line:\n%s", mod)
	}
	src, err := os.ReadFile(filepath.Join("testdata", "twig", "control", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module control\n\n"+string(goLine)+"\n")
	writeFile(t, filepath.Join(dir, "main.go"), string(src))
	bin := filepath.Join(dir, "control")
	command := exec.Command("go", "build", "-o", bin, ".")
	command.Dir = dir
	var out bytes.Buffer
	command.Stdout, command.Stderr = &out, &out
	if err := proclimit.Run(command, 4<<30); err != nil {
		t.Fatalf("go build of the control: %v\n%s", err, out.Bytes())
	}
	return bin
}

// runTwig runs bin with args and is its stdout, stderr and exit status as
// one text.
func runTwig(t *testing.T, bin string, args ...string) string {
	t.Helper()
	command := exec.Command(bin, args...)
	command.Dir = filepath.Dir(args[len(args)-1])
	command.Args[len(command.Args)-1] = filepath.Base(args[len(args)-1])
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := proclimit.Run(command, 1<<30)
	code := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("%s: %v", bin, err)
		}
		code = exit.ExitCode()
	}
	return twigOutcome(stdout.String(), stderr.String(), code)
}

func twigOutcome(stdout, stderr string, code int) string {
	return fmt.Sprintf("stdout:\n%s\nstderr:\n%s\nstatus %d\n", stdout, stderr, code)
}

// twigBreak changes one to three bytes of src: a byte deleted, a byte
// put in, or a byte replaced, each from the bytes the grammar cares about.
func twigBreak(r *rand.Rand, src string) string {
	b := []byte(src)
	marks := []byte("{}()[]=,.:\n-+*/<\"\\ab1_ ")
	for range 1 + r.IntN(3) {
		at := r.IntN(len(b) + 1)
		switch r.IntN(3) {
		case 0:
			if at < len(b) {
				b = append(b[:at], b[at+1:]...)
			}
		case 1:
			b = append(b[:at], append([]byte{marks[r.IntN(len(marks))]}, b[at:]...)...)
		default:
			if at < len(b) {
				b[at] = marks[r.IntN(len(marks))]
			}
		}
	}
	return string(b)
}

// ---- the generated tree ----

type twigPos struct{ line, col int }

type twigEK uint8

const (
	twInt twigEK = iota
	twFloat
	twStr
	twBool
	twNil
	twigName
	twUnary
	twBinary
	twCall
	twIndex
	twField
	twList
	twRec
	twFn
)

// twigExpr is an expression of a generated program. Its at is set when
// the program is printed: where Ergo's parser puts it (an operator's
// token, a call's (, an index's [, a field's .).
type twigExpr struct {
	kind   twigEK
	at     twigPos
	n      int64
	f      float64
	s      string // a string's value, a name, a field, an operator
	x, y   *twigExpr
	args   []*twigExpr
	keys   []string
	params []string
	body   []*twigStmt
}

type twigSK uint8

const (
	tsFn twigSK = iota
	tsLet
	tsAssign
	tsIf
	tsWhile
	tsFor
	tsReturn
	tsBreak
	tsContinue
	tsExpr
)

type twigStmt struct {
	kind      twigSK
	at        twigPos
	name      string
	params    []string
	x, y      *twigExpr // a value, a test, a list, a target and its value
	body, els []*twigStmt
}

// ---- the generator ----

type twigTy uint8

const (
	tyInt twigTy = iota
	tyFloat
	tyStr
	tyBool
	tyList
	tyRec
	tyFn
	tyNil
)

// twigVar is a name the generator may use, with what it means to hold: a
// function's signature, a record's fields. A function may be called only
// where the calls stay acyclic (callable).
type twigVar struct {
	name   string
	ty     twigTy
	keys   []string
	keyTys []twigTy
	minLen int
	params []twigTy
	ret    twigTy
	retSig *twigVar
	owner  int  // the function whose body declares it
	done   bool // its body is generated
	maker  *twigVar
	fixed  bool
}

type twigCtx struct {
	id    int
	loops int
	ret   twigTy
	sig   *twigVar
}

type twigGen struct {
	r      *rand.Rand
	next   int
	scopes [][]*twigVar
	ctxs   []*twigCtx
	fnIDs  int
	// inject is the one resolve error the program carries, once placed.
	inject   bool
	injected *twigFailure
	injectAt func() twigPos
}

type twigFailure struct {
	at  twigPos
	msg string
}

func (g *twigGen) fresh(prefix string) string {
	g.next++
	return prefix + strconv.Itoa(g.next)
}

func (g *twigGen) ctx() *twigCtx { return g.ctxs[len(g.ctxs)-1] }

func (g *twigGen) declare(v *twigVar) {
	g.scopes[len(g.scopes)-1] = append(g.scopes[len(g.scopes)-1], v)
}

// visible is the names in scope that match keep.
func (g *twigGen) visible(keep func(*twigVar) bool) []*twigVar {
	var out []*twigVar
	for _, s := range g.scopes {
		for _, v := range s {
			if keep(v) {
				out = append(out, v)
			}
		}
	}
	return out
}

// callable reports whether the current body may call v: a function whose
// body is done, or one a block of this body binds (its body ends before
// this one), so every call goes to a function that ends earlier and no
// call returns to its caller's function.
func (g *twigGen) callable(v *twigVar) bool {
	if v.maker != nil {
		return g.callable(v.maker)
	}
	return v.ty == tyFn && (v.done || v.owner == g.ctx().id)
}

func (g *twigGen) chance(n int) bool { return g.r.IntN(n) == 0 }

var twigValueTys = []twigTy{tyInt, tyInt, tyFloat, tyStr, tyBool, tyList, tyRec, tyNil}

func (g *twigGen) anyTy() twigTy { return twigValueTys[g.r.IntN(len(twigValueTys))] }

var twigInts = []int64{0, 1, 2, 3, 7, 10, 255, math.MaxInt64}

var twigStrs = []string{"", "a", "ab", "q\"\\", "line\n\ttab", "é", "zz"}

var twigKeys = []string{"x", "y", "k", "next"}

func twLit(k twigEK) *twigExpr { return &twigExpr{kind: k} }

func twRef(n string) *twigExpr { return &twigExpr{kind: twigName, s: n} }

func twBin(op string, l, r *twigExpr) *twigExpr { return &twigExpr{kind: twBinary, s: op, x: l, y: r} }

func twCallOf(fn *twigExpr, args ...*twigExpr) *twigExpr {
	return &twigExpr{kind: twCall, x: fn, args: args}
}

// expr is an expression meant to have type t, which a few times in a
// hundred is of another type, so that the operators fail.
func (g *twigGen) expr(t twigTy, d int) *twigExpr {
	if g.chance(150) {
		t = g.anyTy()
	}
	if g.inject && g.chance(60) {
		g.inject = false
		e := twRef("u0")
		g.injectAt = func() twigPos { return e.at }
		g.injected = &twigFailure{msg: "u0 is not declared"}
		return e
	}
	vars := g.visible(func(v *twigVar) bool { return v.ty == t && v.ty != tyFn })
	if len(vars) > 0 && g.chance(3) {
		return twRef(vars[g.r.IntN(len(vars))].name)
	}
	if fns := g.visible(func(v *twigVar) bool { return g.callable(v) && v.ret == t && v.ret != tyFn }); len(fns) > 0 && d > 0 && g.chance(4) {
		return g.callOf(fns[g.r.IntN(len(fns))], d)
	}
	leaf := d <= 0
	switch t {
	case tyInt:
		if !leaf {
			switch g.r.IntN(8) {
			case 0, 1, 2:
				ops := []string{"+", "-", "*", "/", "%"}
				return twBin(ops[g.r.IntN(len(ops))], g.expr(tyInt, d-1), g.expr(tyInt, d-1))
			case 3:
				return &twigExpr{kind: twUnary, s: "-", x: g.expr(tyInt, d-1)}
			case 4:
				if g.chance(2) {
					return twCallOf(twRef("len"), g.expr(tyStr, d-1))
				}
				return twCallOf(twRef("len"), g.expr(tyList, d-1))
			case 5:
				return twCallOf(twRef("int"), g.expr(tyFloat, d-1))
			case 6:
				if e := g.fieldOf(tyInt); e != nil {
					return e
				}
			case 7:
				if lists := g.visible(func(v *twigVar) bool { return v.ty == tyList && v.minLen > 0 }); len(lists) > 0 {
					l := lists[g.r.IntN(len(lists))]
					i := int64(g.r.IntN(l.minLen))
					if g.chance(10) {
						i = int64(l.minLen + 3)
					}
					return &twigExpr{kind: twIndex, x: twRef(l.name), y: &twigExpr{kind: twInt, n: i}}
				}
			}
		}
		n := twigInts[g.r.IntN(len(twigInts))]
		if n < math.MaxInt64-2 {
			n += int64(g.r.IntN(3))
		}
		return &twigExpr{kind: twInt, n: n}
	case tyFloat:
		if !leaf {
			switch g.r.IntN(6) {
			case 0, 1:
				ops := []string{"+", "-", "*", "/"}
				return twBin(ops[g.r.IntN(len(ops))], g.expr(tyFloat, d-1), g.expr(tyFloat, d-1))
			case 2:
				return &twigExpr{kind: twUnary, s: "-", x: g.expr(tyFloat, d-1)}
			case 3:
				return twCallOf(twRef("float"), g.expr(tyInt, d-1))
			case 4:
				return twCallOf(twRef("sqrt"), g.expr(tyFloat, d-1))
			case 5:
				if e := g.fieldOf(tyFloat); e != nil {
					return e
				}
			}
		}
		fs := []float64{0, 0.5, 1, 2.25, 1e10, 0.1, 1e-7}
		f := fs[g.r.IntN(len(fs))]
		if g.chance(25) {
			f = 3.0e300
		}
		return &twigExpr{kind: twFloat, f: f}
	case tyStr:
		if !leaf {
			switch g.r.IntN(5) {
			case 0, 1:
				return twBin("+", g.expr(tyStr, d-1), g.expr(tyStr, d-1))
			case 2:
				return twCallOf(twRef("str"), g.expr(g.anyTy(), d-1))
			case 3:
				i := int64(0)
				if g.chance(10) {
					i = 1
				}
				return &twigExpr{kind: twIndex, x: g.expr(tyStr, d-1), y: &twigExpr{kind: twInt, n: i}}
			case 4:
				if e := g.fieldOf(tyStr); e != nil {
					return e
				}
			}
		}
		return &twigExpr{kind: twStr, s: twigStrs[g.r.IntN(len(twigStrs))]}
	case tyBool:
		if !leaf {
			switch g.r.IntN(5) {
			case 0, 1:
				ops := []string{"<", "<=", ">", ">="}
				k := []twigTy{tyInt, tyFloat, tyStr}[g.r.IntN(3)]
				l := g.expr(k, d-1)
				if g.chance(3) {
					// a side and its copy: the operators on equal operands
					return twBin(ops[g.r.IntN(len(ops))], l, twigCopy(l))
				}
				return twBin(ops[g.r.IntN(len(ops))], l, g.expr(k, d-1))
			case 2:
				ops := []string{"==", "!="}
				return twBin(ops[g.r.IntN(2)], g.expr(g.anyTy(), d-1), g.expr(g.anyTy(), d-1))
			case 3:
				ops := []string{"and", "or"}
				return twBin(ops[g.r.IntN(2)], g.expr(tyBool, d-1), g.expr(tyBool, d-1))
			case 4:
				return &twigExpr{kind: twUnary, s: "not", x: g.expr(tyBool, d-1)}
			}
		}
		return &twigExpr{kind: twBool, n: int64(g.r.IntN(2))}
	case tyList:
		e := &twigExpr{kind: twList}
		for range g.r.IntN(4) {
			e.args = append(e.args, g.expr(tyInt, d-1))
		}
		return e
	case tyRec:
		e := &twigExpr{kind: twRec}
		for _, k := range twigKeys {
			if g.chance(2) {
				e.keys = append(e.keys, k)
				e.args = append(e.args, g.expr([]twigTy{tyInt, tyFloat, tyStr}[g.r.IntN(3)], d-1))
			}
		}
		return e
	case tyFn:
		return g.fnLit(g.signature(false), d)
	}
	return twLit(twNil)
}

// twigCopy is a deep copy of e, so that the printer gives each copy its
// own positions.
func twigCopy(e *twigExpr) *twigExpr {
	if e == nil {
		return nil
	}
	c := *e
	c.x, c.y = twigCopy(e.x), twigCopy(e.y)
	c.args = make([]*twigExpr, len(e.args))
	for i, a := range e.args {
		c.args[i] = twigCopy(a)
	}
	c.body = make([]*twigStmt, len(e.body))
	for i, s := range e.body {
		c.body[i] = twigCopyStmt(s)
	}
	return &c
}

func twigCopyStmt(s *twigStmt) *twigStmt {
	c := *s
	c.x, c.y = twigCopy(s.x), twigCopy(s.y)
	c.body = make([]*twigStmt, len(s.body))
	for i, b := range s.body {
		c.body[i] = twigCopyStmt(b)
	}
	c.els = make([]*twigStmt, len(s.els))
	for i, b := range s.els {
		c.els[i] = twigCopyStmt(b)
	}
	return &c
}

// fieldOf is a read of a field of type t of a record in scope, or nil.
func (g *twigGen) fieldOf(t twigTy) *twigExpr {
	type pick struct {
		v   *twigVar
		key string
	}
	var picks []pick
	for _, v := range g.visible(func(v *twigVar) bool { return v.ty == tyRec }) {
		for i, k := range v.keys {
			if v.keyTys[i] == t {
				picks = append(picks, pick{v, k})
			}
		}
	}
	if len(picks) == 0 {
		return nil
	}
	p := picks[g.r.IntN(len(picks))]
	key := p.key
	if g.chance(15) {
		key = "nope"
	}
	return &twigExpr{kind: twField, x: twRef(p.v.name), s: key}
}

func (g *twigGen) callOf(f *twigVar, d int) *twigExpr {
	c := twCallOf(twRef(f.name))
	n := len(f.params)
	if g.chance(30) {
		n += 1 - 2*g.r.IntN(2)
	}
	for i := range max(n, 0) {
		t := tyInt
		if i < len(f.params) {
			t = f.params[i]
		}
		c.args = append(c.args, g.expr(t, d-1))
	}
	return c
}

// signature is a function's parameters and result; a maker returns a
// function.
func (g *twigGen) signature(maker bool) *twigVar {
	s := &twigVar{ty: tyFn}
	for range g.r.IntN(3) {
		s.params = append(s.params, []twigTy{tyInt, tyInt, tyFloat, tyStr, tyList}[g.r.IntN(5)])
	}
	s.ret = []twigTy{tyInt, tyInt, tyFloat, tyStr, tyBool, tyList, tyNil}[g.r.IntN(7)]
	if maker {
		s.ret = tyFn
		s.retSig = g.signature(false)
	}
	return s
}

// function generates the body of a function of signature sig: the
// parameters in scope, statements, and a return of its result.
func (g *twigGen) function(sig *twigVar, d int) ([]string, []*twigStmt) {
	g.fnIDs++
	g.ctxs = append(g.ctxs, &twigCtx{id: g.fnIDs, ret: sig.ret, sig: sig})
	g.scopes = append(g.scopes, nil)
	var params []string
	for _, t := range sig.params {
		p := &twigVar{name: g.fresh("p"), ty: t, minLen: 0}
		g.declare(p)
		params = append(params, p.name)
	}
	body := g.block(d-1, 1+g.r.IntN(4), nil)
	var ret *twigExpr
	if sig.ret == tyFn {
		ret = g.fnLit(sig.retSig, d-1)
	} else {
		ret = g.expr(sig.ret, d-1)
	}
	body = append(body, &twigStmt{kind: tsReturn, x: ret})
	g.scopes = g.scopes[:len(g.scopes)-1]
	g.ctxs = g.ctxs[:len(g.ctxs)-1]
	return params, body
}

func (g *twigGen) fnLit(sig *twigVar, d int) *twigExpr {
	params, body := g.function(sig, d)
	return &twigExpr{kind: twFn, params: params, body: body}
}

// block generates n statements in a new scope, after the statements
// first, with the functions it binds placed among them.
func (g *twigGen) block(d, n int, first []*twigStmt) []*twigStmt {
	g.scopes = append(g.scopes, nil)
	defer func() { g.scopes = g.scopes[:len(g.scopes)-1] }()
	var fns []*twigVar
	if d > 0 {
		for range g.r.IntN(3) / 2 * (1 + g.r.IntN(2)) {
			f := g.signature(g.chance(3))
			f.name, f.owner = g.fresh("f"), g.ctx().id
			g.declare(f)
			fns = append(fns, f)
		}
	}
	out := first
	slots := make([]*twigVar, n+len(fns))
	for _, f := range fns {
		for {
			i := g.r.IntN(len(slots))
			if slots[i] == nil {
				slots[i] = f
				break
			}
		}
	}
	for _, f := range slots {
		if f == nil {
			out = append(out, g.stmt(d))
			continue
		}
		params, body := g.function(f, d)
		f.done = true
		out = append(out, &twigStmt{kind: tsFn, name: f.name, params: params, body: body})
	}
	return out
}

func (g *twigGen) stmt(d int) *twigStmt {
	ctx := g.ctx()
	if g.inject && g.chance(20) {
		g.inject = false
		s := &twigStmt{}
		g.injectAt = func() twigPos { return s.at }
		switch {
		case ctx.loops == 0 && g.chance(2):
			s.kind = tsBreak
			g.injected = &twigFailure{msg: "break is outside a loop"}
		case ctx.loops == 0:
			s.kind = tsContinue
			g.injected = &twigFailure{msg: "continue is outside a loop"}
		case len(g.ctxs) == 1:
			s.kind, s.x = tsReturn, twLit(twNil)
			g.injected = &twigFailure{msg: "return is outside a function"}
		default:
			mine := g.scopes[len(g.scopes)-1]
			if len(mine) == 0 {
				g.inject, g.injectAt = true, nil
				return g.stmt(d)
			}
			v := mine[g.r.IntN(len(mine))]
			s.kind, s.name, s.x = tsLet, v.name, twLit(twNil)
			g.injected = &twigFailure{msg: v.name + " is already declared in this block"}
		}
		return s
	}
	assignable := g.visible(func(v *twigVar) bool { return !v.fixed && v.ty != tyFn })
	switch g.r.IntN(14) {
	case 0, 1, 2:
		t := g.anyTy()
		if g.chance(5) {
			t = tyFn
		}
		v := &twigVar{name: g.fresh("v"), ty: t}
		var val *twigExpr
		if t == tyFn {
			makers := g.visible(func(m *twigVar) bool { return g.callable(m) && m.ret == tyFn })
			if len(makers) > 0 && g.chance(2) {
				m := makers[g.r.IntN(len(makers))]
				val = g.callOf(m, d)
				*v = twigVar{name: v.name, ty: tyFn, params: m.retSig.params, ret: m.retSig.ret, maker: m}
			} else {
				sig := g.signature(false)
				val = g.fnLit(sig, d)
				*v = twigVar{name: v.name, ty: tyFn, params: sig.params, ret: sig.ret, done: true}
			}
			v.fixed = true
		} else {
			val = g.expr(t, d)
			switch val.kind {
			case twList:
				v.minLen = len(val.args)
			case twRec:
				v.keys = val.keys
				for _, a := range val.args {
					v.keyTys = append(v.keyTys, map[twigEK]twigTy{twInt: tyInt, twFloat: tyFloat, twStr: tyStr}[a.kind])
				}
			}
			if (t == tyList || t == tyRec) && val.kind != twList && val.kind != twRec {
				v.ty = tyNil // a list or record of unknown shape: read, never indexed
			}
		}
		s := &twigStmt{kind: tsLet, name: v.name, x: val}
		g.declare(v)
		return s
	case 3:
		if len(assignable) > 0 {
			v := assignable[g.r.IntN(len(assignable))]
			if v.ty == tyRec || v.ty == tyList {
				return &twigStmt{kind: tsExpr, x: twCallOf(twRef("print"), twRef(v.name))}
			}
			return &twigStmt{kind: tsAssign, x: twRef(v.name), y: g.expr(v.ty, d)}
		}
	case 4:
		if lists := g.visible(func(v *twigVar) bool { return v.ty == tyList }); len(lists) > 0 {
			l := lists[g.r.IntN(len(lists))]
			if g.chance(2) && l.minLen > 0 {
				return &twigStmt{kind: tsAssign, x: &twigExpr{kind: twIndex, x: twRef(l.name), y: &twigExpr{kind: twInt, n: int64(g.r.IntN(l.minLen))}}, y: g.expr(tyInt, d)}
			}
			return &twigStmt{kind: tsExpr, x: twCallOf(twRef("push"), twRef(l.name), g.expr(tyInt, d))}
		}
	case 5:
		if recs := g.visible(func(v *twigVar) bool { return v.ty == tyRec && len(v.keys) > 0 }); len(recs) > 0 {
			r := recs[g.r.IntN(len(recs))]
			i := g.r.IntN(len(r.keys))
			return &twigStmt{kind: tsAssign, x: &twigExpr{kind: twField, x: twRef(r.name), s: r.keys[i]}, y: g.expr(r.keyTys[i], d)}
		}
	case 6:
		if d > 0 {
			return g.ifStmt(d)
		}
	case 7:
		if d > 0 {
			// while: a counter the body moves first, so continue cannot
			// skip it, and that nothing else assigns.
			c := &twigVar{name: g.fresh("c"), ty: tyInt, fixed: true}
			g.scopes = append(g.scopes, []*twigVar{c})
			ctx.loops++
			step := &twigStmt{kind: tsAssign, x: twRef(c.name), y: twBin("+", twRef(c.name), &twigExpr{kind: twInt, n: 1})}
			body := g.block(d-1, 1+g.r.IntN(3), []*twigStmt{step})
			ctx.loops--
			g.scopes = g.scopes[:len(g.scopes)-1]
			loop := &twigStmt{kind: tsWhile, x: twBin("<", twRef(c.name), &twigExpr{kind: twInt, n: int64(g.r.IntN(5))}), body: body}
			return &twigStmt{kind: tsIf, x: &twigExpr{kind: twBool, n: 1}, body: []*twigStmt{{kind: tsLet, name: c.name, x: &twigExpr{kind: twInt}}, loop}}
		}
	case 8:
		if d > 0 {
			// for: over a fresh list, which nothing else holds, so the
			// body cannot make it longer.
			over := &twigExpr{kind: twList}
			for range g.r.IntN(4) {
				over.args = append(over.args, g.expr(tyInt, d-1))
			}
			g.scopes = append(g.scopes, nil)
			v := &twigVar{name: g.fresh("e"), ty: tyInt, fixed: true}
			g.declare(v)
			ctx.loops++
			body := g.block(d-1, 1+g.r.IntN(3), nil)
			ctx.loops--
			g.scopes = g.scopes[:len(g.scopes)-1]
			return &twigStmt{kind: tsFor, name: v.name, x: over, body: body}
		}
	case 9:
		if ctx.loops > 0 && g.chance(2) {
			return &twigStmt{kind: []twigSK{tsBreak, tsContinue}[g.r.IntN(2)]}
		}
	case 10:
		if len(g.ctxs) > 1 && g.chance(2) {
			if ctx.ret == tyFn {
				return &twigStmt{kind: tsReturn, x: g.fnLit(ctx.sig.retSig, d)}
			}
			if g.chance(5) {
				return &twigStmt{kind: tsReturn, x: nil}
			}
			return &twigStmt{kind: tsReturn, x: g.expr(ctx.ret, d)}
		}
	case 11, 12:
		if fns := g.visible(g.callable); len(fns) > 0 {
			return &twigStmt{kind: tsExpr, x: g.callOf(fns[g.r.IntN(len(fns))], d)}
		}
	case 13:
		if g.chance(2) {
			return g.lateRead()
		}
	}
	return &twigStmt{kind: tsExpr, x: twCallOf(twRef("print"), g.expr(g.anyTy(), d))}
}

// lateRead is a block where a function reads a variable of its caller
// before the caller's let of it: a call of a function g that fills its
// frame with ints, then a call of h, which calls the function its block
// binds before its let of the name that function reads. The call of h
// may take g's frame for reuse; the name reads nil, never g's value.
func (g *twigGen) lateRead() *twigStmt {
	f, h, i := g.fresh("g"), g.fresh("h"), g.fresh("i")
	a, b, c, k := g.fresh("p"), g.fresh("p"), g.fresh("v"), g.fresh("v")
	p, r, w := g.fresh("p"), g.fresh("v"), g.fresh("v")
	num := func(n int64) *twigExpr { return &twigExpr{kind: twInt, n: n} }
	fill := &twigStmt{kind: tsFn, name: f, params: []string{a, b}, body: []*twigStmt{
		{kind: tsLet, name: c, x: twBin("+", twRef(a), twRef(b))},
		{kind: tsLet, name: k, x: twBin("*", twRef(c), num(2))},
		{kind: tsReturn, x: twRef(k)},
	}}
	read := &twigStmt{kind: tsFn, name: h, params: []string{p}, body: []*twigStmt{
		{kind: tsLet, name: r, x: twCallOf(twRef(i))},
		{kind: tsLet, name: w, x: twRef(p)},
		{kind: tsFn, name: i, body: []*twigStmt{{kind: tsReturn, x: twRef(w)}}},
		{kind: tsReturn, x: twRef(r)},
	}}
	return &twigStmt{kind: tsIf, x: &twigExpr{kind: twBool, n: 1}, body: []*twigStmt{
		fill, read,
		{kind: tsExpr, x: twCallOf(twRef("print"), twCallOf(twRef(f), num(int64(g.r.IntN(50))), num(int64(g.r.IntN(50)))))},
		{kind: tsExpr, x: twCallOf(twRef("print"), twCallOf(twRef(h), num(int64(g.r.IntN(50)))))},
	}}
}

func (g *twigGen) ifStmt(d int) *twigStmt {
	s := &twigStmt{kind: tsIf, x: g.expr(tyBool, d-1), body: g.block(d-1, 1+g.r.IntN(3), nil)}
	switch g.r.IntN(3) {
	case 1:
		s.els = g.block(d-1, 1+g.r.IntN(3), nil)
	case 2:
		s.els = []*twigStmt{g.ifStmt(d - 1)}
	}
	return s
}

// twigProgram is the program of seed: its source and what the reference
// says a run with -stats prints. A program whose run the reference finds
// too long is replaced by the next one of the seed.
func twigProgram(seed uint64) (string, string) {
	r := rand.New(rand.NewPCG(seed, 0x7419))
	for {
		g := &twigGen{r: r, scopes: [][]*twigVar{nil}, ctxs: []*twigCtx{{id: 0}}, inject: r.IntN(8) == 0}
		prog := g.block(3, 4+r.IntN(8), nil)
		src := twigPrint(prog)
		if g.injected != nil {
			at := g.injectAt()
			return src, twigOutcome("", "twig: p.tw:"+strconv.Itoa(at.line)+":"+strconv.Itoa(at.col)+": "+g.injected.msg+"\n", 1)
		}
		if want, ok := twigReference(prog); ok {
			return src, want
		}
	}
}

// ---- the printer ----

type twigPrinter struct {
	b         strings.Builder
	line, col int
}

func (p *twigPrinter) put(s string) {
	p.b.WriteString(s)
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			p.line++
			p.col = 1
		} else {
			p.col++
		}
	}
}

func (p *twigPrinter) here() twigPos { return twigPos{p.line, p.col} }

// twigPrint is the source of prog, with every node's position set.
func twigPrint(prog []*twigStmt) string {
	p := &twigPrinter{line: 1, col: 1}
	p.put("// generated\n")
	p.stmts(prog, 0)
	return p.b.String()
}

func (p *twigPrinter) stmts(ss []*twigStmt, depth int) {
	for _, s := range ss {
		p.put(strings.Repeat("\t", depth))
		p.stmt(s, depth)
		p.put("\n")
	}
}

func (p *twigPrinter) body(ss []*twigStmt, depth int) {
	p.put("{\n")
	p.stmts(ss, depth+1)
	p.put(strings.Repeat("\t", depth) + "}")
}

func (p *twigPrinter) stmt(s *twigStmt, depth int) {
	s.at = p.here()
	switch s.kind {
	case tsFn:
		p.put("fn " + s.name + "(" + strings.Join(s.params, ", ") + ") ")
		p.body(s.body, depth)
	case tsLet:
		p.put("let " + s.name + " = ")
		p.expr(s.x, depth)
	case tsAssign:
		p.expr(s.x, depth)
		p.put(" ")
		s.at = p.here()
		p.put("= ")
		p.expr(s.y, depth)
	case tsIf:
		p.put("if ")
		p.expr(s.x, depth)
		p.put(" ")
		p.body(s.body, depth)
		if len(s.els) == 1 && s.els[0].kind == tsIf {
			p.put(" else ")
			p.stmt(s.els[0], depth)
		} else if len(s.els) > 0 {
			p.put(" else ")
			p.body(s.els, depth)
		}
	case tsWhile:
		p.put("while ")
		p.expr(s.x, depth)
		p.put(" ")
		p.body(s.body, depth)
	case tsFor:
		p.put("for " + s.name + " in ")
		p.expr(s.x, depth)
		p.put(" ")
		p.body(s.body, depth)
	case tsReturn:
		p.put("return")
		if s.x != nil {
			p.put(" ")
			p.expr(s.x, depth)
		}
	case tsBreak:
		p.put("break")
	case tsContinue:
		p.put("continue")
	case tsExpr:
		p.expr(s.x, depth)
	}
}

func (p *twigPrinter) expr(e *twigExpr, depth int) {
	e.at = p.here()
	switch e.kind {
	case twInt:
		p.put(strconv.FormatInt(e.n, 10))
	case twFloat:
		text := strconv.FormatFloat(e.f, 'f', -1, 64)
		if !strings.Contains(text, ".") {
			text += ".0"
		}
		p.put(text)
	case twStr:
		p.put("\"" + strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n", "\t", "\\t").Replace(e.s) + "\"")
	case twBool:
		p.put([]string{"false", "true"}[e.n])
	case twNil:
		p.put("nil")
	case twigName:
		p.put(e.s)
	case twUnary:
		p.put("(")
		e.at = p.here()
		if e.s == "-" {
			p.put("-")
		} else {
			p.put("not ")
		}
		p.expr(e.x, depth)
		p.put(")")
	case twBinary:
		p.put("(")
		p.expr(e.x, depth)
		p.put(" ")
		e.at = p.here()
		p.put(e.s + " ")
		p.expr(e.y, depth)
		p.put(")")
	case twCall:
		p.target(e.x, depth)
		e.at = p.here()
		p.put("(")
		p.list(e.args, depth)
		p.put(")")
	case twIndex:
		p.target(e.x, depth)
		e.at = p.here()
		p.put("[")
		p.expr(e.y, depth)
		p.put("]")
	case twField:
		p.target(e.x, depth)
		e.at = p.here()
		p.put("." + e.s)
	case twList:
		p.put("[")
		p.list(e.args, depth)
		p.put("]")
	case twRec:
		p.put("{")
		for i, k := range e.keys {
			if i > 0 {
				p.put(", ")
			}
			p.put(k + ": ")
			p.expr(e.args[i], depth)
		}
		p.put("}")
	case twFn:
		p.put("fn(" + strings.Join(e.params, ", ") + ") ")
		p.body(e.body, depth)
	}
}

func (p *twigPrinter) target(e *twigExpr, depth int) {
	if e.kind == twigName || e.kind == twCall {
		p.expr(e, depth)
		return
	}
	p.put("(")
	p.expr(e, depth)
	p.put(")")
}

func (p *twigPrinter) list(es []*twigExpr, depth int) {
	for i, e := range es {
		if i > 0 {
			p.put(", ")
		}
		p.expr(e, depth)
	}
}

// ---- the reference ----

// The reference's values: twigNil, bool, int64, float64, string, and
// pointers for lists, records and functions, which are equal only to
// themselves; a builtin is its name.
type (
	twigNil     struct{}
	twigBuiltin string
	twigList    struct{ items []any }
	twigRecord  struct {
		keys []string
		vals []any
	}
	twigClosure struct {
		name   string
		code   any // the declaration or the literal
		params []string
		body   []*twigStmt
		env    *twigFrame
	}
	// twigFrame is a call's variables by name: the generator never
	// declares a name twice, so a name is one variable of one function.
	twigFrame struct {
		vars   map[string]any
		parent *twigFrame
	}
)

type twigRun struct {
	out          strings.Builder
	nodes, calls int
}

type twigStop struct{}

type twigJump uint8

const (
	jNone twigJump = iota
	jReturn
	jBreak
	jContinue
)

var twigBuiltins = map[string]int{"print": 1, "len": 1, "push": 2, "str": 1, "float": 1, "int": 1, "sqrt": 1}

// twigReference is what `twig run -stats p.tw` prints for prog, or false
// when the run takes more than a budget of nodes.
func twigReference(prog []*twigStmt) (out string, ok bool) {
	m := &twigRun{}
	var fail *twigFailure
	func() {
		defer func() {
			switch x := recover().(type) {
			case nil:
			case twigFailure:
				fail = &x
			case twigStop:
				ok = false
				fail = nil
				m = nil
			default:
				panic(x)
			}
		}()
		ok = true
		m.block(prog, &twigFrame{vars: twigLocals(nil, prog, map[string]any{})})
	}()
	if m == nil {
		return "", false
	}
	stats := "nodes " + strconv.Itoa(m.nodes) + "\ncalls " + strconv.Itoa(m.calls) + "\n"
	if fail != nil {
		return twigOutcome(m.out.String(), "twig: p.tw:"+strconv.Itoa(fail.at.line)+":"+strconv.Itoa(fail.at.col)+": "+fail.msg+"\n"+stats, 1), true
	}
	return twigOutcome(m.out.String(), stats, 0), true
}

// twigLocals binds in vars, each to nil, the parameters and the names the
// blocks of a function's body declare, not those of the functions inside
// it: a call's variables, which a function a block binds at its start
// may read or set before their let.
func twigLocals(params []string, body []*twigStmt, vars map[string]any) map[string]any {
	for _, p := range params {
		vars[p] = twigNil{}
	}
	for _, s := range body {
		switch s.kind {
		case tsFn, tsLet:
			vars[s.name] = twigNil{}
		case tsFor:
			vars[s.name] = twigNil{}
			twigLocals(nil, s.body, vars)
		case tsIf:
			twigLocals(nil, s.body, vars)
			twigLocals(nil, s.els, vars)
		case tsWhile:
			twigLocals(nil, s.body, vars)
		}
	}
	return vars
}

func (m *twigRun) count() {
	m.nodes++
	if m.nodes > 400_000 {
		panic(twigStop{})
	}
}

func twigFail(at twigPos, msg string) { panic(twigFailure{at, msg}) }

func (m *twigRun) block(ss []*twigStmt, env *twigFrame) (twigJump, any) {
	for _, s := range ss {
		if s.kind == tsFn {
			m.count()
			env.vars[s.name] = &twigClosure{name: s.name, code: s, params: s.params, body: s.body, env: env}
		}
	}
	for _, s := range ss {
		if s.kind == tsFn {
			continue
		}
		m.count()
		if j, v := m.stmt(s, env); j != jNone {
			return j, v
		}
	}
	return jNone, nil
}

func (m *twigRun) set(env *twigFrame, name string, v any) {
	for f := env; f != nil; f = f.parent {
		if _, ok := f.vars[name]; ok {
			f.vars[name] = v
			return
		}
	}
	panic("the generator assigned " + name + ", which no function declares")
}

func (m *twigRun) stmt(s *twigStmt, env *twigFrame) (twigJump, any) {
	switch s.kind {
	case tsLet:
		env.vars[s.name] = m.eval(s.x, env)
	case tsAssign:
		switch t := s.x; t.kind {
		case twigName:
			v := m.eval(s.y, env)
			m.set(env, t.s, v)
		case twIndex:
			x := m.eval(t.x, env)
			i := m.eval(t.y, env)
			v := m.eval(s.y, env)
			k := twigIndex(i, t.at)
			l, ok := x.(*twigList)
			if !ok {
				twigFail(t.at, "a "+twigKind(x)+" has no elements to set")
			}
			if k < 0 || k >= len(l.items) {
				twigFail(t.at, "index "+strconv.Itoa(k)+" is outside a list of "+strconv.Itoa(len(l.items)))
			}
			l.items[k] = v
		case twField:
			x := m.eval(t.x, env)
			v := m.eval(s.y, env)
			r := twigRec(x, t.s, t.at)
			r.vals[twigSlot(r, t.s, t.at)] = v
		}
	case tsIf:
		if m.truth(s.x, env, s.at) {
			return m.block(s.body, env)
		}
		return m.block(s.els, env)
	case tsWhile:
		for m.truth(s.x, env, s.at) {
			j, v := m.block(s.body, env)
			if j == jBreak {
				break
			}
			if j == jReturn {
				return j, v
			}
		}
	case tsFor:
		over := m.eval(s.x, env)
		l, ok := over.(*twigList)
		if !ok {
			twigFail(s.at, "for needs a list, not "+twigKind(over))
		}
		for i := 0; i < len(l.items); i++ {
			env.vars[s.name] = l.items[i]
			j, v := m.block(s.body, env)
			if j == jBreak {
				break
			}
			if j == jReturn {
				return j, v
			}
		}
	case tsReturn:
		if s.x == nil {
			m.count()
			return jReturn, twigNil{}
		}
		return jReturn, m.eval(s.x, env)
	case tsBreak:
		return jBreak, nil
	case tsContinue:
		return jContinue, nil
	case tsExpr:
		m.eval(s.x, env)
	}
	return jNone, nil
}

func (m *twigRun) truth(e *twigExpr, env *twigFrame, at twigPos) bool {
	v := m.eval(e, env)
	b, ok := v.(bool)
	if !ok {
		twigFail(at, "a condition must be a bool, not "+twigKind(v))
	}
	return b
}

func twigKind(v any) string {
	switch v.(type) {
	case twigNil:
		return "nil"
	case bool:
		return "bool"
	case int64:
		return "int"
	case float64:
		return "float"
	case string:
		return "string"
	case *twigList:
		return "list"
	case *twigRecord:
		return "record"
	}
	return "function"
}

func twigIndex(i any, at twigPos) int {
	n, ok := i.(int64)
	if !ok {
		twigFail(at, "an index must be an int, not "+twigKind(i))
	}
	return int(n)
}

func twigRec(x any, key string, at twigPos) *twigRecord {
	r, ok := x.(*twigRecord)
	if !ok {
		twigFail(at, "a "+twigKind(x)+" has no field "+key)
	}
	return r
}

func twigSlot(r *twigRecord, key string, at twigPos) int {
	for i, k := range r.keys {
		if k == key {
			return i
		}
	}
	twigFail(at, "the record has no field "+key)
	return 0
}

func (m *twigRun) lookup(name string, env *twigFrame) any {
	for f := env; f != nil; f = f.parent {
		if v, ok := f.vars[name]; ok {
			return v
		}
	}
	if _, ok := twigBuiltins[name]; ok {
		return twigBuiltin(name)
	}
	panic("the generator read " + name + ", which no function declares")
}

func (m *twigRun) eval(e *twigExpr, env *twigFrame) any {
	m.count()
	switch e.kind {
	case twInt:
		return e.n
	case twFloat:
		return e.f
	case twStr:
		return e.s
	case twBool:
		return e.n == 1
	case twNil:
		return twigNil{}
	case twigName:
		return m.lookup(e.s, env)
	case twUnary:
		v := m.eval(e.x, env)
		if e.s == "not" {
			b, ok := v.(bool)
			if !ok {
				twigFail(e.at, "not cannot take "+twigKind(v))
			}
			return !b
		}
		switch x := v.(type) {
		case int64:
			return -x
		case float64:
			return -x
		}
		twigFail(e.at, "- cannot take "+twigKind(v))
	case twBinary:
		l := m.eval(e.x, env)
		if e.s == "and" || e.s == "or" {
			b, ok := l.(bool)
			if !ok {
				twigFail(e.at, e.s+" cannot take "+twigKind(l))
			}
			if b == (e.s == "or") {
				return b
			}
			r := m.eval(e.y, env)
			if _, ok := r.(bool); !ok {
				twigFail(e.at, e.s+" cannot take "+twigKind(r))
			}
			return r
		}
		return twigBinary(e.s, l, m.eval(e.y, env), e.at)
	case twCall:
		f := m.eval(e.x, env)
		args := make([]any, 0, len(e.args))
		for _, a := range e.args {
			args = append(args, m.eval(a, env))
		}
		return m.call(f, args, e.at)
	case twIndex:
		x := m.eval(e.x, env)
		k := twigIndex(m.eval(e.y, env), e.at)
		switch v := x.(type) {
		case *twigList:
			if k < 0 || k >= len(v.items) {
				twigFail(e.at, "index "+strconv.Itoa(k)+" is outside a list of "+strconv.Itoa(len(v.items)))
			}
			return v.items[k]
		case string:
			if k < 0 || k >= len(v) {
				twigFail(e.at, "index "+strconv.Itoa(k)+" is outside a string of "+strconv.Itoa(len(v)))
			}
			return v[k : k+1]
		}
		twigFail(e.at, "a "+twigKind(x)+" has no elements")
	case twField:
		r := twigRec(m.eval(e.x, env), e.s, e.at)
		return r.vals[twigSlot(r, e.s, e.at)]
	case twList:
		l := &twigList{}
		for _, a := range e.args {
			l.items = append(l.items, m.eval(a, env))
		}
		return l
	case twRec:
		r := &twigRecord{keys: e.keys}
		for _, a := range e.args {
			r.vals = append(r.vals, m.eval(a, env))
		}
		return r
	case twFn:
		return &twigClosure{name: "fn", code: e, params: e.params, body: e.body, env: env}
	}
	panic("unreachable")
}

func twigSame(a, b any) bool {
	switch x := a.(type) {
	case *twigClosure:
		y, ok := b.(*twigClosure)
		return ok && x.code == y.code && x.env == y.env
	case float64:
		y, ok := b.(float64)
		return ok && x == y
	}
	return a == b
}

func twigBinary(op string, l, r any, at twigPos) any {
	switch op {
	case "==":
		return twigSame(l, r)
	case "!=":
		return !twigSame(l, r)
	}
	switch a := l.(type) {
	case int64:
		if b, ok := r.(int64); ok {
			switch op {
			case "+":
				return a + b
			case "-":
				return a - b
			case "*":
				return a * b
			case "/", "%":
				if b == 0 {
					twigFail(at, "division by zero")
				}
				if op == "/" {
					return a / b
				}
				return a % b
			case "<":
				return a < b
			case "<=":
				return a <= b
			case ">":
				return a > b
			case ">=":
				return a >= b
			}
		}
	case float64:
		if b, ok := r.(float64); ok {
			switch op {
			case "+":
				return a + b
			case "-":
				return a - b
			case "*":
				return a * b
			case "/":
				return a / b
			case "<":
				return a < b
			case "<=":
				return a <= b
			case ">":
				return a > b
			case ">=":
				return a >= b
			}
		}
	case string:
		if b, ok := r.(string); ok {
			switch op {
			case "+":
				return a + b
			case "<":
				return a < b
			case "<=":
				return a <= b
			case ">":
				return a > b
			case ">=":
				return a >= b
			}
		}
	}
	twigFail(at, op+" cannot take "+twigKind(l)+" and "+twigKind(r))
	return nil
}

func twigCount(n int) string {
	if n == 1 {
		return "1 argument"
	}
	return strconv.Itoa(n) + " arguments"
}

func (m *twigRun) call(f any, args []any, at twigPos) any {
	m.calls++
	switch fn := f.(type) {
	case *twigClosure:
		if len(args) != len(fn.params) {
			twigFail(at, fn.name+" takes "+twigCount(len(fn.params))+", not "+twigCount(len(args)))
		}
		env := &twigFrame{vars: twigLocals(fn.params, fn.body, map[string]any{}), parent: fn.env}
		for i, p := range fn.params {
			env.vars[p] = args[i]
		}
		if j, v := m.block(fn.body, env); j == jReturn {
			return v
		}
		return twigNil{}
	case twigBuiltin:
		name := string(fn)
		if len(args) != twigBuiltins[name] {
			twigFail(at, name+" takes "+twigCount(twigBuiltins[name])+", not "+twigCount(len(args)))
		}
		return m.builtin(name, args, at)
	}
	twigFail(at, "a "+twigKind(f)+" is not a function")
	return nil
}

func (m *twigRun) builtin(name string, args []any, at twigPos) any {
	v := args[0]
	switch name {
	case "print":
		m.out.WriteString(twigShow(v, 0) + "\n")
		return twigNil{}
	case "len":
		switch x := v.(type) {
		case *twigList:
			return int64(len(x.items))
		case string:
			return int64(len(x))
		}
	case "push":
		if l, ok := v.(*twigList); ok {
			l.items = append(l.items, args[1])
			return twigNil{}
		}
	case "str":
		return twigShow(v, 0)
	case "float":
		switch x := v.(type) {
		case int64:
			return float64(x)
		case float64:
			return x
		}
	case "int":
		switch x := v.(type) {
		case float64:
			if x >= -0x1p63 && x < 0x1p63 {
				return int64(x)
			}
			twigFail(at, "int cannot take a float out of range")
		case int64:
			return x
		}
	case "sqrt":
		if x, ok := v.(float64); ok {
			return math.Sqrt(x)
		}
	}
	twigFail(at, name+" cannot take "+twigKind(v))
	return nil
}

func twigShow(v any, d int) string {
	switch x := v.(type) {
	case twigNil:
		return "nil"
	case bool:
		return strconv.FormatBool(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case string:
		if d == 0 {
			return x
		}
		return strconv.Quote(x)
	case *twigList:
		if d >= 8 {
			return "[...]"
		}
		parts := make([]string, len(x.items))
		for i, item := range x.items {
			parts[i] = twigShow(item, d+1)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *twigRecord:
		if d >= 8 {
			return "{...}"
		}
		parts := make([]string, len(x.keys))
		for i, k := range x.keys {
			parts[i] = k + ": " + twigShow(x.vals[i], d+1)
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case *twigClosure:
		return "<fn " + x.name + ">"
	case twigBuiltin:
		return "<fn " + string(x) + ">"
	}
	panic("unreachable")
}
