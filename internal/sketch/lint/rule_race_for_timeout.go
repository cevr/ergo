package lint

import (
	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// raceForTimeout is the hint for a race against a sleep that a timeout
// states (plan §3; tsgo raceFirstWithSleepToTimeout). It matches std's
// tasks.Race and the prelude Scheduler's Sleep by identity
// (check.RoleOf, check.IsSchedulerSleep). Its fix is safe: the timeout
// runs the same body, and when the deadline passes first it gives the
// same fallback the sleeping block gave.
var raceForTimeout = &Rule{
	Name:    "race-for-timeout",
	Code:    "L106",
	Doc:     "A tasks.Race of a block against a block that only sleeps and gives a fallback is a timeout; write timeout.After(d, block) and catch timeout.Expired.",
	Default: Hint,
	Check:   checkRaceForTimeout,
	SafeFix: true,
}

// sleeper is a race block that only sleeps on the Scheduler and then gives
// a value: its duration and its fallback.
type sleeper struct {
	d, fallback syntax.Expr
}

func checkRaceForTimeout(p *Pass) {
	if p.Package.Std() || p.Package.Host() {
		return
	}
	caught := map[syntax.Expr]bool{}
	syntax.Inspect(p.File, func(n syntax.Node) bool {
		if c, ok := n.(*syntax.CatchExpr); ok {
			caught[c.X] = true
		}
		return true
	})
	syntax.Inspect(p.File, func(n syntax.Node) bool {
		x, ok := n.(*syntax.CallExpr)
		if !ok {
			return true
		}
		call := p.Info.Calls[x]
		if call == nil || check.RoleOf(call.Func) != check.RoleRace || len(x.Args) != 1 {
			return true
		}
		lit, ok := x.Args[0].(*syntax.CompositeLit)
		if !ok || len(lit.Elts) != 2 {
			return true
		}
		var blocks [2]*syntax.FuncLit
		var sleeps [2]*sleeper
		for i, e := range lit.Elts {
			fl, isLit := e.(*syntax.FuncLit)
			if !isLit {
				return true
			}
			blocks[i], sleeps[i] = fl, sleeperOf(p.Info, fl)
		}
		if (sleeps[0] == nil) == (sleeps[1] == nil) {
			return true
		}
		timer, body := 0, 1
		if sleeps[0] == nil {
			timer, body = 1, 0
		}
		message := "this race against a sleep is a timeout: write timeout.After(d, block) and catch timeout.Expired with the fallback"
		var fix *report.Fix
		if !caught[x] && !failsExpired(call) {
			fix = timeoutFixOf(p, x, blocks[body], sleeps[timer])
		}
		p.ReportFix(x.Pos(), x.End(), message, fix)
		return true
	})
}

// sleeperOf is the block's sleep and fallback when its body is a
// Scheduler.Sleep(d) and then a value (a tail or a return), with d and the
// value free of try and of calls that add to the footprint; nil otherwise.
func sleeperOf(info *check.Info, fl *syntax.FuncLit) *sleeper {
	if fl.Body == nil || len(fl.Body.List) != 2 {
		return nil
	}
	first, ok := fl.Body.List[0].(*syntax.ExprStmt)
	if !ok {
		return nil
	}
	sleep, ok := first.X.(*syntax.CallExpr)
	if !ok || !check.IsSchedulerSleep(info, sleep) || len(sleep.Args) != 1 {
		return nil
	}
	var fallback syntax.Expr
	switch last := fl.Body.List[1].(type) {
	case *syntax.ExprStmt:
		fallback = last.X
	case *syntax.ReturnStmt:
		if len(last.Results) == 1 {
			fallback = last.Results[0]
		}
	}
	if fallback == nil || !quiet(info, sleep.Args[0]) || !quiet(info, fallback) {
		return nil
	}
	return &sleeper{d: sleep.Args[0], fallback: fallback}
}

// quiet reports an expression with no try and no call that adds to the
// footprint, outside function literals: evaluating it earlier or later
// does the same.
func quiet(info *check.Info, e syntax.Expr) bool {
	ok := true
	syntax.Inspect(e, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.FuncLit:
			return false
		case *syntax.TryExpr:
			ok = false
		case *syntax.CallExpr:
			if check.CallAddsFootprint(info, n) {
				ok = false
			}
		}
		return ok
	})
	return ok
}

// failsExpired reports a race whose failure row already holds
// timeout.Expired: a catch of the timeout's Expired would catch it too.
func failsExpired(call *check.Call) bool {
	if call.Fails == nil {
		return false
	}
	for _, t := range call.Fails.Elems() {
		if check.IsTimeoutExpired(t) {
			return true
		}
	}
	return false
}

// timeoutFixOf is the fix of the race x: nil when the moved text cannot
// keep the formatter's layout, or when the name timeout is taken.
func timeoutFixOf(p *Pass, x *syntax.CallExpr, body *syntax.FuncLit, s *sleeper) *report.Fix {
	src := p.File.Src
	qual, imports, imported := timeoutPackage, []string(nil), false
	for _, im := range p.Package.ImportsOf(p.File) {
		if check.IsTimeoutPackage(im.Pkg) {
			qual, imported = im.Name, true
		}
	}
	if !imported {
		if p.Package.FileBinds(p.File, timeoutPackage) {
			return nil
		}
		qual, imports = timeoutPackage, []string{timeoutPackage}
	}
	indent := lineIndent(src, int(x.Pos()))
	extra, ok := extraIndent(indent, lineIndent(src, int(body.Pos())))
	if !ok {
		return nil
	}
	bodyText, ok := outdent(src[body.Pos():body.End()], extra)
	if !ok {
		return nil
	}
	fallback, ok := outdent(src[s.fallback.Pos():s.fallback.End()], extra)
	if !ok {
		return nil
	}
	text := timeoutFix(qual, src[s.d.Pos():s.d.End()], bodyText, fallback, indent)
	race := p.Info.Calls[x].Func.Pkg.Path
	return &report.Fix{InsertAt: int(x.Pos()), Replace: int(x.End() - x.Pos()), Text: text, Imports: imports, Unused: []string{race}}
}
