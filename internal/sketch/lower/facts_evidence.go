package lower

// The evidence rows of ergo inspect facts (EVIDENCE-REPORT; LANG-HEALTH
// §4.3 unit 6): besides the facts the build exports for Go (facts.go),
// every change of code the build makes on evidence other than the
// checker's own proof, each with the line that holds the evidence, so a
// reader sees per build what rests on what:
//
//   - kind "rewrite": a law rewrite the build applied (rewrite.go, the IR
//     passes, the codec writers' windows), its entry's level (proved, or
//     validated by a named run) and its ergo.laws line;
//   - kind "unsafe": a memory-class shortcut the build took (shortcuts.go)
//     and the runtime bodies it links, each a row of
//     internal/residue/testdata/unsafe.txt.
//
// A codec function's windows are placed at its type (windowsAt), so each
// of their rows names a source line.
//
// A vouched view (LAWFFI LF2b), an amortised poll and a poll dropped from
// a placed copy add their kinds here with the units that build them; this
// tree has none of the three.

import (
	"fmt"
	"sort"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// RewriteFacts are the rewrite rows of sites: one per applied site, in
// the order the build met them. ledger is the module ledger's text, read
// for each entry's line; a site whose entry is not in it (a shipped std
// entry) names the std ledger.
func RewriteFacts(sites []RewriteSite, ledger string) []Fact {
	lines := map[string]int{}
	for i, line := range strings.Split(ledger, "\n") {
		if e, ok := parseEntry(strings.TrimSpace(line)); ok {
			if _, dup := lines[e.Key()]; !dup {
				lines[e.Key()] = i + 1
			}
		}
	}
	var out []Fact
	for _, s := range sites {
		if !s.Applied {
			continue
		}
		key := ledgerKey(s.Law, s.Provider, s.Instance, s.Build)
		where := "std law ledger"
		if n, ok := lines[key]; ok {
			where = fmt.Sprintf("%s:%d", LedgerFile, n)
		}
		level := s.Verdict
		if s.By != "" {
			level += " by " + s.By
		}
		holds := s.Law
		if s.Provider != "" {
			holds += " for " + s.Provider
		}
		if s.Instance != "" {
			holds += " at " + s.Instance
		}
		reason := where + ": law " + key
		if s.With != "" {
			reason += " with " + s.With
		}
		out = append(out, Fact{
			Kind: "rewrite", Func: s.Law, At: fmt.Sprintf("%s:%d:%d", s.File, s.Line, s.Column),
			Subjects: []string{}, Holds: holds, Evidence: level, Reason: reason,
		})
	}
	return out
}

// windowsAt places the sites the evidence records next, a codec
// function's windows (the cell writers'), at t's declaration, or at
// site, the function's first use, for a type with none. Before, they had
// no position (:0:0), so inspect could not say where a codec rewrite is.
func (l *lowerer) windowsAt(t check.Type, site syntax.Node) {
	if l.rewrite == nil {
		return
	}
	ev, ok := l.rewrite.ev.(*LedgerEvidence)
	if !ok {
		return
	}
	node, pos := site, site.Pos()
	if n, named := t.(*check.Named); named && n.Obj != nil && n.Obj.Decl != nil {
		node, pos = n.Obj.Decl, n.Obj.Decl.Pos()
	}
	ev.file, ev.line, ev.col = l.locate(node, pos)
}

// unsafeFacts are the unsafe rows of the build: one per runtime body of
// each memory-class shortcut the build took, in the registry's order.
func (l *lowerer) unsafeFacts() []Fact {
	var out []Fact
	for _, s := range shortcuts {
		n := l.stats.Taken[s.name]
		if s.class != memoryClass || len(s.unsafe) == 0 || n == 0 {
			continue
		}
		bodies := append([]string(nil), s.unsafe...)
		sort.Strings(bodies)
		for _, body := range bodies {
			out = append(out, Fact{
				Kind: "unsafe", Func: s.name, At: "build", Subjects: []string{},
				Holds:    fmt.Sprintf("%d sites take %s", n, s.name),
				Evidence: "proved", Reason: "internal/residue/testdata/unsafe.txt: " + body + " (admitted by " + s.admission + ")",
			})
		}
	}
	return out
}
