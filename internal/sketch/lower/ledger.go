package lower

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"slices"
	"sort"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The law ledger (runtime.md, Tests 10; plan-2 §3; owner q116, q122): the
// checked-in file ergo.laws beside ergo.mod, which holds one line per
// rewrite law and provider whose run ergo test checked:
//
//	law <pkg>.<Type>.<Law> for <pkg>.<Provider> <verdict> <how> h1:<digest>
//
// The verdict is proved, tested or skipped; how is `at (c, …) …`, the site
// instances the run took, sorted, when the entry has instances, else the
// run's own text. The digest (LawDigest) covers every input of the
// verdict, so the run build is a function of the sources and the ledger:
// a site applies a rewrite only on a current, proved entry that lists its
// constants (LedgerEvidence). ergo test writes the file (LedgerEntries);
// the run build only reads it.

// LedgerFile is the ledger's file name, beside ergo.mod.
const LedgerFile = "ergo.laws"

// LawRunVersion names the law runner and the digest's encoding: a change
// of either makes every entry stale.
const LawRunVersion = "ergo-laws/1"

// LedgerEntry is one line of a ledger.
type LedgerEntry struct {
	Law, Provider string
	Verdict       string
	// How is the run's text; Instances, when the entry has any, are its
	// site instances, sorted, and the line's how is `at` and them.
	How       string
	Instances []string
	Digest    string
}

// Key names the entry: its law and its provider.
func (e LedgerEntry) Key() string { return e.Law + " for " + e.Provider }

// Line is the entry's line in the file, with no line break.
func (e LedgerEntry) Line() string {
	how := e.How
	if len(e.Instances) > 0 {
		how = "at " + strings.Join(e.Instances, " ")
	}
	return "law " + e.Key() + " " + e.Verdict + " " + how + " " + e.Digest
}

// ParseLedger reads a ledger's text. A blank line and a line that starts
// with # are skipped; any other line that is not an entry is an error.
func ParseLedger(text string) (map[string]LedgerEntry, error) {
	out := map[string]LedgerEntry{}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		e, ok := parseEntry(line)
		if !ok {
			return nil, fmt.Errorf("%s:%d: not a ledger entry: %q", LedgerFile, i+1, line)
		}
		out[e.Key()] = e
	}
	return out, nil
}

func parseEntry(line string) (LedgerEntry, bool) {
	rest, ok := strings.CutPrefix(line, "law ")
	if !ok {
		return LedgerEntry{}, false
	}
	law, rest, ok := strings.Cut(rest, " for ")
	if !ok {
		return LedgerEntry{}, false
	}
	prov, rest, ok := strings.Cut(rest, " ")
	if !ok {
		return LedgerEntry{}, false
	}
	verdict, rest, ok := strings.Cut(rest, " ")
	if !ok {
		return LedgerEntry{}, false
	}
	k := strings.LastIndex(rest, " ")
	if k < 0 || !strings.HasPrefix(rest[k+1:], "h1:") {
		return LedgerEntry{}, false
	}
	e := LedgerEntry{Law: law, Provider: prov, Verdict: verdict, How: rest[:k], Digest: rest[k+1:]}
	if list, ok := strings.CutPrefix(e.How, "at "); ok {
		insts, ok := splitInstances(list)
		if !ok {
			return LedgerEntry{}, false
		}
		e.How, e.Instances = "", insts
	}
	return e, true
}

// splitInstances splits `(a, b) (c, d)` into its groups; a quoted string
// may hold parentheses.
func splitInstances(s string) ([]string, bool) {
	var out []string
	for s != "" {
		if s[0] != '(' {
			return nil, false
		}
		end, quoted := -1, false
		for i := 1; i < len(s) && end < 0; i++ {
			switch {
			case quoted && s[i] == '\\':
				i++
			case s[i] == '"':
				quoted = !quoted
			case !quoted && s[i] == ')':
				end = i
			}
		}
		if end < 0 {
			return nil, false
		}
		out = append(out, s[:end+1])
		s = strings.TrimPrefix(s[end+1:], " ")
	}
	return out, true
}

// FormatLedger is the text of the entries, sorted by line.
func FormatLedger(entries map[string]LedgerEntry) string {
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		lines = append(lines, e.Line())
	}
	sort.Strings(lines)
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// ledgerLaw and ledgerProvider are the entry's names: package-qualified,
// so a module's ledger names every package's laws apart.
func ledgerLaw(law *check.Law) string {
	return law.Pkg.Path + "." + law.Owner.Name + "." + law.Name
}

func ledgerProvider(p *check.Named) string {
	if p.Obj.Pkg == nil {
		return p.Obj.Name
	}
	return p.Obj.Pkg.Path + "." + p.Obj.Name
}

// instanceText is a site instance's text in a ledger: its constants in
// parameter order, _ for a parameter that takes none; "" when the site
// fixes no constant, which every entry of the law admits.
func instanceText(in RewriteInstance) string {
	if !constArgs(in) {
		return ""
	}
	parts := make([]string, len(in.Args))
	for i, a := range in.Args {
		parts[i] = "_"
		if a != nil {
			parts[i] = a.ExactString()
		}
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// LawDigest is the digest of every input of law's verdict for prov: the
// law-run version, the declaration of the law's owner (the law's text and
// its operations), the provider's type, every function the provider's
// conversion and constructor reach, through calls and names, and every
// type those name, and the instance set. A declaration is its source text;
// an edit of a declaration the law does not reach leaves the digest as it
// was.
func LawDigest(law *check.Law, prov *check.Named, conv check.Conversion, ctor *check.Func, insts []string) string {
	d := &digester{seen: map[any]bool{}, decls: map[string]string{}}
	d.typ(law.Owner)
	d.typ(prov.Obj)
	roots := tableFuncs(conv)
	if ctor != nil {
		roots = append(roots, ctor)
	}
	for _, f := range roots {
		d.fn(f)
	}
	keys := make([]string, 0, len(d.decls))
	for k := range d.decls {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	fmt.Fprintf(h, "%d:%s", len(LawRunVersion), LawRunVersion)
	for _, k := range keys {
		fmt.Fprintf(h, "%d:%s%d:%s", len(k), k, len(d.decls[k]), d.decls[k])
	}
	for _, in := range insts {
		fmt.Fprintf(h, "i%d:%s", len(in), in)
	}
	return "h1:" + base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// tableFuncs are the conversion's functions, in operation order.
func tableFuncs(conv check.Conversion) []*check.Func {
	names := make([]string, 0, len(conv.Table))
	for n := range conv.Table {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []*check.Func
	for _, n := range names {
		out = append(out, conv.Table[n])
	}
	return out
}

// digester collects the declarations a law's verdict reads, by a key that
// names each apart.
type digester struct {
	seen  map[any]bool
	decls map[string]string
}

func (d *digester) fn(f *check.Func) {
	if f == nil || d.seen[f] {
		return
	}
	d.seen[f] = true
	if f.Decl == nil || f.Pkg == nil {
		d.decls["func "+f.Name] = ""
		return
	}
	d.decls[fmt.Sprintf("func %s.%s@%d", f.Pkg.Path, f.Name, f.Decl.Pos())] = declText(f.Pkg, f.Decl)
	d.walk(f.Pkg, f.Decl)
}

func (d *digester) typ(t *check.TypeName) {
	if t == nil || d.seen[t] {
		return
	}
	d.seen[t] = true
	if t.Decl == nil || t.Pkg == nil {
		d.decls["type "+t.Name] = ""
		return
	}
	d.decls[fmt.Sprintf("type %s.%s", t.Pkg.Path, t.Name)] = declText(t.Pkg, t.Decl)
	d.walk(t.Pkg, t.Decl)
}

// walk follows every function and type that n names or calls.
func (d *digester) walk(p *check.Package, n syntax.Node) {
	if p.Info == nil {
		return
	}
	syntax.Inspect(n, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.CallExpr:
			if c := p.Info.Calls[n]; c != nil && c.Func != nil {
				d.fn(c.Func)
			}
		case *syntax.Ident:
			switch obj := p.Info.Uses[n].(type) {
			case *check.Func:
				d.fn(obj)
			case *check.TypeName:
				d.typ(obj)
			}
		}
		return true
	})
}

// declText is the source text of the declaration n of p.
func declText(p *check.Package, n syntax.Node) string {
	for _, f := range p.Files() {
		for _, decl := range f.Decls {
			if syntax.Node(decl) == n && int(n.End()) <= len(f.Src) && n.Pos() >= 0 {
				return f.Src[n.Pos():n.End()]
			}
		}
	}
	return ""
}

// RewriteSite is a site of a rewrite law the run build met: its law and
// provider, the site's constants, the ledger entry's verdict and state
// (current, stale or missing), and whether it applies.
type RewriteSite struct {
	Law      string `json:"law"`
	Provider string `json:"provider"`
	Instance string `json:"instance"`
	Verdict  string `json:"verdict"`
	State    string `json:"ledger"`
	Applied  bool   `json:"applied"`
}

// LedgerEvidence is the run build's evidence: the ledger's entries. Admit
// admits a law at a site when the entry for the law and provider is
// current (its digest is the one the sources give, and it lists the
// site's constants) and proved. It records every site it was asked
// about, and Lower records the rewrites applied.
type LedgerEvidence struct {
	entries map[string]LedgerEntry
	rules   map[string]rwRule
	digests map[string]string
	sites   map[string]*RewriteSite
	order   []string
	applied int
	// Ledgers name each ledger read and its sha256, for the producer
	// record; set by the caller.
	Ledgers []string
}

// NewLedgerEvidence is the evidence of entries for the run build of pkg.
func NewLedgerEvidence(pkg *check.Package, entries map[string]LedgerEntry) *LedgerEvidence {
	e := &LedgerEvidence{entries: entries, rules: map[string]rwRule{}, digests: map[string]string{}, sites: map[string]*RewriteSite{}}
	for prov, rules := range newRewriter(pkg, nil).rules {
		for _, r := range rules {
			e.rules[ledgerLaw(r.law)+" for "+ledgerProvider(prov)] = r
		}
	}
	return e
}

// HasRules reports whether the package has a rewrite law a provider's run
// checks: without one the build never asks.
func (e *LedgerEvidence) HasRules() bool { return len(e.rules) > 0 }

// Admit: see LedgerEvidence.
func (e *LedgerEvidence) Admit(law *check.Law, prov *check.Named, in RewriteInstance) (bool, string) {
	key := ledgerLaw(law) + " for " + ledgerProvider(prov)
	inst := instanceText(in)
	entry, found := e.entries[key]
	state := "current"
	switch {
	case !found:
		state = "missing"
	case inst != "" && !slices.Contains(entry.Instances, inst):
		state = "stale"
	case e.digest(key, law, prov, entry.Instances) != entry.Digest:
		state = "stale"
	}
	ok := state == "current" && entry.Verdict == "proved"
	site := key + " " + inst
	if s := e.sites[site]; s == nil {
		e.sites[site] = &RewriteSite{Law: ledgerLaw(law), Provider: ledgerProvider(prov), Instance: inst, Verdict: entry.Verdict, State: state}
		e.order = append(e.order, site)
	}
	if ok && law.Rewrite != nil {
		e.sites[site].Applied = true
	}
	switch {
	case ok:
		return true, ""
	case state != "current":
		return false, "ergo.laws: " + key + " is " + state + "; run ergo test"
	}
	return false, "law " + key + " is " + entry.Verdict + ", not proved: only proved laws change code"
}

func (e *LedgerEvidence) digest(key string, law *check.Law, prov *check.Named, insts []string) string {
	k := key + "\x00" + strings.Join(insts, "\x00")
	if d, ok := e.digests[k]; ok {
		return d
	}
	r := e.rules[key]
	d := LawDigest(law, prov, r.conv, r.ctor, insts)
	e.digests[k] = d
	return d
}

// Sites are the rewrite sites the build met, in the order it met them.
func (e *LedgerEvidence) Sites() []RewriteSite {
	out := make([]RewriteSite, 0, len(e.order))
	for _, k := range e.order {
		out = append(out, *e.sites[k])
	}
	return out
}

// Notes are the lines a build prints: one per entry that is stale or
// missing at some site, sorted.
func (e *LedgerEvidence) Notes() []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range e.order {
		s := e.sites[k]
		if s.State == "current" {
			continue
		}
		line := "ergo.laws: " + s.Law + " for " + s.Provider + " is " + s.State + "; run ergo test"
		if !seen[line] {
			seen[line] = true
			out = append(out, line)
		}
	}
	sort.Strings(out)
	return out
}

// Applied is the count of windows the build rewrote.
func (e *LedgerEvidence) Applied() int { return e.applied }

// record is the producer record's Go file: the ledgers read and the
// rewrites applied, kept in the binary by a store in an init function.
func (e *LedgerEvidence) record(l *lowerer, applied int) []byte {
	e.applied = applied
	text := strings.Join(e.Ledgers, "; ")
	if text != "" {
		text += "; "
	}
	text += fmt.Sprintf("rewrites applied %d", applied)
	rec, kept := l.names.alloc("ergoLaws"), l.names.alloc("ergoLawsKept")
	return fmt.Appendf(nil, "package main\n\n// %s is the producer record of the law ledgers this binary was built\n// with (runtime.md, Tests 10).\nvar %s = %q\n\nvar %s string\n\nfunc init() { %s = %s }\n", rec, rec, "ergo producer: "+text, kept, kept, rec)
}

// LedgerEntries are the ledger's entries from the check runs of ev, the
// evidence RewriteChecks returned for u: one per rewrite or post law and
// provider whose run printed a verdict. A law no run took writes nothing.
func LedgerEntries(u Unit, ev RewriteEvidence) []LedgerEntry {
	c, ok := ev.(*checkedEvidence)
	if !ok {
		return nil
	}
	var out []LedgerEntry
	for prov, rules := range newRewriter(u.Package, nil).rules {
		for _, r := range rules {
			if !c.ran[r.law] {
				continue
			}
			key := evidenceKey(r.law.Title(u.Package), provTitle(u.Package, prov))
			verdict := c.verdict[key]
			if verdict == "" {
				continue
			}
			var insts []string
			for _, in := range c.instList {
				if t := instanceText(in); in.Law == r.law && t != "" && !slices.Contains(insts, t) {
					insts = append(insts, t)
				}
			}
			sort.Strings(insts)
			out = append(out, LedgerEntry{Law: ledgerLaw(r.law), Provider: ledgerProvider(prov), Verdict: verdict, How: c.how[key], Instances: insts, Digest: LawDigest(r.law, prov, r.conv, r.ctor, insts)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}
