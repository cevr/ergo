// Package lint is Ergo's linter on the checker's result (LINT0;
// docs/specs/custom-lint.md). A rule reads checked facts: the resolved
// objects, types, rows and footprints of check.Info, and the syntax trees
// the checker resolved. It never matches an identifier by its spelling
// (internal/residue TestLintRulesReadNoSpelling). The runner owns what a
// rule does not: configured severities and presets, reasoned suppression
// (L001) and rule failure (L000).
//
// Lint runs only on checked source and is never part of check, build, run
// or test.
package lint

import (
	"encoding/json"
	"fmt"

	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Severity is a configured severity. Off is a rule that does not run.
type Severity uint8

// The severities, weakest first.
const (
	Off Severity = iota
	Hint
	Information
	Warning
	Error
)

var severityNames = [...]string{Off: "off", Hint: "hint", Information: "information", Warning: "warning", Error: "error"}

func (s Severity) String() string {
	if int(s) < len(severityNames) {
		return severityNames[s]
	}
	return fmt.Sprintf("severity(%d)", uint8(s))
}

// The runner's own codes. They are not rules: no configuration sets their
// severity and no directive suppresses them.
const (
	// CodeRunner is a lint-runner failure: a rule that panicked or reported
	// outside its file. The run is incomplete.
	CodeRunner = "L000"
	// CodeSuppression is an invalid suppression directive: malformed, an
	// unknown rule, a reserved namespace, or unused.
	CodeSuppression = "L001"
)

// Rule is one built-in rule: its unqualified name, its code (L1xx), its
// documentation, its default severity and its check. A pack rule (LINT3)
// joins the registry under namespace/name with the same metadata.
//
// SafeFix marks a rule whose fixes keep the program's behaviour, so
// `ergo fix --lint` applies them. Options decodes the rule's options (nil
// raw for none set: the defaults); a rule with no Options takes none.
type Rule struct {
	Name    string
	Code    string
	Doc     string
	Default Severity
	Check   func(*Pass)
	SafeFix bool
	Options func(raw json.RawMessage) (any, error)
}

// File is one source file of a package as the loader read it: Rel is its
// path relative to the module root ("" for one source text), Syntax its
// tree, nil when it did not parse.
type File struct {
	Rel    string
	Syntax *syntax.File
}

// Unit is one package of the program: its checked package (nil when the
// program does not check) and its files.
type Unit struct {
	Package *check.Package
	Files   []File
}

// Input is a program to lint: its packages, dependencies first. Checked
// is false when the program has any compiler diagnostic; then no rule
// runs.
type Input struct {
	Checked bool
	Units   []Unit
}

// Pass is one rule's run over one file of a checked package.
type Pass struct {
	Package *check.Package
	Info    *check.Info
	File    *syntax.File
	// Options are the rule's decoded options (Rule.Options), nil for a
	// rule that takes none.
	Options any
	rel     string
	found   []Finding
	bad     error
}

// Rel is the file's path relative to the module root, "" for a program of
// one source text.
func (p *Pass) Rel() string { return p.rel }

// Report records a finding of the rule from from to to (byte offsets in
// the pass's file).
func (p *Pass) Report(from, to syntax.Pos, message string) { p.ReportFix(from, to, message, nil) }

// ReportFix is Report with the edit that fixes the finding (EX1's shape;
// its offsets are in the pass's file). The runner names the rule on it.
func (p *Pass) ReportFix(from, to syntax.Pos, message string, fix *report.Fix) {
	if fix != nil && (fix.InsertAt < 0 || fix.Replace < 0 || fix.InsertAt+fix.Replace > len(p.File.Src)) {
		if p.bad == nil {
			p.bad = fmt.Errorf("a fix edits %d..%d outside its file of %d bytes", fix.InsertAt, fix.InsertAt+fix.Replace, len(p.File.Src))
		}
		return
	}
	if from < 0 || to < from || int(to) > len(p.File.Src) {
		if p.bad == nil {
			p.bad = fmt.Errorf("reported %d..%d outside its file of %d bytes", from, to, len(p.File.Src))
		}
		return
	}
	line, column := p.File.Position(from)
	p.found = append(p.found, Finding{File: p.rel, Offset: int(from), Length: int(to - from), Line: line, Column: column, Message: message, Fix: fix})
}

// Finding is one lint finding. Rule is empty for the runner's own codes.
type Finding struct {
	Code     string
	Rule     string
	Severity Severity
	Message  string
	File     string
	Offset   int
	Length   int
	Line     int
	Column   int
	// Fix is the finding's edit, nil when the rule offers none.
	Fix *report.Fix
}

// Rule statuses.
const (
	StatusCompleted    = "completed"
	StatusOff          = "off"
	StatusFailed       = "failed"
	StatusNotEvaluated = "not-evaluated"
)

// Not-evaluated reasons (custom-lint.md, suppression states).
const (
	ReasonUnchecked        = "unchecked-source"
	ReasonRuleOff          = "rule-off"
	ReasonPackNotSelected  = "pack-not-selected"
	ReasonFactsUnavailable = "facts-unavailable"
)

// RuleStatus is what became of one rule in a run.
type RuleStatus struct {
	Rule     string `json:"rule"`
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
}

// Suppression statuses.
const (
	Applied      = "applied"
	Unused       = "unused"
	NotEvaluated = "not-evaluated"
)

// Suppression is the status of one admitted directive: its rule, status,
// reason (for not-evaluated), the directive comment's place and the line
// it covers.
type Suppression struct {
	Rule    string
	Status  string
	Reason  string
	File    string
	Offset  int
	Length  int
	Line    int
	Column  int
	Covered int
}

// Result is one lint run: the findings that remain after suppression (with
// L000 and L001), every rule's status, every admitted directive's status,
// and Complete, true when every enabled rule completed on checked source.
type Result struct {
	Checked      bool
	Findings     []Finding
	Rules        []RuleStatus
	Suppressions []Suppression
	Complete     bool
}

// PolicyPassed reports whether the run passes: checked, complete, no
// finding of severity error and, when strict, none of severity warning.
func (r Result) PolicyPassed(strict bool) bool {
	if !r.Checked || !r.Complete {
		return false
	}
	for _, f := range r.Findings {
		if f.Severity == Error || (strict && f.Severity == Warning) {
			return false
		}
	}
	return true
}
