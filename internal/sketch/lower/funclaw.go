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

// AdmitFunc admits the top-level rewrite law law at a site: the law is in
// the rewrite's scope (Law.FuncRewrite), its ledger entry is current (its
// digest is FuncLawDigest's for the sources and the foreign keys now) and
// admitLevel admits its level for the value class. Else it says why. A
// current refuted entry is recorded as a refusal, which stops the build.
func (e *LedgerEvidence) AdmitFunc(law *check.Law) (bool, string) {
	key := e.names.funcLaw(law)
	if law.FuncRewrite == nil {
		return false, "law " + key + " is not in the scope of a rewrite"
	}
	entry, found := e.entries[key]
	digest, with := FuncLawDigest(law, e.keys)
	if found {
		e.read[key] = true
	}
	switch {
	case !found:
		return false, "ergo.laws: " + key + " is missing; run ergo test"
	case digest != entry.Digest:
		if entry.With != with {
			return false, "ergo.laws: " + key + " was " + entry.Verdict + " with " + orNone(entry.With) + ", and the sources now give " + orNone(with) + "; run ergo test"
		}
		return false, "ergo.laws: " + key + " is stale; run ergo test"
	case entry.Verdict == "refuted":
		why := "law " + key + " is refuted"
		e.refused[fmt.Sprintf("ergo.laws: law %s is refuted, and the sources are those it was refuted on: the build stops; correct the law or what it reaches, then run ergo test", key)] = true
		return false, why
	case !admitLevel(entry.Verdict, entry.By, lawRewriteClass):
		return false, "law " + key + " is " + entry.Verdict + ", and only a proved law or one validated by a named run changes code"
	}
	return true, ""
}
