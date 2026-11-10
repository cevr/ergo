package sketchfront

import (
	"errors"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/eval"
)

// Evaluate runs the root package's pure obligations, its examples first,
// with the checked syntax and Info this analysis already holds (L7; no Go
// build, no emitted module, no child process). It is a separate step from
// Check, which stays static: only an analysis with no diagnostic is
// evaluated, and the budget bounds its work.
func (a Analysis) Evaluate(b eval.Budget) (eval.Report, error) {
	pkgs := a.Checked()
	if len(pkgs) == 0 || len(a.Diagnostics) > 0 {
		return eval.Report{}, errors.New("sketchfront: evaluation needs a program that checks")
	}
	return eval.Run(pkgs[len(pkgs)-1], b)
}

// Evaluation is Evaluate as the check envelope reports it (report.Evaluation):
// its Revision is the analysed text's, so it names the source it ran on,
// and each obligation that did not pass names the native command that
// replays it, `ergo test SOURCE --seed N` (with --law TITLE for a law),
// source being the path as the user gave it.
func (a Analysis) Evaluation(b eval.Budget, source string) (report.Evaluation, error) {
	rep, err := a.Evaluate(b)
	if err != nil {
		return report.Evaluation{}, err
	}
	out := report.Evaluation{
		Revision:   report.Revision(a.Text()),
		Steps:      rep.Steps,
		StepsLimit: rep.Limit,
		Bytes:      rep.Bytes,
		BytesLimit: rep.BytesLimit,
	}
	statuses := make([]string, 0, len(rep.Results))
	out.Obligations = make([]report.Obligation, 0, len(rep.Results))
	for _, res := range rep.Results {
		o := report.Obligation{
			Name:       res.Name,
			Status:     string(res.Status),
			Reason:     res.Reason,
			Site:       report.Site{File: res.Site.File, Line: res.Site.Line, Column: res.Site.Column},
			Steps:      res.Steps,
			Cases:      res.Cases,
			Case:       res.Case,
			TotalCases: res.TotalCases,
			Evidence:   res.Evidence,
			Seed:       res.Seed,
		}
		for _, arg := range res.Arguments {
			o.Arguments = append(o.Arguments, report.Argument{Name: arg.Name, Value: arg.Value})
		}
		if m := res.Machine; m != nil {
			o.Machine = &report.MachineSearch{States: m.States, Edges: m.Edges, Depth: m.Depth, Checked: m.Checked, Steps: m.Steps, Complete: m.Complete, Runs: m.Runs, Run: m.Run}
		}
		if res.Status != eval.Passed {
			o.Replay = replay(source, b.Seed, res.Name)
		}
		statuses = append(statuses, o.Status)
		out.Obligations = append(out.Obligations, o)
	}
	out.Status = report.EvaluationStatus(statuses)
	return out, nil
}

// replay is the ergo test command that runs the obligation name natively
// with the same seed: a law by its report's title, a test with the rest.
func replay(source string, seed uint64, name string) string {
	if strings.ContainsAny(source, " \t\n'\"\\$`") || strings.HasPrefix(source, "-") {
		source = strconv.Quote(source)
	}
	cmd := "ergo test " + source + " --seed " + strconv.FormatUint(seed, 10)
	if title, ok := strings.CutPrefix(name, "law "); ok {
		title, _, _ = strings.Cut(title, " ")
		cmd += " --law " + title
	}
	return cmd
}
