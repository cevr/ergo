package lint

import "ergo.local/prototype/internal/report"

// Advice is the run as the diagnostic report merges it.
func (r Result) Advice() *report.LintAdvice {
	advice := &report.LintAdvice{Complete: r.Complete}
	for _, f := range r.Findings {
		span := report.Span{}
		if f.Line > 0 {
			span = report.Span{Offset: f.Offset, Length: f.Length, Line: f.Line, Column: f.Column}
		}
		advice.Findings = append(advice.Findings, report.LintFinding{Code: f.Code, Rule: f.Rule, Severity: f.Severity.String(), Message: f.Message, File: f.File, Span: span, Fix: f.Fix})
	}
	for _, s := range r.Suppressions {
		advice.Suppressions = append(advice.Suppressions, report.SuppressionStatus{
			Rule: s.Rule, Status: s.Status, Reason: s.Reason, File: s.File, Line: s.Covered,
			Span: report.Span{Offset: s.Offset, Length: s.Length, Line: s.Line, Column: s.Column},
		})
	}
	return advice
}
