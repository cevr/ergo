package lint

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
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
			precision := PrecisionOf(rule.Name)
			for _, f := range found {
				f.Code, f.Rule, f.Severity, f.Precision = rule.Code, rule.Name, severity, precision
				if f.Instead == "" {
					f.Instead = rule.Instead
				}
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
	packFindings, packStatuses, packsComplete := c.runPacks(in)
	findings = append(findings, packFindings...)
	res.Rules = append(res.Rules, packStatuses...)
	res.Complete = res.Complete && packsComplete
	statuses := map[string]*RuleStatus{}
	for i := range res.Rules {
		statuses[res.Rules[i].Rule] = &res.Rules[i]
	}
	suppressed := make([]bool, len(findings))
	for _, unit := range in.Units {
		if unit.Dependency {
			continue
		}
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
		if !c.packs.selects(d.namespace) {
			s.Status, s.Reason = NotEvaluated, ReasonPackNotSelected
			res.Suppressions = append(res.Suppressions, s)
			return ""
		}
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
	case d.form == formQualified && st.Status != StatusCompleted:
		s.Status, s.Reason = NotEvaluated, notEvaluatedReason(st)
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

// LineRange is lines From to To of a file, 1-based and inclusive.
type LineRange struct{ From, To int }

// Added is the lines a change added, by the file a finding names
// (Finding.File: relative to the module root, "" for a program of one
// source text).
type Added map[string][]LineRange

// ParseDiff reads the added lines of a unified diff with no context, as
// `git diff -U0 --no-prefix` prints it: each hunk's +start,count on the
// new side (count 1 when omitted, none when 0), under the file its +++
// line names. A deleted file (+++ /dev/null) adds nothing.
func ParseDiff(unified string) Added {
	added := Added{}
	file := ""
	for _, line := range strings.Split(unified, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ "):
			file = strings.TrimSuffix(line[len("+++ "):], "\t")
			if unquoted, err := strconv.Unquote(file); err == nil {
				file = unquoted
			}
			if file == "/dev/null" {
				file = ""
				continue
			}
			if _, ok := added[file]; !ok {
				added[file] = nil
			}
		case strings.HasPrefix(line, "@@ ") && file != "":
			fields := strings.Fields(line)
			if len(fields) < 3 || !strings.HasPrefix(fields[2], "+") {
				continue
			}
			startText, countText, hasCount := strings.Cut(fields[2][1:], ",")
			start, err := strconv.Atoi(startText)
			if err != nil {
				continue
			}
			count := 1
			if hasCount {
				if count, err = strconv.Atoi(countText); err != nil {
					continue
				}
			}
			if count > 0 {
				added[file] = append(added[file], LineRange{From: start, To: start + count - 1})
			}
		}
	}
	return added
}

// InDiff is the run scoped to a change: the findings whose span meets a
// line the change added, from the first line of the span to its last. A
// finding with no place (a lint-runner failure) stays: it says the run is
// incomplete, whatever changed. The rules' and the directives' statuses
// are the full run's.
func (r Result) InDiff(in Input, added Added) Result {
	files := map[string]*File{}
	for _, unit := range in.Units {
		for i := range unit.Files {
			if f := &unit.Files[i]; f.Syntax != nil && files[f.Rel] == nil {
				files[f.Rel] = f
			}
		}
	}
	out := r
	out.Findings = nil
	for _, f := range r.Findings {
		if f.Line == 0 {
			out.Findings = append(out.Findings, f)
			continue
		}
		last := f.Line
		if file := files[f.File]; file != nil && f.Length > 0 {
			last = file.Syntax.Line(syntax.Pos(f.Offset + f.Length - 1))
		}
		for _, lines := range added[f.File] {
			if lines.From <= last && f.Line <= lines.To {
				out.Findings = append(out.Findings, f)
				break
			}
		}
	}
	return out
}

// runRule runs one rule over every file of every checked package.
func runRule(rule *Rule, options any, in Input) (found []Finding, err error) {
	defer func() {
		if r := recover(); r != nil {
			found, err = nil, fmt.Errorf("panic: %v", r)
		}
	}()
	run := &ruleRun{in: in}
	for _, unit := range in.Units {
		if unit.Package == nil || unit.Package.Info == nil || unit.Dependency {
			continue
		}
		for _, file := range unit.Files {
			if file.Syntax == nil {
				continue
			}
			pass := &Pass{Package: unit.Package, Info: unit.Package.Info, File: file.Syntax, Options: options, rel: file.Rel, run: run}
			rule.Check(pass)
			if pass.bad != nil {
				return nil, pass.bad
			}
			found = append(found, pass.found...)
		}
	}
	return found, nil
}
