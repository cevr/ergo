// Package eval runs a checked program's pure obligations at check time,
// with no Go build (L7, plan section 3). It interprets the checked syntax
// together with the checker's Info: it reads every type, resolution,
// conversion, pattern and failure fact the checker recorded and derives
// none again, so it has no second semantics to drift. A missing fact is an
// internal error, never a guess.
//
// The evaluator runs only what it can prove has no effect: an obligation
// whose footprint (check.Footprint, rows.md Footprints) is not empty is a
// visible skip, and so is a form the evaluator does not interpret yet (a
// Go call, a collection, a function value). Because what runs is pure, a
// skip found part way through a run is sound: nothing observable happened.
//
// Work is bounded by a budget of steps and of call depth, never by time:
// each evaluated node, call and copied byte costs steps before the work
// is done, and a run that exhausts the budget is incomplete, never passed.
//
// Values follow the Go target's semantics, as the lowering does: integers
// wrap at their checked width (int and uint are 64 bits), integer division
// by zero and a negative shift are defects, floats are IEEE at their width,
// strings are bytes. Records, tuples and union values are immutable here,
// so Ergo's value semantics (a copy on every assignment) costs nothing.
//
// Prior art: Zig's comptime runs ordinary functions at compile time under
// an evaluation quota and refuses runtime-only operations by name; Lean's
// #eval runs a checked term with an interactive executor and is no proof.
// Ergo takes the quota and the named refusals, and keeps evaluation apart
// from proof: an example that passes is evaluated, a sampled law tested.
package eval

import (
	"errors"
	"fmt"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Budget bounds one evaluation request: Steps is the total work, Depth the
// deepest call nesting. Both must be positive.
type Budget struct {
	Steps int64
	Depth int
}

// DefaultBudget is the editor budget the plan proposes (section 5): one
// million steps and a call depth of 1024. It is a work bound, not a time
// bound.
func DefaultBudget() Budget { return Budget{Steps: 1_000_000, Depth: 1024} }

// Status is one obligation's verdict.
type Status string

const (
	// Passed: the obligation ran to its end and held.
	Passed Status = "passed"
	// Refuted: an assert was false, a law's body was false, or a typed
	// failure escaped the body. The run goes on.
	Refuted Status = "refuted"
	// Defect: the body hit a defect; it stops the run (runtime.md, Tests 3).
	Defect Status = "defect"
	// Skipped: the obligation cannot run at check time; Reason says why.
	Skipped Status = "skipped"
	// Incomplete: the budget ran out inside the obligation.
	Incomplete Status = "incomplete"
	// NotRun: an earlier defect or an exhausted budget stopped the run
	// before the obligation.
	NotRun Status = "not run"
)

// Site is a source position: the file's name in its package ("" for one
// source text), a 1-based line and byte column.
type Site struct {
	File         string
	Line, Column int
}

// Result is one obligation's record. Every selected obligation has exactly
// one, in the native runner's order: the tests in source order, then the
// package's law runs.
type Result struct {
	// Name is the test's name, or "law L" (top-level) and "law T.L" (a
	// member law), as ergo test names them.
	Name   string
	Status Status
	// Reason is why the obligation was skipped, incomplete or not run, or
	// what refuted it or what the defect was.
	Reason string
	// Site is where it was refuted or hit its defect, or the declaration
	// for a skip.
	Site Site
	// Steps is the work this obligation used.
	Steps int64
}

// Report is the evaluation of one checked package.
type Report struct {
	Results []Result
	// Steps is the work used, of Limit.
	Steps, Limit int64
}

// Refuted reports whether some obligation was refuted.
func (r Report) Refuted() bool { return r.has(Refuted) }

// Defected reports whether a defect stopped the run.
func (r Report) Defected() bool { return r.has(Defect) }

func (r Report) has(s Status) bool {
	for _, res := range r.Results {
		if res.Status == s {
			return true
		}
	}
	return false
}

// ErrBudget is returned for a budget that is not positive.
var ErrBudget = errors.New("eval: the budget's steps and depth must be positive")

// Run evaluates the obligations of pkg, a package the checker accepted
// with no diagnostic. Imports supply definitions; their own tests do not
// run. An error is an internal error: a fact the checker should have
// recorded is missing.
func Run(pkg *check.Package, b Budget) (rep Report, err error) {
	if b.Steps <= 0 || b.Depth <= 0 {
		return Report{}, ErrBudget
	}
	if pkg == nil || pkg.Info == nil {
		return Report{}, errors.New("eval: no checked package")
	}
	in := &interp{budget: b, files: map[syntax.Decl]*syntax.File{}}
	rep.Limit = b.Steps
	defer func() {
		if r := recover(); r != nil {
			ie, ok := r.(internalError)
			if !ok {
				panic(r)
			}
			err = ie
		}
	}()
	stopped := "" // why the rest of the run does not run
	add := func(res Result) {
		if stopped != "" {
			res = Result{Name: res.Name, Status: NotRun, Reason: stopped, Site: res.Site}
		}
		rep.Results = append(rep.Results, res)
		switch res.Status {
		case Defect:
			stopped = "a defect stopped the run"
		case Incomplete:
			stopped = "the evaluation budget ran out"
		}
	}
	for _, t := range pkg.Tests {
		if stopped != "" {
			add(Result{Name: t.Name, Site: in.site(pkg, t.Decl, t.Decl.Pos())})
			continue
		}
		add(in.runTest(pkg, t))
	}
	for _, run := range pkg.LawRuns {
		res := Result{Name: lawName(run.Law), Site: in.site(run.Law.Pkg, run.Law.Decl, run.Law.Decl.Pos())}
		if stopped != "" {
			add(res)
			continue
		}
		add(in.runLaw(pkg, run, res))
	}
	rep.Steps = in.steps
	return rep, nil
}

func lawName(l *check.Law) string {
	switch {
	case l.Owner != nil:
		return "law " + l.Owner.Name + "." + l.Name
	case l.Machine != nil:
		return "law " + l.Machine.Name + "." + l.Name
	}
	return "law " + l.Name
}

// internalError is a missing or contradictory checker fact: the evaluator
// stops and Run returns it, so a gap in Info is never read as success.
type internalError struct{ msg string }

func (e internalError) Error() string { return "eval: internal error: " + e.msg }

func ice(format string, args ...any) {
	panic(internalError{fmt.Sprintf(format, args...)})
}

// runTest runs a test: an example once; a property (a test with
// parameters) takes generated cases, which EV3 brings.
func (in *interp) runTest(pkg *check.Package, t *check.Test) Result {
	res := Result{Name: t.Name, Site: in.site(pkg, t.Decl, t.Decl.Pos())}
	if len(t.Params) > 0 {
		res.Status, res.Reason = Skipped, "a property takes generated cases, which check-time evaluation does not draw yet"
		return res
	}
	if why := in.impure(pkg, t.Decl); why != "" {
		res.Status, res.Reason = Skipped, why
		return res
	}
	return in.runBody(pkg, t.Decl, t.Decl.Body, false, res)
}

// runLaw runs a law run that needs no generated value: a top-level law
// with no parameters, one case. Every other law waits for generated cases
// (EV3) or the explorer (EV4), and is a visible skip.
func (in *interp) runLaw(pkg *check.Package, run check.LawRun, res Result) Result {
	l := run.Law
	switch {
	case l.Unrun != "":
		res.Status, res.Reason = Skipped, l.Unrun
	case l.Machine != nil:
		res.Status, res.Reason = Skipped, "a machine law runs on the explorer, which check-time evaluation does not drive yet"
	case len(run.Bindings) == 0:
		res.Status, res.Reason = Skipped, "no binding of the law runs in this package"
	case len(l.Params) > 0 || len(l.Slots) > 0 || l.Pkg != pkg:
		res.Status, res.Reason = Skipped, "a law takes generated cases, which check-time evaluation does not draw yet"
	case l.Decl.Body == nil:
		ice("the law %s has no body", l.Name)
	default:
		if why := in.impure(l.Pkg, l.Decl); why != "" {
			res.Status, res.Reason = Skipped, why
			return res
		}
		return in.runBody(l.Pkg, l.Decl, l.Decl.Body, true, res)
	}
	return res
}

// impure is why the frame key's footprint forbids running it, "" when it
// is empty. A missing footprint is an internal error: an absent fact is
// not an empty one (rows.md, Footprints 2).
func (in *interp) impure(pkg *check.Package, key syntax.Node) string {
	fp, ok := pkg.Info.Footprints[key]
	if !ok {
		ice("no footprint for the frame at %d", key.Pos())
	}
	switch {
	case len(fp.Caps) > 0:
		return fmt.Sprintf("it uses the capability %s, which check-time evaluation never runs", fp.Caps[0])
	case fp.Tasks:
		return "it starts a task, which check-time evaluation never runs"
	case fp.Opaque:
		return "it calls code whose footprint is unknown (a function value, an operation or a Go function)"
	}
	return ""
}

// runBody runs one obligation's body as a frame and turns its outcome into
// the result.
func (in *interp) runBody(pkg *check.Package, decl syntax.Decl, body *syntax.BlockStmt, law bool, res Result) Result {
	start := in.steps
	fr := in.newFrame(pkg, decl)
	var val Value
	var ab *abrupt
	if law {
		val, ab = in.bodyValue(fr, body.List)
	} else {
		ab = in.stmts(fr, body.List)
	}
	res.Steps = in.steps - start
	if ab == nil {
		if law && val != nil && !truth(val) {
			res.Status, res.Reason = Refuted, "the law is false"
			res.Site = in.site(pkg, decl, lastPos(body))
			return res
		}
		res.Status = Passed
		return res
	}
	res.Site = in.site(ab.pkg, ab.decl, ab.at)
	switch ab.kind {
	case ctlReturn:
		if law && !truth(ab.val) {
			res.Status, res.Reason = Refuted, "the law is false"
			return res
		}
		res.Status = Passed
		res.Site = in.site(pkg, decl, decl.Pos())
	case ctlAssert:
		res.Status, res.Reason = Refuted, "assert failed"
	case ctlFail, ctlEscape:
		res.Status, res.Reason = Refuted, "a failure escaped: "+describe(ab.val)
		res.Site = in.site(pkg, decl, decl.Pos())
	case ctlDefect:
		res.Status, res.Reason = Defect, ab.msg
	case ctlExhausted:
		res.Status, res.Reason = Incomplete, ab.msg
	case ctlUnsupported:
		res.Status, res.Reason = Skipped, ab.msg
	default:
		ice("the control %d left the body", ab.kind)
	}
	return res
}

func lastPos(b *syntax.BlockStmt) syntax.Pos {
	if len(b.List) > 0 {
		return b.List[len(b.List)-1].Pos()
	}
	return b.Pos()
}

// site locates pos of the declaration decl of pkg.
func (in *interp) site(pkg *check.Package, decl syntax.Decl, pos syntax.Pos) Site {
	f := in.fileOf(pkg, decl)
	if f == nil {
		return Site{}
	}
	line, col := f.Position(pos)
	return Site{File: f.Name, Line: line, Column: col}
}

// fileOf is the file of pkg that declares decl: a top-level declaration,
// or a law inside an operation or machine type.
func (in *interp) fileOf(pkg *check.Package, decl syntax.Decl) *syntax.File {
	if decl == nil || pkg == nil {
		return nil
	}
	if f, ok := in.files[decl]; ok {
		return f
	}
	for _, f := range pkg.Files() {
		for _, d := range f.Decls {
			found := d == decl
			if td, isType := d.(*syntax.TypeDecl); isType && !found {
				// A member law: inside its type's declaration.
				syntax.Inspect(td, func(n syntax.Node) bool {
					found = found || n == decl
					return !found
				})
			}
			if found {
				in.files[decl] = f
				return f
			}
		}
	}
	in.files[decl] = nil
	return nil
}
