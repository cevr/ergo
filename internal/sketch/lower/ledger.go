package lower

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The law ledger (runtime.md, Tests 10; plan-2 §3; owner q116, q122): the
// checked-in file ergo.laws beside ergo.mod, module-wide as go.sum is,
// which holds one line per rewrite law, provider and site instance whose
// run ergo test checked:
//
//	law <pkg>.<Type>.<Law> for <pkg>.<Provider> [at (c, …)] [under <build>] <verdict> [by <run>] [with <key>,…] h1:<digest>
//	law <pkg>.<Law> [under <build>] <verdict> [by <run>] [with <key>,…] h1:<digest>
//	fact go <import path>.<Func> <fact> <param> <verdict> [by <run>] [with <key>,…] h1:<digest>
//
// <pkg> is a package's module-wide name (LedgerNames): its import path,
// and for a binary's main package the import path of its folder. The
// verdict is proved, validated, tested, skipped or refuted (LAWFFI r2 §1;
// LFL); by names the run that gave it (native: the law-check build; eval:
// the check-time evaluator; guard: a fact's guard run, LF2), and with the
// shown foreign keys of the Go code the entry reaches (foreignkey.go).
// under names the build configuration (goexperiment.simd) when the Go code
// the entry reaches is another in the stock build (LAW-BUILD-KEY): it is a
// key part, so the stock build's entry and a GOEXPERIMENT=simd build's
// stand side by side and each build reads its own. A
// law with no provider is a top-level law (LF6a); a fact line is a Go
// function's vouched fact and its guard (LF2), which this file reads and
// keeps but does not write. The digest (LawDigest) covers every input of
// the verdict, the instance and the foreign keys included, so the run
// build is a function of the sources, the Go code they reach and the
// ledger: a site applies a rewrite only on a current entry for its
// constants whose level admitLevel admits (LedgerEvidence), and a current
// refuted entry stops the build. Every part
// of a line is a function of the module's sources, so the ledger does not
// depend on which binary's ergo test ran last. ergo test writes the file
// (LedgerEntries); the run build only reads it.

// LedgerFile is the ledger's file name, beside ergo.mod.
const LedgerFile = "ergo.laws"

// LawRunVersion names the law runner and the digest's encoding: a change
// of either makes every entry stale.
const LawRunVersion = "ergo-laws/2"

// ledgerVerdicts are the verdicts a line may hold.
var ledgerVerdicts = sync.OnceValue(func() map[string]bool {
	return map[string]bool{"proved": true, "validated": true, "tested": true, "skipped": true, "refuted": true}
})

// LedgerEntry is one line of a ledger. The run's text (its cases, its
// symbols) is not part of it: one run takes the instances of the binary
// that ran it, so its text is not a function of the module's sources;
// ergo test prints it.
type LedgerEntry struct {
	// Law and Provider name a law line; Provider is "" for a top-level
	// law. Fact names a fact line instead (`go <target> <fact> <param>`).
	Law, Provider string
	Fact          string
	// Instance is the site's constants, `(c, …)`; "" for a site with none.
	Instance string
	// Build is the build configuration the entry's Go code depends on
	// (ForeignKey.Build), a key part; "" when every build reaches the same.
	Build   string
	Verdict string
	// By is the run that gave the verdict (native, eval, guard); "" for
	// none, which no level above tested may lack.
	By string
	// With is the shown foreign keys the verdict was given with, sorted,
	// comma-separated; "" when the entry reaches no Go code.
	With   string
	Digest string
}

// Key names the entry: its law, its provider, its instance and its
// build, or its fact.
func (e LedgerEntry) Key() string {
	if e.Fact != "" {
		return "fact " + e.Fact
	}
	return ledgerKey(e.Law, e.Provider, e.Instance, e.Build)
}

func ledgerKey(law, prov, inst, build string) string {
	k := law
	if prov != "" {
		k += " for " + prov
	}
	if inst != "" {
		k += " at " + inst
	}
	if build != "" {
		k += " under " + build
	}
	return k
}

// Line is the entry's line in the file, with no line break.
func (e LedgerEntry) Line() string {
	s := e.Key() + " " + e.Verdict
	if e.Fact == "" {
		s = "law " + s
	}
	if e.By != "" {
		s += " by " + e.By
	}
	if e.With != "" {
		s += " with " + e.With
	}
	return s + " " + e.Digest
}

// LawPackage and ProviderPackage are the module-wide names of the
// packages that declare the entry's law and provider, and LawName and
// ProviderName their names there (Type.Law, Provider). A top-level law's
// entry (no provider) names its law Law, as its package declares it.
func (e LedgerEntry) LawPackage() (pkg, name string) {
	i := strings.LastIndex(e.Law, ".")
	if i < 0 {
		return "", e.Law
	}
	if e.Provider == "" {
		return e.Law[:i], e.Law[i+1:]
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
	var e LedgerEntry
	var rest string
	if fact, ok := strings.CutPrefix(line, "fact "); ok {
		words := strings.Fields(fact)
		i := slices.IndexFunc(words, func(w string) bool { return ledgerVerdicts()[w] })
		if i < 3 || words[0] != "go" {
			return LedgerEntry{}, false
		}
		e.Fact, rest = strings.Join(words[:i], " "), strings.Join(words[i:], " ")
	} else {
		r, ok := strings.CutPrefix(line, "law ")
		if !ok {
			return LedgerEntry{}, false
		}
		e.Law, r, ok = strings.Cut(r, " ")
		if !ok {
			return LedgerEntry{}, false
		}
		if p, ok := strings.CutPrefix(r, "for "); ok {
			e.Provider, r, ok = strings.Cut(p, " ")
			if !ok {
				return LedgerEntry{}, false
			}
		}
		if list, ok := strings.CutPrefix(r, "at "); ok {
			insts, tail, ok := splitInstance(list)
			if !ok {
				return LedgerEntry{}, false
			}
			e.Instance, r = insts, tail
		}
		if b, ok := strings.CutPrefix(r, "under "); ok {
			e.Build, r, ok = strings.Cut(b, " ")
			if !ok {
				return LedgerEntry{}, false
			}
		}
		rest = r
	}
	// The tail: <verdict> [by <run>] [with <keys>] h1:<digest>.
	words := strings.Split(rest, " ")
	if len(words) < 2 || !ledgerVerdicts()[words[0]] {
		return LedgerEntry{}, false
	}
	e.Verdict, words = words[0], words[1:]
	if len(words) >= 3 && words[0] == "by" {
		e.By, words = words[1], words[2:]
	}
	if len(words) >= 3 && words[0] == "with" {
		e.With, words = words[1], words[2:]
	}
	if len(words) != 1 || !strings.HasPrefix(words[0], "h1:") {
		return LedgerEntry{}, false
	}
	e.Digest = words[0]
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
// does not reach leaves it as it was. keys adds the foreign key of each Go
// package whose function an `extern go` the walk reached names
// (foreignkey.go): with is their shown forms, the line's with field, and
// build the build configuration they depend on, the line's under field.
func LawDigest(law *check.Law, prov *check.Named, conv check.Conversion, ctor *check.Func, inst string, keys ForeignKeys) (digest, with, build string) {
	d := newDigester()
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
	fmt.Fprintf(h, "c%d:%s", len(closed), closed)
	fmt.Fprintf(h, "i%d:%s", len(inst), inst)
	with, build = d.foreignKeys(h, keys)
	return "h1:" + base64.StdEncoding.EncodeToString(h.Sum(nil)), with, build
}

// foreignKeys writes the key of each Go package the walk reached into h,
// in path order, and returns their shown forms, sorted and joined by
// commas, and the build configuration any of them depends on; nothing for
// nil keys.
func (d *digester) foreignKeys(h io.Writer, keys ForeignKeys) (with, build string) {
	if keys == nil || len(d.foreign) == 0 {
		return "", ""
	}
	targets := make([]string, 0, len(d.foreign))
	for p := range d.foreign {
		targets = append(targets, p)
	}
	sort.Strings(targets)
	shown := map[string]bool{}
	groups := map[string]hash.Hash{}
	for _, p := range targets {
		k := keys(p)
		build = UnionBuild(build, k.Build)
		s := p + " " + k.Key
		fmt.Fprintf(h, "f%d:%s", len(s), s)
		if k.Group == "" {
			shown[k.Shown] = true
			continue
		}
		if groups[k.Group] == nil {
			groups[k.Group] = sha256.New()
		}
		fmt.Fprintf(groups[k.Group], "%d:%s", len(s), s)
	}
	for g, gh := range groups {
		shown[g+"@"+short(hex.EncodeToString(gh.Sum(nil)))] = true
	}
	out := make([]string, 0, len(shown))
	for s := range shown {
		out = append(out, s)
	}
	sort.Strings(out)
	return strings.Join(out, ","), build
}

func newDigester() *digester {
	return &digester{seen: map[any]bool{}, decls: map[string]bool{}, lets: map[*check.Var]letDecl{}, named: map[*check.Package]map[string]*check.Var{}, indexed: map[*check.Package]bool{}, foreign: map[string]bool{}}
}

// TargetDigest is the digest of every input of the verdict of the run of
// law with binding in the test build of pkg (lawcache; v1 audit 3, F7),
// LawDigest's rule over every slot: the law's declaration (its owner's,
// or its own at top level), each slot's provider, its type and the
// functions its conversion and constructor reach, whether its
// construction is closed, and, so that an edit of how a case is drawn
// makes it stale too, the generator of every type the walk reached: its
// constructor and, for an operation type, its builder in pkg (check's
// builders.go), which no name in the law reaches; and the functions each
// implicit conversion and each generic call's witness the walk meets put
// in a value (derived: a codec's reader).
func TargetDigest(pkg *check.Package, law *check.Law, binding []check.LawProvider) string {
	_, digest := targetDigest(pkg, law, binding)
	return digest
}

// targetDigest is TargetDigest and the walk that made it.
func targetDigest(pkg *check.Package, law *check.Law, binding []check.LawProvider) (*digester, string) {
	d, slots := targetWalk(law, binding)
	return d, d.sum(pkg, slots)
}

// targetWalk is TargetDigest's walk: the digester holding what the run
// reaches, and its slots.
func targetWalk(law *check.Law, binding []check.LawProvider) (*digester, []string) {
	d := newDigester()
	d.derived = true
	switch {
	case law.Owner != nil:
		d.typ(law.Owner)
	case law.Machine != nil:
		d.typ(law.Machine)
	case law.Pkg != nil && law.Decl != nil:
		d.decl("law "+law.Pkg.Path+"."+law.Name, law.Pkg, law.Decl)
	}
	var slots []string
	for k, p := range binding {
		if p.Type == nil {
			continue
		}
		d.typ(p.Type.Obj)
		roots := tableFuncs(p.Conv)
		if p.Ctor != nil {
			roots = append(roots, p.Ctor)
		}
		for _, f := range roots {
			d.fn(f)
		}
		closed := "closed"
		if constructionOpen(p.Type, p.Ctor, p.Conv) != nil {
			closed = "open"
		}
		slots = append(slots, fmt.Sprintf("%d %s %s %t", k, p.Type, closed, p.Value))
	}
	return d, slots
}

// ContextDigest is the digest of every input of the verdict of the run of
// law under the named context lc in the test build of pkg (lawcache): the
// law's owner's declaration, the context's declaration and those of the
// contexts it embeds, every function, type and package let their
// provider expressions reach (each provider's constructor among them),
// the functions of each acquired provider's conversions, the instance's
// parameter types, and the generators of every type reached, by the
// tokens of each (no position). An edit of any of them makes a new
// digest.
func ContextDigest(pkg *check.Package, law *check.Law, lc check.LawContext) string {
	_, digest := contextDigest(pkg, law, lc)
	return digest
}

// contextDigest is ContextDigest and the walk that made it.
func contextDigest(pkg *check.Package, law *check.Law, lc check.LawContext) (*digester, string) {
	d, slots := contextWalk(law, lc)
	return d, d.sum(pkg, slots)
}

// contextWalk is ContextDigest's walk: the digester holding what the run
// reaches, and its slots.
func contextWalk(law *check.Law, lc check.LawContext) (*digester, []string) {
	d := newDigester()
	d.derived = true
	d.typ(law.Owner)
	d.context(lc.Context)
	var slots []string
	if lc.With != nil {
		for _, a := range lc.With.Acquired {
			if a.Entry == nil {
				continue
			}
			for _, conv := range a.Entry.Conversions {
				for _, f := range tableFuncs(conv) {
					d.fn(f)
				}
			}
			for _, c := range a.Entry.Caps {
				slots = append(slots, "acquires "+c.String())
			}
		}
		for _, t := range lc.With.Discharged {
			slots = append(slots, "binds "+t.String())
		}
	}
	for _, t := range lc.Params {
		slots = append(slots, "param "+t.String())
		if n, ok := t.(*check.Named); ok {
			d.typ(n.Obj)
		}
	}
	slots = append(slots, "context "+lc.Context.Name, "unrun "+lc.Unrun)
	return d, slots
}

// context adds the declaration of the named context c, and of each
// context it embeds, and walks their provider expressions.
func (d *digester) context(c *check.Context) {
	if c == nil || d.seen[c] {
		return
	}
	d.seen[c] = true
	// A law runs only under a named context (planLawContexts), which has
	// its declaration; an embedded one is named too.
	if p := c.Package(); p != nil && c.Decl != nil {
		d.decl("context "+p.Path+"."+c.Name, p, c.Decl)
	} else {
		d.decls["context "+c.Name] = true
	}
	for _, e := range c.Entries {
		d.context(e.Embedded)
	}
}

// sum is the digest of the declarations the walk added, the generators
// of the types it reached in pkg, and slots, in order.
func (d *digester) sum(pkg *check.Package, slots []string) string {
	d.generators(pkg)
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
	for _, s := range slots {
		fmt.Fprintf(h, "s%d:%s", len(s), s)
	}
	return "h1:" + base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// generators adds the generator of each type the walk reached, until it
// reaches no new one: the type's constructor, and an operation type's
// builder in pkg.
func (d *digester) generators(pkg *check.Package) {
	done := map[*check.TypeName]bool{}
	for {
		var next []*check.TypeName
		for k := range d.seen {
			if t, ok := k.(*check.TypeName); ok && !done[t] {
				next = append(next, t)
			}
		}
		if len(next) == 0 {
			return
		}
		for _, t := range next {
			done[t] = true
			d.fn(t.Constructor())
			if n, ok := t.Type.(*check.Named); ok && pkg != nil {
				if _, ops := n.Underlying().(*check.Interface); ops {
					if b := pkg.Builder(n); b != nil {
						d.fn(b.Func)
					}
				}
			}
		}
	}
}

// RunTarget is a run of a law the test build of a package reports under
// Label, which the verdict store keys (lawcache): a binding of a law that
// runs, whose providers all run; a run of an owner's law under a named
// context (Context set); or a machine's reachability law (Machine).
type RunTarget struct {
	Law     *check.Law
	Binding []check.LawProvider
	// Context is the named context the run builds, nil for a binding's
	// or a machine's run.
	Context *check.LawContext
	// Machine marks a reachability law's run: its search, which each
	// runner reports in its own terms (eval.MachineSearch, the test
	// build's line), so each keeps its own verdict of it.
	Machine bool
	Label   string
}

// Digest is TargetDigest of t, or ContextDigest for a run under a
// context.
func (t RunTarget) Digest(pkg *check.Package) string {
	if t.Context != nil {
		return ContextDigest(pkg, t.Law, *t.Context)
	}
	return TargetDigest(pkg, t.Law, t.Binding)
}

// DigestWith is Digest with the foreign key of each Go package whose
// function an `extern go` the walk reached names, as LawDigest takes them:
// the store's key of a run's verdict (lawcache) changes when the Go code
// behind it does, a module's own body included, and with the file go
// build compiles for the target (U1 SIMD-SCAN: a vector kernel's verdict
// is not its stock twin's). It is Digest when keys is nil or the walk
// reached no `extern go`.
func (t RunTarget) DigestWith(pkg *check.Package, keys ForeignKeys) string {
	var d *digester
	var digest string
	if t.Context != nil {
		d, digest = contextDigest(pkg, t.Law, *t.Context)
	} else {
		d, digest = targetDigest(pkg, t.Law, t.Binding)
	}
	return d.withForeign(digest, keys)
}

// TestDigestWith is TestDigest with the foreign keys DigestWith takes.
func TestDigestWith(pkg *check.Package, t *check.Test, keys ForeignKeys) string {
	d := newDigester()
	d.decl("test "+pkg.Path+"."+t.Name, pkg, t.Decl)
	return d.withForeign(d.sum(pkg, nil), keys)
}

// withForeign is digest and the foreign keys of the Go packages the walk
// reached, or digest alone when there are none or keys is nil.
func (d *digester) withForeign(digest string, keys ForeignKeys) string {
	if keys == nil || len(d.foreign) == 0 {
		return digest
	}
	h := sha256.New()
	fmt.Fprintf(h, "%d:%s", len(digest), digest)
	_, _ = d.foreignKeys(h, keys)
	return "h1:" + base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// RunTargets are the targets of pkg's law runs, in run order. Two runs
// with one label are no target: a line names one run.
func RunTargets(pkg *check.Package) []RunTarget {
	var out []RunTarget
	count := map[string]int{}
	add := func(t RunTarget) {
		count[t.Label]++
		out = append(out, t)
	}
	for _, r := range pkg.LawRuns {
		if r.Law.Unrun != "" {
			continue
		}
		if r.Law.Machine != nil && r.Law.Decl != nil && r.Law.Decl.Reach != nil {
			add(RunTarget{Law: r.Law, Machine: true, Label: "law " + r.Law.Title(pkg)})
			continue
		}
		for _, b := range r.Bindings {
			if bindingUnrun(b) != "" {
				continue
			}
			add(RunTarget{Law: r.Law, Binding: b, Label: BindingLabel(pkg, r.Law, b)})
		}
		for i := range r.Contexts {
			lc := &r.Contexts[i]
			if lc.Unrun != "" {
				continue
			}
			add(RunTarget{Law: r.Law, Context: lc, Label: "law " + r.Law.Title(pkg) + " under " + lc.Context.Name})
		}
	}
	return slices.DeleteFunc(out, func(t RunTarget) bool { return count[t.Label] > 1 })
}

// BindingLabel is the report's name of a run of law with the binding b
// in the test build of pkg: `law T.L for P` for a member law's owner,
// then `with x = P` for each parameter of an operation type.
func BindingLabel(pkg *check.Package, law *check.Law, b []check.LawProvider) string {
	label := "law " + law.Title(pkg)
	var with []string
	for k, s := range law.Slots {
		if s.Param < 0 {
			label += " for " + providerTitleIn(pkg, b[k])
			continue
		}
		with = append(with, law.Params[s.Param].Name+" = "+providerTitleIn(pkg, b[k]))
	}
	if len(with) > 0 {
		label += " with " + strings.Join(with, ", ")
	}
	return label
}

// providerTitleIn is the name a report of pkg's test build gives a
// provider: its type's name, with its package's name before it when it
// is of another package.
func providerTitleIn(pkg *check.Package, p check.LawProvider) string {
	if q := p.Type.Obj.Pkg; q != nil && q != pkg {
		return q.Name + "." + p.Type.Obj.Name
	}
	return p.Type.Obj.Name
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
	// foreign are the `extern go` targets the walk reached.
	foreign map[string]bool
	// nodes are the declarations the walk added, in walk order (reach.go).
	nodes []ReachedDecl
	// inputs are the declarations decl added, in the order the walk met
	// them: the graph rows' Input edges (RunTarget.Inputs).
	inputs []Input
	// derived makes the walk follow the code a value reaches with no name
	// in the source: each implicit conversion's operation functions and
	// each generic call's witnesses (a codec's decode[R](r) reaches its
	// reader's functions only so). A verdict's key needs them
	// (TargetDigest, ContextDigest: LAWCACHE-CODEC); the ledger's
	// LawDigest does not follow them yet.
	derived bool
}

// Input is one declaration whose tokens a verdict's digest covers: its
// package and its top-level declaration node. The graph rows' Input
// edges are these (internal/sketch/graphrows), so a digest changes when,
// and only when, the tokens of one of them change.
type Input struct {
	Pkg  *check.Package
	Decl syntax.Node
}

// Inputs is t's digest (Digest) and the declarations it covers.
func (t RunTarget) Inputs(pkg *check.Package) (string, []Input) {
	var d *digester
	var digest string
	if t.Context != nil {
		d, digest = contextDigest(pkg, t.Law, *t.Context)
	} else {
		d, digest = targetDigest(pkg, t.Law, t.Binding)
	}
	return digest, d.inputs
}

// TestInputs is the digest of a test block's inputs, by TargetDigest's
// rule (the test's declaration, every function, type and package let it
// reaches, and the generators of the types reached), and the
// declarations it covers. No store keys a test's verdict by it yet; the
// graph's Task rows do (CI-SELECT §12.3).
func TestInputs(pkg *check.Package, test *check.Test) (string, []Input) {
	d := newDigester()
	if test != nil && test.Decl != nil && pkg != nil {
		d.decl("test "+pkg.Path+"."+test.Name, pkg, test.Decl)
	}
	return d.sum(pkg, nil), d.inputs
}

// conversion adds the functions an implicit conversion's table puts in
// the value, and their instances' witnesses.
func (d *digester) conversion(c check.Conversion) {
	for _, f := range tableFuncs(c) {
		d.fn(f)
	}
	names := make([]string, 0, len(c.Instances))
	for n := range c.Instances {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		d.instance(c.Instances[n])
	}
}

// instance adds the functions the witnesses of a generic function's
// instance supply, at any depth.
func (d *digester) instance(in *check.Instance) {
	if in == nil || d.seen[in] {
		return
	}
	d.seen[in] = true
	for _, w := range in.Witnesses {
		for _, op := range w.Ops {
			d.fn(op.Func)
			d.instance(op.Inst)
		}
	}
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
	if f.Decl != nil && f.Decl.Extern != nil && f.Decl.Extern.Target != nil {
		d.foreign[f.Decl.Extern.Target.Value] = true
	}
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
	text := syntax.TokenText(declText(p, n))
	d.decls[key+"\n"+text] = true
	d.nodes = append(d.nodes, ReachedDecl{Key: key, Text: text, Pkg: p, Node: n})
	d.inputs = append(d.inputs, Input{Pkg: p, Decl: n})
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
		if e, ok := n.(syntax.Expr); ok && d.derived {
			if c, ok := p.Info.Implicit[e]; ok {
				d.conversion(c)
			}
			for _, c := range p.Info.ImplicitElems[e] {
				d.conversion(c)
			}
			d.instance(p.Info.Instances[e])
		}
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
// constants, the ledger entry's verdict, the run that gave it and the
// foreign keys the sources give now, its state (current, stale or
// missing), and whether it applies.
type RewriteSite struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Law      string `json:"law"`
	Provider string `json:"provider"`
	Instance string `json:"instance"`
	Build    string `json:"build,omitempty"`
	Verdict  string `json:"verdict"`
	By       string `json:"by,omitempty"`
	With     string `json:"with,omitempty"`
	State    string `json:"ledger"`
	Applied  bool   `json:"applied"`
	// Why is why a site with a current entry keeps the reference
	// lowering: its verdict, and, for a provider whose construction is
	// open, that the proof covers every value of its type and the place
	// that opens it (construction.go). State says why for any other site.
	Why string `json:"why,omitempty"`
}

// admitLevel is the one admission of a ledger level for a change of code
// of class (LAWFFI r2 §1; owner q192 to q195): a proved entry admits any
// class; a validated entry, a vouched fact whose guard passed or a law
// whose licence run passed, admits the value class only, and only with
// the run that gave it named; tested, skipped and refuted admit nothing.
// The memory-class exception of q193 is admitView.
func admitLevel(verdict, by string, class trust) bool {
	switch verdict {
	case "proved":
		return true
	case "validated":
		return by != "" && class == valueClass
	}
	return false
}

// admitView is the one memory-class exception of q193 (LAWFFI r2 §1;
// LF2b): the shortcut host-view, a zero-copy view of a string's bytes at a
// Go call, rests on a validated fact as a value-class change does. No
// other memory-class change admits validated.
func admitView(verdict, by string) bool {
	return verdict == "proved" || verdict == "validated" && by != ""
}

// lawRewriteClass is the trust class of a law rewrite: a false law gives a
// wrong value (LAWFFI r2 §3.2 rule 2). LF6c gives it a registry row of its
// own (law-rewrite-foreign).
const lawRewriteClass = valueClass

// LedgerEvidence is the run build's evidence: the ledger's entries. Admit
// admits a law at a site when the entry for the law, the provider and the
// site's constants is current (its digest is the one the sources and the
// foreign keys give) and admitLevel admits its level. It records every
// site it was asked about, every entry it read, and every current refuted
// entry, which stops the build (Refusals); Lower records the rewrites
// applied.
type LedgerEvidence struct {
	names   LedgerNames
	entries map[string]LedgerEntry
	keys    ForeignKeys
	rules   map[string]rwRule
	digests map[string][3]string
	sites   map[string]*RewriteSite
	order   []string
	read    map[string]bool
	refused map[string]bool
	// notes are the line a build prints for each site whose entry is not
	// current: what changed, when the foreign keys say it.
	notes   map[string]string
	applied int
	// file, line and col are the position of the window the lowering
	// asks about (rewriteWindow).
	file      string
	line, col int
}

// NewLedgerEvidence is the evidence of entries for the run build of pkg,
// whose packages names names, with the foreign keys of the Go code the
// program links.
func NewLedgerEvidence(pkg *check.Package, names LedgerNames, entries map[string]LedgerEntry, keys ForeignKeys) *LedgerEvidence {
	e := &LedgerEvidence{names: names, entries: entries, keys: keys, rules: map[string]rwRule{}, digests: map[string][3]string{}, sites: map[string]*RewriteSite{}, read: map[string]bool{}, refused: map[string]bool{}, notes: map[string]string{}}
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
	digest, with, build := e.digest(ledgerKey(lawName, provName, inst, ""), law, prov, inst)
	key := ledgerKey(lawName, provName, inst, build)
	entry, found := e.entries[key]
	state := "current"
	switch {
	case !found:
		state = "missing"
	case digest != entry.Digest:
		state = "stale"
	}
	if found {
		e.read[key] = true
	}
	ok := state == "current" && admitLevel(entry.Verdict, entry.By, lawRewriteClass)
	file, line, col := e.file, e.line, e.col
	site := fmt.Sprintf("%s:%d:%d %s", file, line, col, key)
	if s := e.sites[site]; s == nil {
		note := "ergo.laws: " + key + " is " + state + stdUntypedWhy() + "; run ergo test"
		if state == "stale" && entry.With != with {
			note = "ergo.laws: " + key + " was " + entry.Verdict + " with " + orNone(entry.With) + ", and the sources now give " + orNone(with) + stdUntypedWhy() + "; run ergo test"
		}
		e.sites[site] = &RewriteSite{
			File: file, Line: line, Column: col,
			Law: lawName, Provider: provName, Instance: inst, Build: build, Verdict: entry.Verdict, By: entry.By, With: with, State: state,
		}
		e.notes[site] = note
		e.order = append(e.order, site)
	}
	if ok && law.Rewrite != nil {
		e.sites[site].Applied = true
	}
	switch {
	case ok:
		return true, ""
	case state != "current":
		return false, e.notes[site]
	case entry.Verdict == "refuted":
		e.refused[fmt.Sprintf("ergo.laws: law %s is refuted, and the sources are those it was refuted on (%s:%d:%d): the build stops; correct the law or what it reaches, then run ergo test", key, file, line, col)] = true
		e.sites[site].Why = "law " + key + " is refuted"
		return false, e.sites[site].Why
	}
	why := "law " + key + " is " + entry.Verdict + ", not proved: only proved laws change code"
	if entry.Verdict == "validated" {
		why = "law " + key + " is validated with no run named: a validated law changes code only with the run that licensed it"
	}
	r := e.rule(law, prov)
	if open := constructionWhy(prov, r.ctor, r.conv, (*openConstruction).at); open != "" {
		why += "; " + open
	}
	e.sites[site].Why = why
	return false, why
}

func orNone(with string) string {
	if with == "" {
		return "no foreign key"
	}
	return with
}

func (e *LedgerEvidence) digest(key string, law *check.Law, prov *check.Named, inst string) (string, string, string) {
	if d, ok := e.digests[key]; ok {
		return d[0], d[1], d[2]
	}
	r := e.rule(law, prov)
	d, with, build := LawDigest(law, prov, r.conv, r.ctor, inst, e.keys)
	e.digests[key] = [3]string{d, with, build}
	return d, with, build
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
		if s.State == "current" || seen[e.notes[k]] {
			continue
		}
		seen[e.notes[k]] = true
		out = append(out, e.notes[k])
	}
	sort.Strings(out)
	return out
}

// Refusals are the lines of the current refuted entries the build met,
// sorted: each stops the build, locked or not (LAWFFI r2 §1, Admission).
func (e *LedgerEvidence) Refusals() []string {
	out := make([]string, 0, len(e.refused))
	for l := range e.refused {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// Applied is the count of windows the build rewrote.
func (e *LedgerEvidence) Applied() int { return e.applied }

// record is the producer record's Go file: the sha256 of the entries the
// build read (Read) and the rewrites applied, the windows' and, by level,
// the rewrites over functions (levels), kept in the binary by a store in
// an init function.
func (e *LedgerEvidence) record(l *lowerer, applied int, levels map[string]int) []byte {
	byLevel := ""
	for _, level := range slices.Sorted(maps.Keys(levels)) {
		applied += levels[level]
		byLevel += fmt.Sprintf(", %s %d", level, levels[level])
	}
	if byLevel != "" {
		byLevel = " (over functions" + byLevel + ")"
	}
	if r := l.canon.funcRw; r != nil && r.every > 0 && !r.test {
		// The canary's mode (LAWFFI r2 §3.7): the record names it.
		byLevel += fmt.Sprintf("; -rewrites=canary=%d", r.every)
	}
	e.applied = applied
	sum := sha256.Sum256([]byte(e.Read()))
	text := fmt.Sprintf("%s entries sha256:%x; rewrites applied %d%s", LedgerFile, sum, applied, byLevel)
	rec, kept := l.names.alloc("ergo__Laws"), l.names.alloc("ergo__LawsKept") // pkgnames.go, rule 3
	return fmt.Appendf(nil, "package main\n\n// %s is the producer record of the law ledgers this binary was built\n// with (runtime.md, Tests 10).\nvar %s = %q\n\nvar %s string\n\nfunc init() { %s = %s }\n", rec, rec, "ergo producer: "+text, kept, kept, rec)
}

// LedgerEntries are the ledger's entries from the check runs of ev, the
// evidence RewriteChecks returned for u, whose packages names names: for
// each rewrite or post law and provider whose run printed a verdict, one
// with no instance and one per site instance the run took, for the root's
// providers and its imports' (importRules). A law no run took writes
// nothing. Each verdict but skipped names its run, the law-check build
// (native), and each entry the foreign keys of the Go code it reaches.
func LedgerEntries(u Unit, names LedgerNames, ev RewriteEvidence, keys ForeignKeys) []LedgerEntry {
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
			by := "native"
			if verdict == "skipped" {
				by = ""
			}
			for _, inst := range insts {
				d, with, build := LawDigest(r.law, prov, r.conv, r.ctor, inst, keys)
				out = append(out, LedgerEntry{Law: names.law(r.law), Provider: names.provider(prov), Instance: inst, Build: build, Verdict: verdict, By: by, With: with, Digest: d})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}
