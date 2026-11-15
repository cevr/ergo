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
// checked-in file ergo.laws beside ergo.mod, module-wide as go.sum is,
// which holds one line per rewrite law, provider and site instance whose
// run ergo test checked:
//
//	law <pkg>.<Type>.<Law> for <pkg>.<Provider> [at (c, …)] <verdict> h1:<digest>
//
// <pkg> is a package's module-wide name (LedgerNames): its import path,
// and for a binary's main package the import path of its folder. The
// verdict is proved, tested or skipped. The digest (LawDigest) covers
// every input of the verdict, the instance included, so the run build is a
// function of the sources and the ledger: a site applies a rewrite only on
// a current, proved entry for its constants (LedgerEvidence). Every part
// of a line is a function of the module's sources, so the ledger does not
// depend on which binary's ergo test ran last. ergo test writes the file
// (LedgerEntries); the run build only reads it.

// LedgerFile is the ledger's file name, beside ergo.mod.
const LedgerFile = "ergo.laws"

// LawRunVersion names the law runner and the digest's encoding: a change
// of either makes every entry stale.
const LawRunVersion = "ergo-laws/2"

// LedgerEntry is one line of a ledger. The run's text (its cases, its
// symbols) is not part of it: one run takes the instances of the binary
// that ran it, so its text is not a function of the module's sources;
// ergo test prints it.
type LedgerEntry struct {
	Law, Provider string
	// Instance is the site's constants, `(c, …)`; "" for a site with none.
	Instance string
	Verdict  string
	Digest   string
}

// Key names the entry: its law, its provider and its instance.
func (e LedgerEntry) Key() string { return ledgerKey(e.Law, e.Provider, e.Instance) }

func ledgerKey(law, prov, inst string) string {
	k := law + " for " + prov
	if inst != "" {
		k += " at " + inst
	}
	return k
}

// Line is the entry's line in the file, with no line break.
func (e LedgerEntry) Line() string {
	return "law " + e.Key() + " " + e.Verdict + " " + e.Digest
}

// LawPackage and ProviderPackage are the module-wide names of the
// packages that declare the entry's law and provider, and LawName and
// ProviderName their names there (Type.Law, Provider).
func (e LedgerEntry) LawPackage() (pkg, name string) {
	i := strings.LastIndex(e.Law, ".")
	if i < 0 {
		return "", e.Law
	}
	j := strings.LastIndex(e.Law[:i], ".")
	if j < 0 {
		return "", e.Law
	}
	return e.Law[:j], e.Law[j+1:]
}

func (e LedgerEntry) ProviderPackage() (pkg, name string) {
	i := strings.LastIndex(e.Provider, ".")
	if i < 0 {
		return "", e.Provider
	}
	return e.Provider[:i], e.Provider[i+1:]
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
	e := LedgerEntry{Law: law, Provider: prov}
	if list, ok := strings.CutPrefix(rest, "at "); ok {
		insts, tail, ok := splitInstance(list)
		if !ok {
			return LedgerEntry{}, false
		}
		e.Instance, rest = insts, tail
	}
	verdict, digest, ok := strings.Cut(rest, " ")
	if !ok || !strings.HasPrefix(digest, "h1:") || strings.Contains(digest, " ") {
		return LedgerEntry{}, false
	}
	e.Verdict, e.Digest = verdict, digest
	return e, true
}

// splitInstance splits `(a, b) rest` into the group and the rest after
// one space; a quoted string may hold parentheses and spaces.
func splitInstance(s string) (string, string, bool) {
	if s == "" || s[0] != '(' {
		return "", "", false
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
	if end < 0 || !strings.HasPrefix(s[end+1:], " ") {
		return "", "", false
	}
	return s[:end+1], s[end+2:], true
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

// LedgerNames are the module-wide names of packages whose name is not
// their path: a binary's main package is named by its folder's import
// path, so two binaries of one module name their laws apart. Any other
// package is named by its path.
type LedgerNames map[*check.Package]string

// Of is p's module-wide name.
func (n LedgerNames) Of(p *check.Package) string {
	if s, ok := n[p]; ok {
		return s
	}
	return p.Path
}

// law and provider are the entry's names: package-qualified.
func (n LedgerNames) law(law *check.Law) string {
	return n.Of(law.Pkg) + "." + law.Owner.Name + "." + law.Name
}

func (n LedgerNames) provider(p *check.Named) string {
	if p.Obj.Pkg == nil {
		return p.Obj.Name
	}
	return n.Of(p.Obj.Pkg) + "." + p.Obj.Name
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
// its operations), the provider's type, every function
// the provider's conversion and constructor reach, through calls and
// names, every type and package let those name, at any depth and in any
// package, whether the provider's construction is closed (construction.go:
// the proof's state set depends on it), and the instance (instanceText; ""
// for none). A declaration is its tokens (syntax.TokenText), keyed by its
// kind, package and name: no position, space or comment. So an edit of a
// declaration the law reaches makes the digest change, and an edit that
// changes no token of one, moves one, or touches a declaration the law
// does not reach leaves it as it was.
func LawDigest(law *check.Law, prov *check.Named, conv check.Conversion, ctor *check.Func, inst string) string {
	d := &digester{seen: map[any]bool{}, decls: map[string]bool{}, lets: map[*check.Var]letDecl{}, named: map[*check.Package]map[string]*check.Var{}, indexed: map[*check.Package]bool{}}
	d.typ(law.Owner)
	d.typ(prov.Obj)
	roots := tableFuncs(conv)
	if ctor != nil {
		roots = append(roots, ctor)
	}
	for _, f := range roots {
		d.fn(f)
	}
	closed := "closed"
	if constructionOpen(prov, ctor, conv) != nil {
		closed = "open"
	}
	keys := make([]string, 0, len(d.decls))
	for k := range d.decls {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	fmt.Fprintf(h, "%d:%s", len(LawRunVersion), LawRunVersion)
	for _, k := range keys {
		fmt.Fprintf(h, "%d:%s", len(k), k)
	}
	fmt.Fprintf(h, "c%d:%s", len(closed), closed)
	fmt.Fprintf(h, "i%d:%s", len(inst), inst)
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

// digester collects the declarations a law's verdict reads: each is one
// entry of decls, its kind, package and name, then its tokens. Two
// same-named functions (196m) differ in their tokens.
type digester struct {
	seen  map[any]bool
	decls map[string]bool
	// lets are the package lets of the packages indexed, by their
	// variable, each with its declaration.
	lets    map[*check.Var]letDecl
	named   map[*check.Package]map[string]*check.Var
	indexed map[*check.Package]bool
}

// letDecl is a package let's declaration and its package.
type letDecl struct {
	pkg  *check.Package
	decl *syntax.VarDecl
}

func (d *digester) fn(f *check.Func) {
	if f == nil || d.seen[f] {
		return
	}
	d.seen[f] = true
	if f.Decl == nil || f.Pkg == nil {
		d.decls["func "+f.Name] = true
		return
	}
	d.decl("func "+f.Pkg.Path+"."+f.Name, f.Pkg, f.Decl)
}

func (d *digester) typ(t *check.TypeName) {
	if t == nil || d.seen[t] {
		return
	}
	d.seen[t] = true
	if t.Decl == nil || t.Pkg == nil {
		d.decls["type "+t.Name] = true
		return
	}
	d.decl("type "+t.Pkg.Path+"."+t.Name, t.Pkg, t.Decl)
}

// let adds the declaration of v when it is a package let of a package the
// walk indexed; a local variable has none.
func (d *digester) let(v *check.Var) {
	l, ok := d.lets[v]
	if !ok || d.seen[l.decl] {
		return
	}
	d.seen[l.decl] = true
	d.decl("let "+l.pkg.Path+"."+v.Name, l.pkg, l.decl)
}

// decl adds the declaration n of p under key and walks it.
func (d *digester) decl(key string, p *check.Package, n syntax.Node) {
	d.decls[key+"\n"+syntax.TokenText(declText(p, n))] = true
	d.walk(p, n)
}

// index records the package lets of p.
func (d *digester) index(p *check.Package) {
	if p == nil || p.Info == nil || d.indexed[p] {
		return
	}
	d.indexed[p] = true
	for _, f := range p.Files() {
		for _, decl := range f.Decls {
			vd, ok := decl.(*syntax.VarDecl)
			if !ok {
				continue
			}
			syntax.Inspect(vd, func(n syntax.Node) bool {
				if id, ok := n.(*syntax.Ident); ok {
					if v, ok := p.Info.Defs[id].(*check.Var); ok {
						d.lets[v] = letDecl{pkg: p, decl: vd}
						if d.named[p] == nil {
							d.named[p] = map[string]*check.Var{}
						}
						d.named[p][v.Name] = v
					}
				}
				return true
			})
		}
	}
}

// walk follows every function, type and package let that n names or
// calls, qualified names included.
func (d *digester) walk(p *check.Package, n syntax.Node) {
	if p.Info == nil {
		return
	}
	d.index(p)
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
			case *check.Var:
				d.let(obj)
			}
		case *syntax.SelectorExpr:
			// The checker records no use of a qualified name's member.
			if id, ok := n.X.(*syntax.Ident); ok {
				if pn, ok := p.Info.Uses[id].(*check.PkgName); ok {
					d.member(pn.Pkg, n.Sel.Name)
				}
			}
		}
		return true
	})
}

// member adds the package let, the type or the functions named name in
// q, which a qualified name reads.
func (d *digester) member(q *check.Package, name string) {
	if q == nil {
		return
	}
	d.index(q)
	if v := d.named[q][name]; v != nil {
		d.let(v)
	}
	for _, t := range q.Types {
		if t.Name == name {
			d.typ(t)
		}
	}
	for _, f := range q.Funcs {
		if f.Name == name {
			d.fn(f)
		}
	}
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

// RewriteSite is a site of a rewrite law the run build met: the source
// position of its window's first call, its law and provider, the site's
// constants, the ledger entry's verdict and state
// (current, stale or missing), and whether it applies.
type RewriteSite struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Law      string `json:"law"`
	Provider string `json:"provider"`
	Instance string `json:"instance"`
	Verdict  string `json:"verdict"`
	State    string `json:"ledger"`
	Applied  bool   `json:"applied"`
	// Why is why a site with a current entry keeps the reference
	// lowering: its verdict, and, for a provider whose construction is
	// open, that the proof covers every value of its type and the place
	// that opens it (construction.go). State says why for any other site.
	Why string `json:"why,omitempty"`
}

// LedgerEvidence is the run build's evidence: the ledger's entries. Admit
// admits a law at a site when the entry for the law, the provider and the
// site's constants is current (its digest is the one the sources give)
// and proved. It records every site it was asked about and every entry it
// read, and Lower records the rewrites applied.
type LedgerEvidence struct {
	names   LedgerNames
	entries map[string]LedgerEntry
	rules   map[string]rwRule
	digests map[string]string
	sites   map[string]*RewriteSite
	order   []string
	read    map[string]bool
	applied int
	// file, line and col are the position of the window the lowering
	// asks about (rewriteWindow).
	file      string
	line, col int
}

// NewLedgerEvidence is the evidence of entries for the run build of pkg,
// whose packages names names.
func NewLedgerEvidence(pkg *check.Package, names LedgerNames, entries map[string]LedgerEntry) *LedgerEvidence {
	e := &LedgerEvidence{names: names, entries: entries, rules: map[string]rwRule{}, digests: map[string]string{}, sites: map[string]*RewriteSite{}, read: map[string]bool{}}
	for prov, rules := range newRewriter(pkg, nil).rules {
		for _, r := range rules {
			e.rules[names.law(r.law)+" for "+names.provider(prov)] = r
		}
	}
	return e
}

// HasRules reports whether the package has a rewrite law a provider's run
// checks: without one the build never asks.
func (e *LedgerEvidence) HasRules() bool { return len(e.rules) > 0 }

// Admit: see LedgerEvidence.
func (e *LedgerEvidence) Admit(law *check.Law, prov *check.Named, in RewriteInstance) (bool, string) {
	lawName, provName, inst := e.names.law(law), e.names.provider(prov), instanceText(in)
	key := ledgerKey(lawName, provName, inst)
	entry, found := e.entries[key]
	state := "current"
	switch {
	case !found:
		state = "missing"
	case e.digest(key, law, prov, inst) != entry.Digest:
		state = "stale"
	}
	if found {
		e.read[key] = true
	}
	ok := state == "current" && entry.Verdict == "proved"
	file, line, col := e.file, e.line, e.col
	site := fmt.Sprintf("%s:%d:%d %s", file, line, col, key)
	if s := e.sites[site]; s == nil {
		e.sites[site] = &RewriteSite{
			File: file, Line: line, Column: col,
			Law: lawName, Provider: provName, Instance: inst, Verdict: entry.Verdict, State: state,
		}
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
	why := "law " + key + " is " + entry.Verdict + ", not proved: only proved laws change code"
	r := e.rule(law, prov)
	if open := constructionWhy(prov, r.ctor, r.conv, (*openConstruction).at); open != "" {
		why += "; " + open
	}
	e.sites[site].Why = why
	return false, why
}

func (e *LedgerEvidence) digest(key string, law *check.Law, prov *check.Named, inst string) string {
	if d, ok := e.digests[key]; ok {
		return d
	}
	r := e.rule(law, prov)
	d := LawDigest(law, prov, r.conv, r.ctor, inst)
	e.digests[key] = d
	return d
}

// rule is the rule of law for prov: the root's, or, for a provider of
// another package of the module (cellRewriter), one of the laws that
// package runs, read once.
func (e *LedgerEvidence) rule(law *check.Law, prov *check.Named) rwRule {
	key := e.names.law(law) + " for " + e.names.provider(prov)
	if r, ok := e.rules[key]; ok || prov.Obj.Pkg == nil {
		return r
	}
	for p, rules := range newRewriter(prov.Obj.Pkg, nil).rules {
		for _, r := range rules {
			if k := e.names.law(r.law) + " for " + e.names.provider(p); e.rules[k].law == nil {
				e.rules[k] = r
			}
		}
	}
	return e.rules[key]
}

// evidenceFor is the evidence for the laws of the providers pkg declares:
// the ledger of the module that declares pkg, chosen here alone and by
// module, never by a package's name or std membership (owner, 2026-11-12:
// the standard library gets no special treatment; STD-NOPRIV NP2). A
// package with a unit is the build's module's: it takes the run build's
// evidence, the module's ergo.laws (nil in a build that takes none). A
// package with none ships in the compiler with its ledger (shippedLedger),
// which answers for it. A required module's own ledger joins here (MD1).
func (l *lowerer) evidenceFor(pkg *check.Package) RewriteEvidence {
	if l.isSourced(pkg) {
		if l.rewrite == nil {
			return nil
		}
		return l.rewrite.ev
	}
	return newShippedEvidence(pkg)
}

// Read is the text of the entries the build read, sorted: the run build
// is a function of the sources and these lines alone, so an entry of
// another binary, or a site this build does not have, never changes it.
func (e *LedgerEvidence) Read() string {
	read := map[string]LedgerEntry{}
	for k := range e.read {
		read[k] = e.entries[k]
	}
	return FormatLedger(read)
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
		line := "ergo.laws: " + ledgerKey(s.Law, s.Provider, s.Instance) + " is " + s.State + "; run ergo test"
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

// record is the producer record's Go file: the sha256 of the entries the
// build read (Read) and the rewrites applied, kept in the binary by a
// store in an init function.
func (e *LedgerEvidence) record(l *lowerer, applied int) []byte {
	e.applied = applied
	sum := sha256.Sum256([]byte(e.Read()))
	text := fmt.Sprintf("%s entries sha256:%x; rewrites applied %d", LedgerFile, sum, applied)
	rec, kept := l.names.alloc("ergoLaws"), l.names.alloc("ergoLawsKept")
	return fmt.Appendf(nil, "package main\n\n// %s is the producer record of the law ledgers this binary was built\n// with (runtime.md, Tests 10).\nvar %s = %q\n\nvar %s string\n\nfunc init() { %s = %s }\n", rec, rec, "ergo producer: "+text, kept, kept, rec)
}

// LedgerEntries are the ledger's entries from the check runs of ev, the
// evidence RewriteChecks returned for u, whose packages names names: for
// each rewrite or post law and provider whose run printed a verdict, one
// with no instance and one per site instance the run took, for the root's
// providers and its imports' (importRules). A law no run took writes
// nothing.
func LedgerEntries(u Unit, names LedgerNames, ev RewriteEvidence) []LedgerEntry {
	c, ok := ev.(*checkedEvidence)
	if !ok {
		return nil
	}
	var out []LedgerEntry
	all := newRewriter(u.Package, nil).rules
	for prov, rules := range importRules(u) {
		all[prov] = append(all[prov], rules...)
	}
	for prov, rules := range all {
		for _, r := range rules {
			if !c.ran[r.law] {
				continue
			}
			key := evidenceKey(r.law.Title(u.Package), provTitle(u.Package, prov))
			verdict := c.verdict[key]
			if verdict == "" {
				continue
			}
			insts := []string{""}
			for _, in := range c.instList {
				if t := instanceText(in); in.Law == r.law && t != "" && !slices.Contains(insts, t) {
					insts = append(insts, t)
				}
			}
			for _, inst := range insts {
				out = append(out, LedgerEntry{Law: names.law(r.law), Provider: names.provider(prov), Instance: inst, Verdict: verdict, Digest: LawDigest(r.law, prov, r.conv, r.ctor, inst)})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}
