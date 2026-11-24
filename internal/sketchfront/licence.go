package sketchfront

import (
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lower"
)

// AdviseLicences gives ergo check's advice (LAWFFI r2 §3.6, Report; LF6c):
// once, on its law's obligation in e, for each top-level rewrite law in
// scope that the root package declares and whose ledger entry, in the
// ledger at path read through fsys, is not current and validated. Such a
// law is checked and run as the property it states, and changes no code
// until ergo test's licence run validates it; the advice says which of
// that is missing. A law with a current validated entry has none.
func (a Analysis) AdviseLicences(e *report.Evaluation, fsys FS, path string) error {
	if a.pkg == nil || e == nil {
		return nil
	}
	text, err := ledgerText(fsys, path)
	if err != nil {
		return err
	}
	entries, err := lower.ParseLedger(text)
	if err != nil {
		return err
	}
	names, keys := ledgerNames(a), foreignKeys(a, fsys)
	for _, law := range a.pkg.FunctionRewrites() {
		want := lower.FuncLawEntry(law, names, "", keys)
		prev, found := entries[want.Key()]
		var state string
		switch {
		case !found:
			state = "ergo.laws has no entry for it"
		case prev.Digest != want.Digest:
			state = "its ergo.laws entry is stale: a body the law reaches, or a Go function's key, changed since it was " + prev.Verdict
		case prev.Verdict == "validated" && prev.By != "", prev.Verdict == "proved":
			continue
		default:
			state = "its ergo.laws entry is " + prev.Verdict
		}
		advice := "law " + law.Name + " is a rewrite in scope, and " + state + ": it changes no code until ergo test's licence run validates it"
		for i := range e.Obligations {
			if e.Obligations[i].Name == "law "+law.Name {
				e.Obligations[i].Advice = advice
				break
			}
		}
	}
	return nil
}

// licenceEntries are the ledger entries of the top-level rewrite laws in
// scope that pkg declares (check.FuncRewrite; LAWFFI LF6b): an entry of
// old whose digest is current stays as it is, and every other law takes
// the licence run (eval.LicenceRun), whose report line, with its count
// per class, goes to w, and whose verdict the entry holds, by eval. The
// run takes the law's corpus cases first (cases, by the entry's key), and
// a refuted run's case joins them (the corpus file, ergo.cases). A law
// over the module's own Go bodies calls them through their driver
// (LAWFFI LF2m; gobodydriver.go), built with tc at the first law that
// runs; a zero tc builds none, and such a law is skipped.
//
// A skipped run is never a verdict (AC10, LICENCE-ADT): each is in
// skipped, with its reason and the fix, and the caller fails on it
// (LicenceSkippedError). A current skipped entry runs again, so its
// reason is this run's.
func licenceEntries(pkg *check.Package, names lower.LedgerNames, old map[string]lower.LedgerEntry, keys lower.ForeignKeys, w io.Writer, cases corpus, tc build.Toolchain) (out []lower.LedgerEntry, skipped []SkippedLicence, err error) {
	var driver *goBodyDriver
	var refuted []string
	started := false
	defer func() { driver.Close() }()
	for _, law := range pkg.FunctionRewrites() {
		stub := lower.FuncLawEntry(law, names, "", keys)
		if prev, ok := old[stub.Key()]; ok && prev.Digest == stub.Digest && prev.Verdict != "skipped" {
			out = append(out, prev)
			if p := law.ClaimRewriteBinding(); p != nil && prev.Verdict == "refuted" {
				refuted = append(refuted, "licence law "+stub.Key()+": refuted"+p.ClaimBlame())
			}
			continue
		}
		if !started && tc.Go != "" {
			started = true
			d, err := startGoBodies(pkg, tc, w)
			if err != nil {
				return nil, nil, err
			}
			driver = d
		}
		var goBodies eval.GoBodies
		if driver != nil {
			goBodies = driver
		}
		l, err := eval.LicenceRun(pkg, law, eval.LicenceBudget(), cases[stub.Key()], goBodies)
		if err != nil {
			return nil, nil, err
		}
		if w != nil {
			fmt.Fprintf(w, "%s (%s)\n", l.Line(), pkg.Path)
		}
		if l.Verdict == "refuted" && l.Case != "" && cases != nil && !slices.Contains(cases[stub.Key()], l.Case) {
			cases[stub.Key()] = append(cases[stub.Key()], l.Case)
		}
		if l.Verdict == "refuted" && law.ClaimRewriteBinding() != nil {
			refuted = append(refuted, l.Line())
		}
		if l.Verdict == "skipped" {
			skipped = append(skipped, SkippedLicence{Law: stub.Key(), Package: pkg.Path, Reason: l.Reason})
		}
		out = append(out, lower.FuncLawEntry(law, names, l.Verdict, keys))
	}
	if len(refuted) != 0 {
		return out, skipped, &ClaimLicenceRefutedError{Lines: refuted}
	}
	return out, skipped, nil
}

// ClaimLicenceRefutedError follows the committed ledger write. The
// declaration's property report cannot hide a refuted claimant equation.
type ClaimLicenceRefutedError struct{ Lines []string }

func (e *ClaimLicenceRefutedError) Error() string { return strings.Join(e.Lines, "\n") }

// SkippedLicence is a rewrite law whose licence run did not run (AC10):
// a rewrite law in scope must have a verdict, so its test fails.
type SkippedLicence struct {
	Law, Package, Reason string
}

// Line is the failure line ergo test prints for it: the law, the run's
// reason, and the fix that reason names.
func (s SkippedLicence) Line() string {
	line := "FAIL licence law " + s.Law + ": skipped: " + s.Reason
	if fix := licenceFix(s.Reason); fix != "" {
		line += "; " + fix
	}
	return line
}

// licenceFix is the fix for a licence run's skip reason: what the run
// lacks, and how the law or its module provides it (types.md, Laws 14).
func licenceFix(reason string) string {
	switch {
	case strings.Contains(reason, "no family of pure functions"):
		return "state the law over functions whose parameters and result are int or bool, the families the run draws"
	case strings.Contains(reason, "a generator for") || strings.Contains(reason, "no generator"):
		return "declare a constructor New<T> for the type the run cannot draw, or state the law at a type it draws"
	case strings.Contains(reason, "a Go function"):
		return "the run calls Go bodies only through the go tool: ergo test of a binary that imports the package builds them"
	}
	return ""
}

// LicenceSkippedError is a ledger write that left a rewrite law with no
// verdict (AC10): the ledger is written, and the test fails.
type LicenceSkippedError struct {
	Skipped []SkippedLicence
}

func (e *LicenceSkippedError) Error() string {
	lines := make([]string, len(e.Skipped))
	for i, s := range e.Skipped {
		lines[i] = s.Line()
	}
	return strings.Join(lines, "\n")
}

// CorpusFile is the licence runs' corpus, beside ergo.laws: one line
// `law <pkg>.<Law> (<values>)` per refuted case, which each later licence
// run of the law takes first (LAWFFI r2 §3.4; eval's encodeCase). Like a
// Go fuzz corpus it is checked in, and ergo test only adds to it; a line
// goes when its package no longer declares the law.
const CorpusFile = "ergo.cases"

// corpus is a corpus file's lines by their law's key, in file order.
type corpus map[string][]string

func corpusPath(ledger string) string {
	if ledger == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(ledger), CorpusFile)
}

// parseCorpus reads a corpus file's text; a blank line or a # line is
// skipped, and any other line that is not `law <key> (…)` is an error.
func parseCorpus(text string) (corpus, error) {
	out := corpus{}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rest, ok := strings.CutPrefix(line, "law ")
		key, c, ok2 := strings.Cut(rest, " ")
		if !ok || !ok2 || !strings.HasPrefix(c, "(") {
			return nil, fmt.Errorf("%s:%d: not a corpus line: %q", CorpusFile, i+1, line)
		}
		out[key] = append(out[key], c)
	}
	return out, nil
}

// format is the corpus's text, its laws sorted and each law's cases in
// the order they were found.
func (c corpus) format() string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		for _, line := range c[k] {
			b.WriteString("law " + k + " " + line + "\n")
		}
	}
	return b.String()
}
