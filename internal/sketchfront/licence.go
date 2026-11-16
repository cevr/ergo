package sketchfront

import (
	"fmt"
	"io"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lower"
)

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
