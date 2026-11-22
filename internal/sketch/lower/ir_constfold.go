package lower

// IR builder: build-time folding (CF1). A call of a function whose row is
// empty (no needs, no failure: eval.Rowless reads the checker's row, never
// a list of names) whose arguments are constants, transitively (literals,
// constants, record and list literals of them, other such calls, and
// package functions passed as values), is computed at build time by the
// evaluator (eval.FoldValue) and built as its constant: a basic value, or
// a record, a list or a union's variant of constants. A call that reads a
// local, that runs past the fold budget, or whose value holds a function,
// a pointer, a map or a cell stays as written. std html's static subtree is
// such a call: its value is one Node of one string (stdlib.md, HTML 4).
//
// The fold runs on the IR path only; the text emitter, frozen, never
// folds. constFoldLowering = "off" (-ldflags -X or SetConstFoldLoweringForTest)
// keeps every call as written: the reference the fold property compares
// against (TestFoldPrintsWhatTheCallPrints).

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/toolchain"
)

// constFoldLowering selects the fold: "" folds, "off" keeps every call.
var constFoldLowering = ""

// SetConstFoldLoweringForTest selects the fold ("" or "off") and returns the
// previous one.
func SetConstFoldLoweringForTest(mode string) string {
	prev := constFoldLowering
	constFoldLowering = mode
	return prev
}

// The evaluator runs a Go-backed std function through its native, the
// compiler's Go's code (Unicode tables, strconv's formatting). So a fold
// runs only when the Go that builds the program and the Go the compiler
// runs on are both the pinned release (toolchain.Version); otherwise, or
// when the build's Go cannot say, every call stays. This is LF3c r2's one
// check (wf10/lf3c ir_fold.go foldGoAgrees, by the orchestrator's ruling
// of 2026-11-17), written here until the lines merge, when this pass calls
// that one instead.
//
// constFoldBuildGo follows toolchain.Select's rule for ERGO_GO: unset or
// "managed" is the pinned release, built with GOTOOLCHAIN=local so no
// go.mod line chooses another; "system" or a path is the release that go
// command reports (go env GOVERSION), asked once per choice; "" when it
// cannot say.
var (
	constFoldCompilerGo = runtime.Version()
	constFoldBuildGo    = constFoldBuildRelease
)

var (
	constFoldReleaseMu sync.Mutex
	constFoldReleases  map[string]string // made on first use
)

// constFoldBuildRelease is the release of the Go the build selects.
func constFoldBuildRelease() string {
	t, _ := toolchain.Status(os.Getenv, "")
	if t.Mode == "managed" {
		return toolchain.Version
	}
	constFoldReleaseMu.Lock()
	defer constFoldReleaseMu.Unlock()
	v, ok := constFoldReleases[t.Go]
	if !ok {
		cmd := exec.Command(t.Go, "env", "GOVERSION")
		cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local")
		if b, err := cmd.Output(); err == nil {
			v = strings.TrimSpace(string(b))
		}
		if constFoldReleases == nil {
			constFoldReleases = map[string]string{}
		}
		constFoldReleases[t.Go] = v
	}
	return v
}

// SetConstFoldBuildGoForTest makes the lowerings that follow see the
// program built by the Go release v, and returns what restores the one
// before it.
func SetConstFoldBuildGoForTest(v string) (restore func()) {
	prev := constFoldBuildGo
	constFoldBuildGo = func() string { return v }
	return func() { constFoldBuildGo = prev }
}

// SetConstFoldCompilerGoForTest makes the lowerings that follow see the
// compiler running on the Go release v, and returns what restores the one
// before it.
func SetConstFoldCompilerGoForTest(v string) (restore func()) {
	prev := constFoldCompilerGo
	constFoldCompilerGo = v
	return func() { constFoldCompilerGo = prev }
}

// foldGoPinned reports a build whose Go and whose compiler's Go are the
// pinned release.
func foldGoPinned() bool {
	return constFoldCompilerGo == toolchain.Version && constFoldBuildGo() == toolchain.Version
}

// constFoldBudget bounds one fold: a call that needs more stays as written.
// The fluid gallery's button page, a 28 KB tree, takes 0.5M to 1M.
var constFoldBudget = eval.Budget{Steps: 2_000_000, Depth: 64}

// SetConstFoldBudgetForTest sets the steps of one fold and returns the
// previous steps.
func SetConstFoldBudgetForTest(steps int64) int64 {
	prev := constFoldBudget.Steps
	constFoldBudget.Steps = steps
	return prev
}

// FoldCounts are the fold's outcomes over a lowering's IR bodies: the
// calls built as constants, the candidates the evaluator could not finish
// within the budget, and the other candidates it refused (a value that is
// no constant, a form it does not run), in the bodies that took IR. It is
// a count, never a time. Templates counts the calls built as templates and
// TemplateRefused the candidates that made none (ir_template.go). NoNative
// counts the refused that called a Go function the evaluator has no
// native of (eval.NoNative), a part of Refused.
type FoldCounts struct {
	Folded          int `json:"folded"`
	OverBudget      int `json:"overBudget"`
	Refused         int `json:"refused"`
	NoNative        int `json:"noNative"`
	Templates       int `json:"templates"`
	TemplateRefused int `json:"templateRefused"`
}

func (c *FoldCounts) add(d FoldCounts) {
	c.Folded += d.Folded
	c.OverBudget += d.OverBudget
	c.Refused += d.Refused
	c.NoNative += d.NoNative
	c.Templates += d.Templates
	c.TemplateRefused += d.TemplateRefused
}

// fold is the call x built as its constant, or nil when x is no candidate
// or does not fold.
func (b *irBuilder) fold(x *syntax.CallExpr) (out *irExpr) {
	if constFoldLowering == "off" || !foldGoPinned() || b.decl == nil || b.pkg == nil || b.law != nil {
		return nil
	}
	c := b.l.info.Calls[x]
	if c == nil || c.Kind != check.CallFunc || c.Func == nil || x.Trailing != nil || len(c.TypeArgs) > 0 || c.StartsTask {
		return nil
	}
	if c.Func.Pkg == nil || c.Func.Pkg.Host() || !eval.Rowless(c.Func) {
		return nil
	}
	// A call of an extern stays: the built program runs its Go body, which
	// may answer otherwise than the evaluator's native by design (std
	// html's inFold is false there and true in a fold). A fold of an Ergo
	// function still runs the externs it calls.
	if d := c.Func.Decl; d == nil || d.Body == nil || d.Extern != nil {
		return nil
	}
	t := b.l.info.Types[x]
	if t == nil || !b.closed(x) {
		return nil
	}
	counts := &b.foldCounts
	v, why, over := eval.FoldValue(b.pkg, b.decl, x, constFoldBudget)
	if why != "" {
		if over {
			counts.OverBudget++
		} else {
			counts.Refused++
			if eval.NoNative(why) {
				counts.NoNative++
			}
		}
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			if _, isNo := r.(irUnsupported); !isNo {
				panic(r)
			}
			// A constant the IR cannot spell: the call stays.
			counts.Refused++
			out = nil
		}
	}()
	e := b.constOf(v, t, x, b.pos(x, x.Pos()))
	counts.Folded++
	return e
}

// closed reports whether x reads no local: its operands are literals,
// constants, record and list literals, calls and package functions, all
// closed in turn. The evaluator decides the rest.
func (b *irBuilder) closed(x syntax.Expr) bool {
	info := b.l.info
	if _, ok := info.Values[x]; ok {
		return true
	}
	switch x := x.(type) {
	case *syntax.BasicLit:
		return true
	case *syntax.ParenExpr:
		return b.closed(x.X)
	case *syntax.Ident:
		switch info.Uses[x].(type) {
		case *check.Func, *check.TypeName:
			return true
		}
		return false
	case *syntax.SelectorExpr:
		// pkg.F, a function or a type of an imported package.
		switch check.QualifiedMember(info, x).(type) {
		case *check.Func, *check.TypeName:
			return true
		}
		return false
	case *syntax.CompositeLit:
		if x.Base != nil {
			return false
		}
		for _, e := range x.Elts {
			if kv, ok := e.(*syntax.KeyValueExpr); ok {
				e = kv.Value
			}
			if !b.closed(e) {
				return false
			}
		}
		return true
	case *syntax.CallExpr:
		if x.Trailing != nil || !b.closed(x.Fun) {
			return false
		}
		for _, a := range x.Args {
			if !b.closed(a) {
				return false
			}
		}
		return true
	case *syntax.UnaryExpr:
		return b.closed(x.X)
	case *syntax.BinaryExpr:
		return b.closed(x.X) && b.closed(x.Y)
	}
	return false
}

// constOf is the IR of the folded value v in a place of type t.
func (b *irBuilder) constOf(v eval.Const, t check.Type, site syntax.Expr, p irPos) *irExpr {
	switch v.Kind {
	case eval.ConstHole:
		return b.holeInto(v.Hole, t, site) // ir_template.go
	case eval.ConstBasic:
		return b.constant(t, v.Basic, site, p)
	case eval.ConstSlice:
		s, ok := t.(*check.Slice)
		if !ok {
			irNo("folded list of type %s", t)
		}
		lit := &irExpr{K: "lit", Pos: p, T: b.goType(t, site)}
		for _, e := range v.Elems {
			lit.Elems = append(lit.Elems, b.constOf(e, s.Elem, site, p))
		}
		return lit
	case eval.ConstRecord:
		n, ok := t.(*check.Named)
		if !ok {
			irNo("folded record of type %s", t)
		}
		if n.Obj == v.Named.Obj {
			s, ok := n.Underlying().(*check.Struct)
			if !ok || len(s.Fields) != len(v.Elems) {
				irNo("folded record's fields")
			}
			if len(s.Fields) == 0 {
				return &irExpr{K: "zero", Pos: p, T: b.goType(t, site)}
			}
			td := b.l.typeDecl(n.Obj, site)
			lit := &irExpr{K: "lit", Pos: p, T: b.goType(t, site)}
			for i, f := range s.Fields {
				lit.Keys = append(lit.Keys, td.fieldName[f.Name])
				lit.Elems = append(lit.Elems, b.constOf(v.Elems[i], f.Type, site, p))
			}
			return lit
		}
		u, ok := n.Underlying().(*check.Union)
		if !ok {
			irNo("folded record into %s", t)
		}
		for _, vt := range u.Variants {
			if vn, ok := vt.(*check.Named); ok && vn.Obj == v.Named.Obj {
				return b.variantInto(vt, t, b.constOf(v, vt, site, p), site)
			}
		}
		irNo("folded variant of %s", t)
	}
	irNo("folded value of kind %d", v.Kind)
	return nil
}
