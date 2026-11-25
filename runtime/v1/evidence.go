package v1

import "sync"

// CaseEvidence is structured bounded-testing evidence submitted by a test or
// law case during execution. It may downgrade a finite law's "proved" verdict
// to "tested" (or fail an incomplete campaign); it never promotes proof.
type CaseEvidence struct {
	Active     bool
	Tested     int
	Matched    int
	Planned    int
	Scope      string
	Summary    string
	Complete   bool
	Refuted    bool
	Incomplete bool
	Reason     string
}

var caseEvState struct {
	mu sync.Mutex
	ev CaseEvidence
}

// RecordCaseEvidence submits structured bounded-test evidence for the active
// test/law case. Multiple submissions in one case accumulate counts and keep
// Complete true only when every submission was complete.
//
//ergo:wait none
func RecordCaseEvidence(tested, matched, planned int, scope, summary string, complete, refuted bool, reason string) {
	caseEvState.mu.Lock()
	defer caseEvState.mu.Unlock()
	incomplete := !complete && !refuted
	if !caseEvState.ev.Active {
		caseEvState.ev = CaseEvidence{
			Active:     true,
			Tested:     tested,
			Matched:    matched,
			Planned:    planned,
			Scope:      scope,
			Summary:    summary,
			Complete:   complete,
			Refuted:    refuted,
			Incomplete: incomplete,
			Reason:     reason,
		}
		return
	}
	caseEvState.ev.Tested += tested
	caseEvState.ev.Matched += matched
	caseEvState.ev.Planned += planned
	if caseEvState.ev.Scope == "" {
		caseEvState.ev.Scope = scope
	} else if scope != "" && scope != caseEvState.ev.Scope {
		caseEvState.ev.Scope += "; " + scope
	}
	if caseEvState.ev.Summary == "" {
		caseEvState.ev.Summary = summary
	} else if summary != "" {
		caseEvState.ev.Summary += "; " + summary
	}
	if refuted {
		caseEvState.ev.Refuted = true
		caseEvState.ev.Complete = false
		if caseEvState.ev.Reason == "" {
			caseEvState.ev.Reason = reason
		}
	} else if incomplete {
		caseEvState.ev.Incomplete = true
		caseEvState.ev.Complete = false
		if caseEvState.ev.Reason == "" {
			caseEvState.ev.Reason = reason
		}
	}
}

// CaseEvidenceReset clears any recorded case evidence before a new test/law
// case begins.
//
//ergo:wait none
func CaseEvidenceReset() {
	caseEvState.mu.Lock()
	caseEvState.ev = CaseEvidence{}
	caseEvState.mu.Unlock()
}

// CaseEvidenceTake returns and clears the structured evidence recorded during
// the just-finished test/law case.
//
//ergo:wait none
func CaseEvidenceTake() CaseEvidence {
	caseEvState.mu.Lock()
	ev := caseEvState.ev
	caseEvState.ev = CaseEvidence{}
	caseEvState.mu.Unlock()
	return ev
}
