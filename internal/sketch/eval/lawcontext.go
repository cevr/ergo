package eval

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketch/testplan"
	"ergo.local/prototype/runtime/testgen"
)

// contextLaw runs a member law under a named context that binds its owner
// (types.md, Laws 10; LawRun.Contexts), as ergo test does: by the one rule
// (testplan.Context), each case listing or drawing the law's parameters
// (from the stream of the run's name), then
// building the context as a with of it does, then running the law's body
// against the provider the context bound to the owner. The provider is
// the one the program uses, made by its constructor with its own
// capabilities in place.
func (in *interp) contextLaw(law *check.Law, lc *check.LawContext, res Result) (out Result) {
	fr := in.newFrame(law.Pkg, law.Decl)
	start := in.steps
	out = res
	out.TotalCases = testplan.Cases
	in.casesBudget(testplan.Cases)
	defer func() {
		if r := recover(); r != nil {
			ab, ok := r.(*abrupt)
			if !ok {
				panic(r)
			}
			out.Status, out.Reason, out.Site = statusOf(ab), ab.msg, in.site(ab.pkg, ab.decl, ab.at)
		}
		out.Steps = in.steps - start
		if out.Status != Passed {
			out.Evidence = ""
		}
	}()
	cap, ok := lc.With.Discharged[0].(*check.Named)
	if !ok {
		ice("a law run under %s whose owner is no capability", lc.Context.Name)
	}
	if own := testplan.GenericOwner(law); own != nil {
		fr.env = &tenv{}
		for i, tp := range own.TParams {
			fr.env.bind(tp, rtype{t: cap.TArgs[i]})
		}
	}
	claimInput := -1
	if lc.Claim != nil {
		info, why := lc.Claim.ClaimInfo(law)
		if why != "" {
			ice("claim law %s: %s", law.Name, why)
		}
		fr.info, fr.tab = info, in.table(info)
		claimInput = lc.Claim.ClaimFeed - 1
	}
	pre := preflight{in: in, seen: map[syntax.Node]bool{}}
	if why := pre.body(fr, fr.decl, law.Decl.Body, nil, nil); why != "" {
		out.Status, out.Reason = Skipped, why
		return
	}
	types := make([]check.Type, len(law.Params))
	for k, v := range law.Params {
		types[k] = v.Type
		if lc.Params != nil {
			types[k] = lc.Params[k]
		}
		if k == claimInput {
			continue
		}
		if why := pre.generator(fr, types[k], nil, map[check.Type]bool{}); why != "" {
			out.Status, out.Reason = Skipped, why
			return
		}
	}
	// The one rule, as ergo test runs it (lawContextRun's listedRun).
	p := testplan.Context(law.Params, types, claimInput)
	out.TotalCases = p.Count
	out.Evidence, out.Seed, out.form = "tested", in.budget.Seed, formDrawn
	if p.Listed {
		out.Evidence, out.form = "proved", formListed
	}
	r := testgen.New(in.budget.Seed, res.Name)
	r.Charge = func(n int64) { mustCharge(in, fr, n) }
	// run draws case k's arguments from r, as it stands (or takes the
	// listed case k), builds the context and runs the law.
	run := func(k int) (Result, []func() Argument) {
		cf := in.newFrame(fr.pkg, fr.decl)
		cf.env = fr.env
		cf.info, cf.tab = fr.info, fr.tab
		var listed []testplan.Value
		if p.Listed {
			listed = p.At(k)
		}
		shows := make([]func() Argument, 0, len(law.Params))
		for i, j := 0, 0; i < len(law.Params); i++ {
			if i == claimInput {
				continue
			}
			v := law.Params[i]
			var a Value
			if p.Listed {
				a = in.listValue(cf, listed[j])
			} else {
				a = in.generate(cf, r, types[i], testplan.Depth, nil)
			}
			shows = append(shows, func() Argument { return Argument{Name: v.Name, Value: in.argument(cf, types[i], a)} })
			cf.bind(v, a)
			j++
		}
		caps, built, ab := in.acquire(cf, law.Decl, lc.With)
		if ab != nil {
			return in.abruptResult(res, ab), shows
		}
		key := typeKey(cf.rt(cap))
		own, found := caps.lookup(key)
		if !found {
			ice("the context %s does not bind %s", lc.Context.Name, cap)
		}
		outer := cf.caps
		cf.caps = caps
		if claimInput < 0 {
			cf.lawOwn = own
		} else {
			entry := lc.With.Entries[0]
			for _, a := range built {
				if a.a.Entry == entry {
					cf.bind(law.Params[claimInput], a.v)
					shows = append(shows, func() Argument {
						return Argument{Name: law.Params[claimInput].Name, Value: in.argument(cf, lc.Claim.At, a.v)}
					})
					break
				}
			}
		}
		result := in.runBodyFrame(cf, law.Decl.Body, true, res)
		// The case owns what its build acquired: it releases them after
		// the law's last operation, last built first, each once; a
		// release's defect fails the case as any defect does (types.md,
		// Laws 10, Under a context).
		if result.Status == Passed || result.Status == Refuted || result.Status == Defect {
			if rel := in.releaseAll(cf, law.Decl, built, outer, nil); rel != nil && (result.Status != Defect || rel.kind != ctlDefect) {
				result = in.abruptResult(result, rel)
			}
		}
		return result, shows
	}
	defer in.beginSometimes(fr, law.Decl.Body)()
	evSeen, evSummary := false, ""
	if law.Sometimes {
		for k := 0; k < p.Count; k++ {
			out.Case = k + 1
			mustCharge(in, fr, 1)
			r.Start(k + 1)
			r.Record()
			in.resetCaseEv()
			result, shows := run(k)
			out.Cases = k + 1
			if result.Status == Passed {
				out.Status, out.Reason, out.Site, out.escaped, out.Message = result.Status, result.Reason, result.Site, result.escaped, result.Message
				if result.Schedule != 0 {
					out.Schedule = result.Schedule
				}
				if !in.applyCaseEv(&out, true) {
					out.Case, out.Arguments = k+1, shown(shows)
					return
				}
				out.Case = 0
				out.Arguments = shown(shows)
				in.checkSometimes(&out, k+1)
				return
			}
			if result.Status == Refuted && result.bodyFalse {
				in.resetCaseEv()
				continue
			}
			if result.Status == Refuted && !p.Listed && len(law.Params) > 0 {
				result, shows = in.shrinkCase(r, result, shows, func() (Result, []func() Argument) { in.resetCaseEv(); return run(k) })
			}
			out.Status, out.Reason, out.Site, out.escaped, out.Message = result.Status, result.Reason, result.Site, result.escaped, result.Message
			if result.Schedule != 0 {
				out.Schedule = result.Schedule
			}
			in.resetCaseEv()
			out.Case, out.Arguments, out.Drawn, out.ShrunkTape = k+1, shown(shows), result.Drawn, result.ShrunkTape
			return
		}
		msg := fmt.Sprintf("no witness found across %d cases", p.Count)
		out.Status = Refuted
		out.Reason = msg
		out.Message = msg
		out.Site = in.site(fr.pkg, fr.decl, fr.decl.Pos())
		out.Case = 0
		return
	}
	for k := 0; k < p.Count; k++ {
		out.Case = k + 1
		mustCharge(in, fr, 1)
		r.Start(k + 1)
		r.Record()
		in.resetCaseEv()
		result, shows := run(k)
		out.Cases = k + 1
		if result.Status == Refuted && !p.Listed && len(law.Params) > 0 {
			result, shows = in.shrinkCase(r, result, shows, func() (Result, []func() Argument) { in.resetCaseEv(); return run(k) })
		}
		out.Status, out.Reason, out.Site, out.escaped, out.Message = result.Status, result.Reason, result.Site, result.escaped, result.Message
		if result.Schedule != 0 {
			out.Schedule = result.Schedule
		}
		if result.Status != Passed {
			in.resetCaseEv()
			out.Case, out.Arguments, out.Drawn, out.ShrunkTape = k+1, shown(shows), result.Drawn, result.ShrunkTape
			return
		}
		if !in.applyCaseEv(&out, true) {
			out.Case, out.Arguments = k+1, shown(shows)
			return
		}
		if out.Evidence == "tested" && out.Reason != "" {
			evSeen = true
			if evSummary == "" {
				evSummary = out.Reason
			} else if out.Reason != evSummary {
				evSummary += "; " + out.Reason
			}
		}
	}
	if !in.checkSometimes(&out, out.Cases) {
		return
	}
	if evSeen {
		out.Evidence = "tested"
		out.Reason = evSummary
	}
	out.Case = 0
	return
}
