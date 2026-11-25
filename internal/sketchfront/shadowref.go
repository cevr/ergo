package sketchfront

import (
	"fmt"
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/runtime/v1/shadow"
)

// ShadowRefutation is a test build's rewrite shadow that found a law
// false at a site (shadow.Shadow): its ledger key and, when each
// parameter's type has one, the case in ergo.cases's text.
type ShadowRefutation struct {
	Law, Case string
}

// ParseShadowLine reads a line of the test program's standard error: a
// shadow's refutation line (shadow.RefutedPrefix), or ok false.
func ParseShadowLine(line string) (ShadowRefutation, bool) {
	rest, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), shadow.RefutedPrefix)
	if !ok {
		return ShadowRefutation{}, false
	}
	law, tail, ok := strings.Cut(rest, " refuted")
	if !ok || law == "" || strings.Contains(law, " ") {
		return ShadowRefutation{}, false
	}
	tail = strings.TrimPrefix(tail, " ")
	if tail != "" && (!strings.HasPrefix(tail, "(") || !strings.HasSuffix(tail, ")")) {
		return ShadowRefutation{}, false
	}
	return ShadowRefutation{Law: law, Case: tail}, true
}

// RecordRefutations writes each shadow refutation of an ergo test run
// into the module's ledger (LAWFFI r2 §3.6, "ergo test writes the
// refutation of §3.4"; runtime.md, Tests 10): the law's entry becomes
// `refuted by shadow` at the sources' digest, so the run build stops on it
// (a refuted entry is a refusal) and the next licence run keeps it until
// the sources change, and its case joins ergo.cases, which each later
// licence run takes first. A test failure is so never a fact the ledger
// does not see. A law the module does not declare (std's, or another
// module's) has its own ledger: it is named, and not written. It returns
// the count of entries and corpus lines changed.
func RecordRefutations(a Analysis, req BuildRequest, refs []ShadowRefutation) (int, error) {
	if len(refs) == 0 || req.Ledger == "" || a.pkg == nil {
		return 0, nil
	}
	names := ledgerNames(a)
	keys := foreignKeys(a, req.Inputs)
	laws := map[string]*check.Law{}
	for _, p := range a.modulePackages() {
		for _, law := range p.Laws {
			if law.TopRewrite() != nil {
				laws[lower.FuncLawEntry(law, names, "", keys).Key()] = law
				// The test build reads no ledger, so its shadow names a
				// binary's main package `main` (LedgerNames' default);
				// the run is this analysis's, so the name is its root's.
				laws[lower.FuncLawEntry(law, nil, "", keys).Key()] = law
			}
		}
	}
	text, err := ledgerText(req.Inputs, req.Ledger)
	if err != nil {
		return 0, err
	}
	entries, err := lower.ParseLedger(text)
	if err != nil {
		return 0, err
	}
	ctext, err := ledgerText(req.Inputs, corpusPath(req.Ledger))
	if err != nil {
		return 0, err
	}
	cases, propCases, err := parseCorpus(ctext)
	if err != nil {
		return 0, err
	}
	before := cases.formatWith(propCases)
	changed := 0
	for _, r := range refs {
		law, ok := laws[r.Law]
		if !ok {
			if req.Stderr != nil {
				fmt.Fprintf(req.Stderr, "ergo test: law %s is false at a site; its package is not this module's, so its own ledger holds it, and %s is not written\n", r.Law, lower.LedgerFile)
			}
			continue
		}
		e := lower.FuncLawEntry(law, names, "refuted", keys)
		e.By = "shadow"
		if prev, ok := entries[e.Key()]; !ok || prev.Line() != e.Line() {
			entries[e.Key()] = e
			changed++
			if req.Stderr != nil {
				fmt.Fprintf(req.Stderr, "%s: law %s refuted by a test's shadow\n", lower.LedgerFile, r.Law)
			}
		}
		if r.Case != "" && !slices.Contains(cases[e.Key()], r.Case) {
			cases[e.Key()] = append(cases[e.Key()], r.Case)
		}
	}
	if after := cases.formatWith(propCases); after != before {
		changed++
		if err := writeFileAtomic(corpusPath(req.Ledger), []byte(after)); err != nil {
			return 0, err
		}
	}
	if changed == 0 {
		return 0, nil
	}
	return changed, writeFileAtomic(req.Ledger, []byte(lower.FormatLedger(entries)))
}

// modulePackages are the analysed root's package and, for a program, each
// package of its own module it imports.
func (a Analysis) modulePackages() []*check.Package {
	out := []*check.Package{a.pkg}
	if a.program == nil {
		return out
	}
	for _, q := range a.program.imports {
		if q.pkg != nil && q.mod == nil && q.pkg != a.pkg {
			out = append(out, q.pkg)
		}
	}
	return out
}
