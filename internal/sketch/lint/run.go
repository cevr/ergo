package lint

import (
	"fmt"
	"slices"
	"strings"
)

// Run lints the program: every enabled rule over every file of every
// checked package, then the suppression directives. A rule that panics or
// reports outside its file fails alone: its findings are dropped, one L000
// error names it, and the run is incomplete. On unchecked source no rule
// runs, and each known directive is not-evaluated: unchecked-source.
func (c *Configuration) Run(in Input) Result {
	res := Result{Checked: in.Checked, Complete: in.Checked}
	var findings []Finding
	for _, rule := range c.registry.rules {
		severity := c.severity[rule.Name]
		st := RuleStatus{Rule: rule.Name, Code: rule.Code, Severity: severity.String()}
		switch {
		case severity == Off:
			st.Status = StatusOff
		case !in.Checked:
			st.Status, st.Reason = StatusNotEvaluated, ReasonUnchecked
		default:
			options, err := c.optionsOf(rule)
			var found []Finding
			if err == nil {
				found, err = runRule(rule, options, in)
			}
			if err != nil {
				st.Status = StatusFailed
				res.Complete = false
				findings = append(findings, Finding{Code: CodeRunner, Severity: Error, Message: fmt.Sprintf("lint-runner: rule %s did not complete: %v", rule.Name, err)})
				break
			}
			st.Status = StatusCompleted
			for _, f := range found {
				f.Code, f.Rule, f.Severity = rule.Code, rule.Name, severity
				if f.Fix != nil {
					fix := *f.Fix
					fix.Rule = rule.Name
					f.Fix = &fix
				}
				findings = append(findings, f)
			}
		}
		res.Rules = append(res.Rules, st)
	}
	statuses := map[string]*RuleStatus{}
	for i := range res.Rules {
		statuses[res.Rules[i].Rule] = &res.Rules[i]
	}
	suppressed := make([]bool, len(findings))
	for _, unit := range in.Units {
		for _, file := range unit.Files {
			for _, d := range directivesOf(file) {
				if problem := c.suppress(d, in.Checked, statuses, findings, suppressed, &res); problem != "" {
					findings = append(findings, Finding{Code: CodeSuppression, Severity: Error, Message: problem, File: d.file, Offset: int(d.at), Length: d.length, Line: d.line, Column: d.column})
					suppressed = append(suppressed, false)
				}
			}
		}
	}
	for i, f := range findings {
		if !suppressed[i] {
			res.Findings = append(res.Findings, f)
		}
	}
	slices.SortStableFunc(res.Findings, func(a, b Finding) int {
		if a.File != b.File {
			return strings.Compare(a.File, b.File)
		}
		if a.Offset != b.Offset {
			return a.Offset - b.Offset
		}
		if a.Length != b.Length {
			return a.Length - b.Length
		}
		if a.Code != b.Code {
			return strings.Compare(a.Code, b.Code)
		}
		return strings.Compare(a.Message, b.Message)
	})
	return res
}

// suppress decides one directive: it records the status of an admitted
// directive, marks the findings it removes, and returns the L001 message
// of an invalid or unused one.
func (c *Configuration) suppress(d directive, checked bool, statuses map[string]*RuleStatus, findings []Finding, suppressed []bool, res *Result) string {
	s := Suppression{Rule: d.rule, File: d.file, Offset: int(d.at), Length: d.length, Line: d.line, Column: d.column, Covered: d.line + 1}
	switch d.form {
	case formMalformed:
		return "malformed suppression; write // " + DirectiveWord + " RULE -- REASON, with a reason"
	case formQualified:
		if reservedNamespace(d.namespace) {
			return fmt.Sprintf("unknown lint rule %s: the namespace %s is reserved", d.rule, d.namespace)
		}
		s.Status, s.Reason = NotEvaluated, ReasonPackNotSelected
		res.Suppressions = append(res.Suppressions, s)
		return ""
	}
	st := statuses[d.rule]
	if st == nil {
		return fmt.Sprintf("unknown lint rule %s in a suppression", d.rule)
	}
	switch {
	case !checked:
		s.Status, s.Reason = NotEvaluated, ReasonUnchecked
	case st.Status == StatusOff:
		s.Status, s.Reason = NotEvaluated, ReasonRuleOff
	case st.Status == StatusFailed:
		s.Status, s.Reason = NotEvaluated, ReasonFactsUnavailable
	default:
		s.Status = Unused
		for i, f := range findings {
			if f.Rule == d.rule && f.File == d.file && f.Line == s.Covered {
				suppressed[i] = true
				s.Status = Applied
			}
		}
	}
	res.Suppressions = append(res.Suppressions, s)
	if s.Status == Unused {
		return fmt.Sprintf("unused suppression: %s reports nothing on line %d", d.rule, s.Covered)
	}
	return ""
}

// Fixable reports a finding `ergo fix --lint` applies: it has a fix and its
// rule is enabled and marks its fixes safe (Rule.SafeFix).
func (c *Configuration) Fixable(f Finding) bool {
	rule := c.registry.byName[f.Rule]
	return f.Fix != nil && rule != nil && rule.SafeFix && c.severity[f.Rule] != Off
}

// runRule runs one rule over every file of every checked package.
func runRule(rule *Rule, options any, in Input) (found []Finding, err error) {
	defer func() {
		if r := recover(); r != nil {
			found, err = nil, fmt.Errorf("panic: %v", r)
		}
	}()
	for _, unit := range in.Units {
		if unit.Package == nil || unit.Package.Info == nil {
			continue
		}
		for _, file := range unit.Files {
			if file.Syntax == nil {
				continue
			}
			pass := &Pass{Package: unit.Package, Info: unit.Package.Info, File: file.Syntax, Options: options, rel: file.Rel}
			rule.Check(pass)
			if pass.bad != nil {
				return nil, pass.bad
			}
			found = append(found, pass.found...)
		}
	}
	return found, nil
}
