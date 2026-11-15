package lint

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	sdk "ergo.local/prototype/lint"
)

// Expectation is what a source fixture expects of the selected rule packs
// (`ergo lint test`, docs/specs/custom-lint.md): whether the run was
// complete, the status of every pack rule, the pack findings and any
// lint-runner errors, and the status of every suppression that names a
// namespace/rule. Built-in advice is outside it, so a fixture tests its
// packs, not the compiler's own rules.
type Expectation struct {
	Complete     bool                  `json:"complete"`
	Rules        []RuleStatus          `json:"rules"`
	Findings     []ExpectedFinding     `json:"findings"`
	Suppressions []ExpectedSuppression `json:"suppressions,omitempty"`
}

// ExpectedFinding is one pack finding or lint-runner error of a fixture.
type ExpectedFinding struct {
	Code        string           `json:"code"`
	Severity    string           `json:"severity"`
	Message     string           `json:"message"`
	Span        *sdk.Span        `json:"span,omitempty"`
	Related     []sdk.Related    `json:"related,omitempty"`
	Suggestions []sdk.Suggestion `json:"suggestions,omitempty"`
}

// ExpectedSuppression is the status of one namespace/rule directive.
type ExpectedSuppression struct {
	Rule   string `json:"rule"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Line   int    `json:"line"`
}

// ExpectationOf is the part of a run a fixture expectation holds.
func ExpectationOf(r Result) Expectation {
	e := Expectation{Complete: r.Complete, Rules: []RuleStatus{}, Findings: []ExpectedFinding{}}
	for _, st := range r.Rules {
		if qualified(st.Rule) {
			e.Rules = append(e.Rules, st)
		}
	}
	for _, f := range r.Findings {
		if !qualified(f.Rule) && f.Code != CodeRunner {
			continue
		}
		out := ExpectedFinding{Code: f.Code, Severity: f.Severity.String(), Message: f.Message, Related: f.Related, Suggestions: f.Suggestions}
		if f.Line > 0 {
			out.Span = &sdk.Span{File: f.File, Offset: f.Offset, Length: f.Length, Line: f.Line, Column: f.Column}
		}
		e.Findings = append(e.Findings, out)
	}
	for _, s := range r.Suppressions {
		if qualified(s.Rule) {
			e.Suppressions = append(e.Suppressions, ExpectedSuppression{Rule: s.Rule, Status: s.Status, Reason: s.Reason, Line: s.Covered})
		}
	}
	return e
}

// Encode is the expectation's file form: indented JSON and a newline.
func (e Expectation) Encode() []byte {
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(data, '\n')
}

// ParseExpectation decodes an expectation file strictly: unknown fields,
// and any data after its one JSON object, are refused.
func ParseExpectation(data []byte) (Expectation, error) {
	var e Expectation
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		return Expectation{}, err
	}
	var rest json.RawMessage
	if err := dec.Decode(&rest); !errors.Is(err, io.EOF) {
		return Expectation{}, fmt.Errorf("data follows the expectation")
	}
	return e, nil
}

// Diff compares two expectations by content: "" when equal, else both in
// their file form.
func (e Expectation) Diff(got Expectation) string {
	want, have := e.Encode(), got.Encode()
	if bytes.Equal(want, have) {
		return ""
	}
	return "want:\n" + strings.TrimSpace(string(want)) + "\ngot:\n" + strings.TrimSpace(string(have))
}
