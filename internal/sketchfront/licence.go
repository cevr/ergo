package sketchfront

import (
	"fmt"
	"io"

	"ergo.local/prototype/internal/report"
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
	for _, law := range a.pkg.Laws {
		if law.FuncRewrite == nil || law.Pkg != a.pkg {
			continue
		}
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
// per class, goes to w, and whose verdict the entry holds, by eval.
func licenceEntries(pkg *check.Package, names lower.LedgerNames, old map[string]lower.LedgerEntry, keys lower.ForeignKeys, w io.Writer) ([]lower.LedgerEntry, error) {
	var out []lower.LedgerEntry
	for _, law := range pkg.Laws {
		if law.FuncRewrite == nil {
			continue
		}
		stub := lower.FuncLawEntry(law, names, "", keys)
		if prev, ok := old[stub.Key()]; ok && prev.Digest == stub.Digest {
			out = append(out, prev)
			continue
		}
		l, err := eval.LicenceRun(pkg, law, eval.LicenceBudget())
		if err != nil {
			return nil, err
		}
		if w != nil {
			fmt.Fprintf(w, "%s (%s)\n", l.Line(), pkg.Path)
		}
		out = append(out, lower.FuncLawEntry(law, names, l.Verdict, keys))
	}
	return out, nil
}
