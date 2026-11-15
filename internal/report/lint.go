package report

import (
	"sort"

	sdk "ergo.local/prototype/lint"
)

// LintFinding is one finding a lint run hands the report: its code (L000,
// L001 or a rule's L1xx), the rule's name, the severity the configuration
// gives it (error, warning, information or hint), the message and where it
// applies. File follows Diagnostic.File. Related and Suggestions are a rule
// pack finding's, as the pack reported them.
type LintFinding struct {
	Code        string
	Rule        string
	Severity    string
	Message     string
	File        string
	Span        Span
	Fix         *Fix
	Related     []sdk.Related
	Suggestions []sdk.Suggestion
}

// SuppressionStatus is the outcome of one admitted suppression directive
// (`// ergo-lint-disable-next-line RULE -- REASON`): applied, unused, or
// not-evaluated with a reason (docs/specs/custom-lint.md, suppression
// states). Span is the directive's comment; Line is the 1-based line it
// covers. Range is the directive's LSP range when its span lies in the
// snapshot's text.
type SuppressionStatus struct {
	Rule   string           `json:"rule"`
	Status string           `json:"status"`
	Reason string           `json:"reason,omitempty"`
	File   string           `json:"file,omitempty"`
	Span   Span             `json:"span"`
	Line   int              `json:"line"`
	Range  *DiagnosticRange `json:"range,omitempty"`
}

// LintAdvice is what a lint run adds to a diagnostic report. Complete is
// false when a rule did not complete (a lint-runner L000 error says which).
type LintAdvice struct {
	Findings     []LintFinding
	Suppressions []SuppressionStatus
	Complete     bool
}

// lspSeverity is the LSP DiagnosticSeverity of a severity name.
func lspSeverity(severity string) int {
	switch severity {
	case "warning":
		return 2
	case "information":
		return 3
	case "hint":
		return 4
	}
	return 1
}

// ReportWithLint is Report with lint advice merged after the compiler's
// findings: each lint finding keeps its configured severity and names its
// rule, and the counts and policy cover both. The policy fails on any
// finding of severity error, on a warning when strict, and on an
// incomplete lint run. A nil advice is Report.
func (r *Result) ReportWithLint(snapshot SourceSnapshot, strict bool, advice *LintAdvice) DiagnosticReport {
	report := r.Report(snapshot, strict)
	if advice == nil {
		return report
	}
	report.LintComplete = advice.Complete
	project := projector(snapshot)
	var lint []DiagnosticFinding
	for _, f := range advice.Findings {
		finding := DiagnosticFinding{Code: f.Code, Rule: f.Rule, File: f.File, Origin: "lint", Severity: f.Severity, Message: f.Message, Span: f.Span, Fix: f.Fix, Related: f.Related, Suggestions: f.Suggestions}
		lsp := &LSPDiagnostic{Severity: lspSeverity(f.Severity), Code: f.Code, Source: "ergo lint", Message: f.Message}
		if location, ok := project(f.File, f.Span); ok {
			finding.LocationAvailable = true
			lsp.Range = location
		}
		finding.LSP = lsp
		lint = append(lint, finding)
	}
	sort.SliceStable(lint, func(i, j int) bool { return findingLess(lint[i], lint[j]) })
	report.Diagnostics = append(report.Diagnostics, lint...)
	for _, s := range advice.Suppressions {
		if location, ok := project(s.File, s.Span); ok {
			s.Range = &location
		}
		report.Suppressions = append(report.Suppressions, s)
	}
	report.TotalCounts = DiagnosticCounts{}
	for _, f := range report.Diagnostics {
		switch f.Severity {
		case "warning":
			report.TotalCounts.Warnings++
		case "information":
			report.TotalCounts.Information++
		case "hint":
			report.TotalCounts.Hints++
		default:
			report.TotalCounts.Errors++
		}
	}
	report.ReturnedCount = len(report.Diagnostics)
	report.PolicyPassed = r.Checked && advice.Complete && report.TotalCounts.Errors == 0 && (!strict || report.TotalCounts.Warnings == 0)
	return report
}

// projector maps a file's span to its LSP range on the snapshot, as Report
// projects a compiler diagnostic.
func projector(snapshot SourceSnapshot) func(file string, span Span) (DiagnosticRange, bool) {
	indexes := map[string]sourcePositionIndex{}
	return func(file string, span Span) (DiagnosticRange, bool) {
		text := snapshot.Text
		if file != "" {
			known := false
			text, known = snapshot.Files[file]
			if !known {
				return DiagnosticRange{}, false
			}
		}
		index, ok := indexes[file]
		if !ok {
			index = newSourcePositionIndex(text)
			indexes[file] = index
		}
		if span == (Span{}) {
			return DiagnosticRange{}, false
		}
		return index.rangeFor(text, span)
	}
}
