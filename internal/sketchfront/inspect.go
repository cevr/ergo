// InspectGo is the one core of ergo inspect (IG8; docs/tooling.md,
// inspect): what a checked program reaches of Go. The CLI prints its
// GoReport; an MCP tool, when semantic tooling ports one, prints the same GoReport.
//
// For each function of the program it lists the Go symbols the function's
// code can reach: the Go functions, methods and interface operations it
// calls, names or puts into an operation type, in its body and in the
// function literals written there, and those of every function of the
// program it reaches the same way. A user extern (Level 2) reaches its Go
// target. A std function is a leaf: std is reviewed, and its Go is its own.
// For each symbol it gives the Go declaration, the Ergo signature, the tier
// (pure: the reviewed list, no go.Foreign; foreign: go.Foreign), the
// provenance of its binding, and whether a call can wait in Go where an
// interruption cannot reach it: a foreign call whose Go declaration takes
// no context.Context is an uninterruptible host wait (IG10). Each Go import
// lists the declarations its binding refuses, with the reasons.
//
// It reads the checker's model only (Info.Calls, Info.Uses,
// Info.Implicit) and the bindings' text; it runs no Go tool.
//
// What it does not follow: a call of a function value or of an operation
// of an operation-type value whose provider is chosen elsewhere (a with);
// the provider's functions are reached where the value is made.
//
// Prior art: go list -deps and Go's callgraph tools (static reach over
// declared calls); Elixir's mix xref (calls into other modules, by
// callee); Swift's API notes are the reviewed facts the tier comes from.

package sketchfront

import (
	"fmt"
	"sort"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketch/syntax"
)

// GoReport is what ergo inspect prints.
type GoReport struct {
	Imports      []GoImport          `json:"imports"`
	Functions    []GoFunction        `json:"functions"`
	Symbols      []GoSymbol          `json:"symbols"`
	RewriteSites []lower.RewriteSite `json:"rewriteSites"`
	// Derived are the program's derives (patterns.md, Derivation 7).
	Derived []GoDerived `json:"derived,omitempty"`
}

// InspectRewrites are the rewrite sites of the checked analysis's run
// build (types.md, Laws 14; runtime.md, Tests 10): each site's law and
// provider, its constants, the verdict of its entry in the ledger at
// ledger and the std ledger, the entry's state (current, stale or
// missing), and whether the build applies the rewrite. It lowers the
// program in process and runs no Go tool; source anchors a program of
// one source text.
func InspectRewrites(a Analysis, source, ledger string) ([]lower.RewriteSite, error) {
	if len(a.Diagnostics) > 0 || (a.file == nil && a.program == nil) || a.pkg == nil {
		return nil, &BuildError{Diagnostics: a.Diagnostics}
	}
	unit, err := analysisUnit(a, source)
	if err != nil {
		return nil, err
	}
	text, err := ledgerText(nil, ledger)
	if err != nil {
		return nil, err
	}
	ev, err := ledgerEvidence(a, unit, text, nil)
	if err != nil {
		return []lower.RewriteSite{}, err
	}
	// Every program: a site of a std law over functions needs no law of
	// the program's own.
	unit.Rewrite = ev
	// The IR path, which ergo build takes: its bodies hold the rewrites
	// over functions, and its source bodies the windows. A form the
	// lowering refuses ends the list there; ergo build names the refusal.
	_, _, _ = lower.LowerIR(unit)
	return ev.Sites(), nil
}

// RewritesText is ergo inspect rewrites' table: one line per site, its
// position, its law (and provider), its entry's level, run and foreign
// keys, the entry's state, and whether the build applies it, or why not.
func RewritesText(sites []lower.RewriteSite) string {
	var b strings.Builder
	for _, s := range sites {
		law := s.Law
		if s.Provider != "" {
			law += " for " + s.Provider
		}
		if s.Build != "" {
			law += " under " + s.Build
		}
		level := s.Verdict
		if level == "" {
			level = "none"
		}
		if s.By != "" {
			level += " by " + s.By
		}
		if s.With != "" {
			level += "  with " + s.With
		}
		applied := "applied"
		if !s.Applied {
			applied = "not applied"
			if s.Why != "" && !strings.HasPrefix(s.Why, "not applied") {
				applied += ": " + s.Why
			} else if s.Why != "" {
				applied = s.Why
			}
		}
		fmt.Fprintf(&b, "%s:%d:%d  %s  %s  %s  %s\n", s.File, s.Line, s.Column, law, level, s.State, applied)
	}
	return b.String()
}

// InspectFacts is the fact section of the checked analysis's run build
// (lower/facts.go; facts.md, Lowering 7): each fact the build exports for
// Go's optimizer, by position. It lowers the program in process and runs
// no Go tool; source anchors a program of one source text.
//
// After the facts come the build's evidence rows (lower's
// facts_evidence.go): each law rewrite the run build applies, with its
// ledger entry's level and line in ledger (the module's ergo.laws), and
// each unsafe body its shortcuts link.
func InspectFacts(a Analysis, source, ledger string) ([]lower.Fact, error) {
	if len(a.Diagnostics) > 0 || (a.file == nil && a.program == nil) || a.pkg == nil {
		return nil, &BuildError{Diagnostics: a.Diagnostics}
	}
	unit, err := analysisUnit(a, source)
	if err != nil {
		return nil, err
	}
	facts, unsafe, err := lower.FactsAndUnsafe(unit)
	if err != nil {
		return facts, err
	}
	facts = append(append([]lower.Fact{}, facts...), unsafe...)
	text, err := ledgerText(nil, ledger)
	if err != nil {
		return facts, err
	}
	sites, err := InspectRewrites(a, source, ledger)
	if err != nil {
		return facts, err
	}
	return append(facts, lower.RewriteFacts(sites, text)...), nil
}

// InspectForms are the form sites of the checked analysis's run build on
// the IR path (lower/ir_forms_report.go; IR-FORMS IRF-6): at each site a
// pass over the IR decided, the form, the pass, and whether it fired or
// the reason it did not. It lowers the program in process and runs no Go
// tool, so it needs no patched toolchain; source anchors a program of one
// source text.
func InspectForms(a Analysis, source string) ([]lower.FormSite, error) {
	if len(a.Diagnostics) > 0 || (a.file == nil && a.program == nil) || a.pkg == nil {
		return nil, &BuildError{Diagnostics: a.Diagnostics}
	}
	unit, err := analysisUnit(a, source)
	if err != nil {
		return nil, err
	}
	_, rep, err := lower.LowerIR(unit)
	if err != nil {
		return nil, err
	}
	if rep.FormSites == nil {
		return []lower.FormSite{}, nil
	}
	return rep.FormSites, nil
}

// FormsText is sites as ergo inspect forms prints them, one line each.
func FormsText(sites []lower.FormSite) string { return lower.FormsText(sites) }

// FactsText is facts as ergo inspect facts prints them, one line each.
func FactsText(facts []lower.Fact) string { return lower.FactsText(facts) }

// CopyReport is the copy report of the checked analysis's run build
// (ergo build -report copies, ergo inspect copies; OWN1 and REUSE RU0,
// lower/copyreport.go): each Append, Concat and owned-form site with the
// form it lowers to (in place, one extender, or copy), every other copy
// the lowering writes, each call of an owned body, with why, and the
// functions with owned parameters. It lowers the program in process and runs
// no Go tool; source anchors a program of one source text.
func CopyReport(a Analysis, source string) (*lower.CopyReport, error) {
	if len(a.Diagnostics) > 0 || (a.file == nil && a.program == nil) || a.pkg == nil {
		return nil, &BuildError{Diagnostics: a.Diagnostics}
	}
	unit, err := analysisUnit(a, source)
	if err != nil {
		return nil, err
	}
	report := &lower.CopyReport{}
	unit.Copies = report
	if _, err := lower.Lower(unit); err != nil {
		return nil, err
	}
	return report, nil
}

// GoDerived is one `type T derive E` or mapped content asset: what it produced,
// its mapper identity, media kind, evaluation step budget, and cache hit status.
type GoDerived struct {
	Package   string   `json:"package"`
	Type      string   `json:"type"`
	Plan      string   `json:"plan"`
	Decls     []string `json:"decls"`
	Steps     int      `json:"steps"`
	Kind      string   `json:"kind,omitempty"`
	MediaKind string   `json:"mediaKind,omitempty"`
	CacheHit  bool     `json:"cacheHit,omitempty"`
	CacheKey  string   `json:"cacheKey,omitempty"`
}

// GoImport is one Go package the program imports.
type GoImport struct {
	Path       string      `json:"path"`
	Provenance string      `json:"provenance"`
	Refused    []GoRefusal `json:"refused"`
}

// GoRefusal is a declaration a binding does not import, with its reason.
type GoRefusal struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Wait kinds of a GoSymbol.
const (
	// WaitNone: a reviewed-pure call computes and returns.
	WaitNone = "none"
	// WaitInterruptible: the call takes the task's context (IG10).
	WaitInterruptible = "interruptible"
	// WaitUninterruptible: a foreign call with no context; an interruption
	// takes effect only after it returns.
	WaitUninterruptible = "uninterruptible"
)

// GoSymbol is one Go symbol the program reaches.
type GoSymbol struct {
	// Target is the Go symbol: "net/url.Parse", "net/url.URL.Hostname".
	Target string `json:"target"`
	// Go is its declaration as Go writes it.
	Go string `json:"go"`
	// Ergo is its Ergo signature (Level 1).
	Ergo string `json:"ergo"`
	// Tier is "pure", "memory" (a memory-only Go call, GBX6: no row, not
	// pure) or "foreign".
	Tier       string `json:"tier"`
	Provenance string `json:"provenance"`
	Wait       string `json:"wait"`
}

// GoFunction is one function of the program with a body.
type GoFunction struct {
	Package string `json:"package"`
	Name    string `json:"name"`
	// File is the name of the declaring file in its package; empty for a
	// program of one source text.
	File string `json:"file,omitempty"`
	Line int    `json:"line"`
	// Reach are the Targets of the Go symbols it reaches, sorted.
	Reach []string `json:"reach"`
	// Waits are the Targets of Reach whose Wait is uninterruptible.
	Waits []string `json:"waits"`
}

// reachNode is one function's direct reach.
type reachNode struct {
	fn    *check.Func
	syms  map[string]bool
	edges []*check.Func
}

type reachWalker struct {
	nodes   map[*check.Func]*reachNode
	symbols map[string]GoSymbol
}

// InspectGo inspects the checked packages of a program (its imports first, the
// root last; std packages are skipped).
func InspectGo(pkgs []*check.Package) GoReport {
	w := &reachWalker{nodes: map[*check.Func]*reachNode{}, symbols: map[string]GoSymbol{}}
	r := GoReport{Imports: []GoImport{}, Functions: []GoFunction{}, Symbols: []GoSymbol{}, RewriteSites: []lower.RewriteSite{}}
	seenImport := map[string]bool{}
	var order []*check.Func
	for _, p := range pkgs {
		if p == nil || p.Std() || p.Host() {
			continue
		}
		for _, g := range check.GoImports(p) {
			if seenImport[g.Path] {
				continue
			}
			seenImport[g.Path] = true
			imp := GoImport{Path: g.Path, Provenance: check.GoProvenance(g), Refused: []GoRefusal{}}
			for name, why := range check.GoRefusals(g) {
				imp.Refused = append(imp.Refused, GoRefusal{Name: name, Reason: why})
			}
			sort.Slice(imp.Refused, func(i, j int) bool { return imp.Refused[i].Name < imp.Refused[j].Name })
			r.Imports = append(r.Imports, imp)
		}
		for _, f := range p.Funcs {
			if ergoBody(f) {
				w.direct(f)
				order = append(order, f)
			}
		}
	}
	for _, f := range order {
		reach := w.reach(f)
		fn := GoFunction{Package: f.Pkg.Path, Name: f.Name, Reach: []string{}, Waits: []string{}}
		fn.File, fn.Line = check.FuncPosition(f)
		for _, t := range reach {
			fn.Reach = append(fn.Reach, t)
			if w.symbols[t].Wait == WaitUninterruptible {
				fn.Waits = append(fn.Waits, t)
			}
		}
		r.Functions = append(r.Functions, fn)
	}
	for _, s := range w.symbols {
		r.Symbols = append(r.Symbols, s)
	}
	sort.Slice(r.Symbols, func(i, j int) bool { return r.Symbols[i].Target < r.Symbols[j].Target })
	for _, p := range pkgs {
		if p == nil || p.Std() || p.Host() || p.Derive == nil {
			continue
		}
		for _, rec := range p.Derive.Records {
			planName := rec.MediaKind
			if rec.MapperPkg != "" && rec.MapperFunc != "" {
				planName = rec.MapperPkg + "." + rec.MapperFunc
			} else if rec.MapperPkg != "" {
				planName = rec.MapperPkg
			}
			g := GoDerived{
				Package:   p.Path,
				Type:      rec.Target,
				Plan:      planName,
				Decls:     append([]string{}, rec.Decls...),
				Steps:     int(rec.Steps),
				Kind:      rec.Kind,
				MediaKind: rec.MediaKind,
				CacheHit:  rec.CacheHit,
				CacheKey:  rec.CacheKey,
			}
			r.Derived = append(r.Derived, g)
		}
	}
	return r
}

// body reports a function of the program whose body is Ergo.
func ergoBody(f *check.Func) bool {
	return f != nil && f.Decl != nil && f.Decl.Extern == nil && f.Decl.Body != nil &&
		f.Pkg != nil && !f.Pkg.Std() && !f.Pkg.Host() && f.Pkg.Info != nil
}

// direct is f's direct reach, walked on first use.
func (w *reachWalker) direct(f *check.Func) *reachNode {
	if n := w.nodes[f]; n != nil {
		return n
	}
	n := &reachNode{fn: f, syms: map[string]bool{}}
	w.nodes[f] = n
	info := f.Pkg.Info
	syntax.Inspect(f.Decl.Body, func(x syntax.Node) bool {
		switch x := x.(type) {
		case *syntax.CallExpr:
			if c := info.Calls[x]; c != nil {
				switch c.Kind {
				case check.CallFunc:
					w.reaches(n, c.Func)
				case check.CallOp:
					w.operation(n, info, c)
				}
			}
		case *syntax.Ident:
			if g, ok := info.Uses[x].(*check.Func); ok {
				w.reaches(n, g)
			}
		}
		if e, ok := x.(syntax.Expr); ok {
			w.conversion(n, info.Implicit[e])
			for _, conv := range info.ImplicitElems[e] {
				w.conversion(n, conv)
			}
		}
		return true
	})
	return n
}

// conversion reaches the functions that implement an operation type when
// a value becomes one.
func (w *reachWalker) conversion(n *reachNode, conv check.Conversion) {
	if conv.Kind != check.ConvOperations {
		return
	}
	for _, g := range conv.Table {
		w.reaches(n, g)
	}
}

// reaches adds the function g to n: a Go symbol, the Go target of a user
// extern, or an edge to a function of the program.
func (w *reachWalker) reaches(n *reachNode, g *check.Func) {
	switch {
	case g == nil:
	case g.Pkg.Host():
		n.syms[w.symbol(g)] = true
	case check.GoExternOf(g) != nil && check.GoExternOf(g).Binding != nil:
		n.syms[w.symbol(check.GoExternOf(g).Binding)] = true
	case ergoBody(g):
		n.edges = append(n.edges, g)
	}
}

// operation adds a call of an operation of a Go interface type (io.Reader's
// Read on a host value).
func (w *reachWalker) operation(n *reachNode, info *check.Info, c *check.Call) {
	if c.Op == nil || c.Recv == nil {
		return
	}
	t := info.Types[c.Recv]
	if e, ok := check.GoRefElem(t); ok {
		t = e
	}
	if p, ok := t.(*check.Pointer); ok {
		t = p.Elem
	}
	named, ok := t.(*check.Named)
	if !ok || named.Obj == nil || !named.Obj.Pkg.Host() {
		return
	}
	target := named.Obj.Pkg.Path + "." + named.Obj.Name + "." + c.Op.Name
	if _, ok := w.symbols[target]; !ok {
		s := GoSymbol{Target: target, Go: "interface method " + named.Obj.Name + "." + c.Op.Name, Ergo: c.Op.Sig.String(),
			Provenance: check.GoProvenance(named.Obj.Pkg)}
		s.Tier, s.Wait = goTier(check.GoForeign(c.Op.Sig), false)
		w.symbols[target] = s
	}
	n.syms[target] = true
}

// symbol records the Go function or method g and returns its Target.
func (w *reachWalker) symbol(g *check.Func) string {
	target := g.Decl.Extern.Target.Value
	if _, ok := w.symbols[target]; ok {
		return target
	}
	s := GoSymbol{Target: target, Go: check.GoDecl(g), Provenance: check.GoProvenance(g.Pkg)}
	if g.Sig != nil {
		s.Ergo = g.Sig.String()
	}
	s.Tier, s.Wait = goTier(check.GoForeign(g.Sig), check.GoContext(g))
	if check.GoEffect(g) {
		s.Tier = "memory" // it never waits: WaitNone
	}
	w.symbols[target] = s
	return target
}

func goTier(foreign, context bool) (string, string) {
	switch {
	case !foreign:
		return "pure", WaitNone
	case context:
		return "foreign", WaitInterruptible
	}
	return "foreign", WaitUninterruptible
}

// reach is the sorted set of Targets f reaches, through every function of
// the program it reaches.
func (w *reachWalker) reach(f *check.Func) []string {
	seen := map[*check.Func]bool{f: true}
	syms := map[string]bool{}
	stack := []*check.Func{f}
	for len(stack) > 0 {
		g := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		n := w.direct(g)
		for s := range n.syms {
			syms[s] = true
		}
		for _, e := range n.edges {
			if !seen[e] {
				seen[e] = true
				stack = append(stack, e)
			}
		}
	}
	out := make([]string, 0, len(syms))
	for s := range syms {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
