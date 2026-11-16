package lower

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"sort"

	"ergo.local/prototype/internal/sketch/check"
)

// The ledger side of a top-level rewrite law over functions (LAWFFI LF6a;
// owner q195: "the validation key is the content hash of every body both
// sides of the law reach … not a module version"). The law's entry is the
// line `law <pkg>.<Law> <verdict> [by <run>] [with <keys>] h1:<digest>`;
// LF6b's licence run writes it, LF6c's engine asks AdmitFunc at each site.

// FuncLawDigest is the digest of the evidence of the top-level law law:
// the tokens of its declaration and of every declaration either side
// reaches, transitively (the functions it calls, the functions those call,
// the types and package lets they name), and the foreign key of each Go
// function they reach (an `import go` function too: its binding is an
// `extern go` target). So an edit of any body the law reaches makes the entry
// stale, and an edit of one it does not reach leaves it current. with is
// the keys' shown forms.
func FuncLawDigest(law *check.Law, keys ForeignKeys) (digest, with string) {
	d := newDigester()
	d.decl("law "+law.Pkg.Path+"."+law.Name, law.Pkg, law.Decl)
	decls := make([]string, 0, len(d.decls))
	for k := range d.decls {
		decls = append(decls, k)
	}
	sort.Strings(decls)
	h := sha256.New()
	fmt.Fprintf(h, "%d:%s", len(LawRunVersion), LawRunVersion)
	for _, k := range decls {
		fmt.Fprintf(h, "%d:%s", len(k), k)
	}
	with = d.foreignKeys(h, keys)
	return "h1:" + base64.StdEncoding.EncodeToString(h.Sum(nil)), with
}

// funcLaw is a top-level law's entry name: its package's module-wide
// name and its own.
func (n LedgerNames) funcLaw(law *check.Law) string {
	return n.Of(law.Pkg) + "." + law.Name
}

// FuncLawEntry is the ledger entry of the top-level law law with verdict,
// the licence run's (LF6b): by eval (the check-time evaluator) for every
// verdict but skipped, which names no run, and the digest and foreign
// keys of FuncLawDigest. The entry has no instance: a refutation's
// arguments are ergo test's output, not the ledger's, since a line's
// `at (…)` is a site's constants and a key part, and a value may be
// private.
func FuncLawEntry(law *check.Law, names LedgerNames, verdict string, keys ForeignKeys) LedgerEntry {
	d, with := FuncLawDigest(law, keys)
	by := "eval"
	if verdict == "skipped" {
		by = ""
	}
	return LedgerEntry{Law: names.funcLaw(law), Verdict: verdict, By: by, With: with, Digest: d}
}

// AdmitFunc admits the top-level rewrite law law at a site: the law is in
// the rewrite's scope (Law.FuncRewrite), its ledger entry is current (its
// digest is FuncLawDigest's for the sources and the foreign keys now) and
// admitLevel admits its level for the value class. Else it says why. A
// current refuted entry is recorded as a refusal, which stops the build.
func (e *LedgerEvidence) AdmitFunc(law *check.Law) (bool, string) {
	v := e.funcVerdict(law)
	return v.OK, v.Why
}

// funcVerdict is what evidence says of a top-level rewrite law: its entry
// name, the entry's verdict, run and foreign keys, its state (current,
// stale or missing), and whether a site may use it, else why not.
type funcVerdict struct {
	Key, Verdict, By, With, State string
	OK                            bool
	Why                           string
}

// funcEvidence is evidence that answers for top-level rewrite laws: the
// build's ledger (LedgerEvidence) or the shipped one (shippedEvidence).
type funcEvidence interface {
	funcVerdict(law *check.Law) funcVerdict
}

func (e *LedgerEvidence) funcVerdict(law *check.Law) funcVerdict {
	key := e.names.funcLaw(law)
	if law.FuncRewrite == nil {
		return funcVerdict{Key: key, Why: "law " + key + " is not in the scope of a rewrite"}
	}
	entry, found := e.entries[key]
	digest, with := FuncLawDigest(law, e.keys)
	if found {
		e.read[key] = true
	}
	return judgeFunc(key, entry, found, digest, with, "ergo.laws: ", func() {
		e.refused[fmt.Sprintf("ergo.laws: law %s is refuted, and the sources are those it was refuted on: the build stops; correct the law or what it reaches, then run ergo test", key)] = true
	})
}

// funcVerdict of a law of a shipped package: its entry in the shipped
// ledger, against the shipped foreign keys. A refuted shipped entry
// refuses the rewrite and stops nothing: the compiler shipped it.
func (e *shippedEvidence) funcVerdict(law *check.Law) funcVerdict {
	key := LedgerNames(nil).funcLaw(law)
	if law.FuncRewrite == nil {
		return funcVerdict{Key: key, Why: "law " + key + " is not in the scope of a rewrite"}
	}
	entry, found := e.entries[key]
	d, with := FuncLawDigest(law, ShippedForeignKeys)
	return judgeFunc(key, entry, found, d, with, "the std law ledger: ", func() {})
}

// judgeFunc is the one judgement of a top-level law's entry: missing,
// stale, refuted (refuse is called), a level admitLevel refuses, or
// admitted.
func judgeFunc(key string, entry LedgerEntry, found bool, digest, with, ledger string, refuse func()) funcVerdict {
	v := funcVerdict{Key: key, Verdict: entry.Verdict, By: entry.By, With: with, State: "current"}
	switch {
	case !found:
		v.State, v.Why = "missing", ledger+key+" is missing; run ergo test"
	case digest != entry.Digest:
		v.State, v.Why = "stale", ledger+key+" is stale; run ergo test"
		if entry.With != with {
			v.Why = ledger + key + " was " + entry.Verdict + " with " + orNone(entry.With) + ", and the sources now give " + orNone(with) + "; run ergo test"
		}
	case entry.Verdict == "refuted":
		v.Why = "law " + key + " is refuted"
		refuse()
	case !admitLevel(entry.Verdict, entry.By, lawRewriteClass):
		v.Why = "law " + key + " is " + entry.Verdict + ", and only a proved law or one validated by a named run changes code"
	default:
		v.OK = true
	}
	return v
}

// funcSite records the row of a rewrite site of a top-level law at
// file:line:col (ergo inspect rewrites), or replaces it: a lowering that
// runs again from the start (lowerAgain) meets the site again. It returns
// the row's key.
func (e *LedgerEvidence) funcSite(file string, line, col int, v funcVerdict, applied bool, why string) string {
	site := fmt.Sprintf("%s:%d:%d %s", file, line, col, v.Key)
	if e.sites[site] == nil {
		e.order = append(e.order, site)
		if v.State != "current" {
			e.notes[site] = v.Why
		}
	}
	if v.State != "current" {
		why = ""
	}
	e.sites[site] = &RewriteSite{File: file, Line: line, Column: col, Law: v.Key, Verdict: v.Verdict, By: v.By, With: v.With, State: v.State, Applied: applied, Why: why}
	return site
}

// funcUnapplied marks the rows sites as not applied, for why: the body
// that holds them kept its source body.
func (e *LedgerEvidence) funcUnapplied(sites []string, why string) {
	for _, s := range sites {
		if r := e.sites[s]; r != nil && r.Applied {
			r.Applied, r.Why = false, why
		}
	}
}
