package lower

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The domain comes from CallKindCount, not the fixtures. Each obligation
// states rows.md Footprints' rule, checks a source that isolates it, and
// supplies a recursion-axis shape. Missing obligations fail before the
// property can claim completeness. A retired kind has no source shape. A
// kind that is never lowered has a source whose every call of it stands in
// a body no build lowers, and no recursion shape.
type i1CallRule struct {
	name, behavior string
	src            string
	graph          string
	generated      *i1Graph
	retired        bool
	// neverLowered says why no build lowers the kind.
	neverLowered string
}

var i1CallRules = map[check.CallKind][]i1CallRule{
	check.CallFunc: {
		{name: "known-pure", behavior: "transparent-pure", src: "func leaf(n int) int { n }\nfunc subject(n int) int { leaf(n) }", graph: "self"},
		{name: "known-capability", behavior: "transparent-capability", src: "func leaf(n int) int needs Probe { Probe.Tick(); n }\nfunc subject(n int) int needs Probe { leaf(n) }", graph: "self"},
		{name: "host", behavior: "opaque", src: "func subject(n int) int { let xs = []int{}; let _ = xs.Append(n); n }", graph: "host", generated: i1OpaqueGraph("host", "let xs = []int{}; let _ = xs.Append(n)")},
		{name: "extern", behavior: "opaque", src: "func subject(n int) int { let s LiveScheduler; s.Sleep(0s); n }", graph: "extern", generated: i1OpaqueGraph("extern", "let s LiveScheduler; s.Sleep(0s)")},
	},
	check.CallOp: {
		{name: "operation-value", behavior: "opaque", src: "type Shape { Area(n int) int }\nfunc subject(s Shape, n int) int { s.Area(n) }", graph: "op-value", generated: &i1Graph{
			name: "op-value", opaque: true, frames: []string{"walk", "Sq.Area"},
			decls: func(row, clause string) string {
				// The operation's row is empty. Its own with discharges
				// walk's row, independently of walk's footprint clause.
				return "type Shape { Area(n int) int }\ntype Sq {}\nfunc Area(s Sq, n int) int { with Local { walk(s, n) } }\n" +
					i1Fib("walk(s Shape, n int)", "s.Area(n - 1)", "s.Area(n - 2)")(row, clause)
			}, call: "walk(Sq, %d)",
		}},
	},
	check.CallProviderOp: {
		{name: "capability", behavior: "capability", src: "func subject(n int) int needs Probe { Probe.Tick(); n }", graph: "op-cycle"},
	},
	check.CallFuncValue: {
		{name: "unknown", behavior: "opaque", src: "func subject(cb func(int) int, n int) int { cb(n) }", graph: "value-reentry"},
		{name: "immediate-pure", behavior: "transparent-pure", src: "func subject(n int) int { func() int { n }() }", graph: "lit-in-place"},
		{name: "immediate-capability", behavior: "transparent-capability", src: "func subject(n int) int needs Probe { func() int { Probe.Tick(); n }() }", graph: "lit-in-place"},
	},
	check.CallConversion: {
		{name: "conversion", behavior: "pure", src: "func subject(n int) int64 { int64(n) }", graph: "conversion", generated: &i1Graph{name: "conversion", decls: i1Fib("left(n int)", "int(left(n - 1))", "int(left(n - 2))"), call: "left(%d)", frames: []string{"left"}}},
	},
	check.CallLen: {
		{name: "len", behavior: "pure", src: "func subject(n int) int { len([]int{n}) }", graph: "len", generated: &i1Graph{name: "len", decls: i1Fib("left(n int)", "len([]int{left(n - 1)})", "len([]int{left(n - 2)})"), call: "left(%d)", frames: []string{"left"}}},
	},
	check.CallWith: {
		{name: "pure-with", behavior: "transparent-pure", src: "func subject(n int) int { with Local { n } }", graph: "with-body"},
		{name: "capability-with", behavior: "transparent-capability", src: "func subject(n int) int { with Local { Probe.Tick(); n } }", graph: "with-body"},
		{name: "opaque-builder", behavior: "opaque", src: "func makeSilent() Silent { let xs = []int{}; let _ = xs.Append(1); Silent }\nlet Loud = context { Probe: makeSilent() }\nfunc subject(n int) int { with Loud { n } }", graph: "context-reentry", generated: i1OpaqueGraph("context-reentry", "with Loud {}")},
	},
	check.CallPrelude: {{name: "retired", behavior: "pure", retired: true}},
	check.CallLawOp: {{name: "law-operation", behavior: "opaque",
		src:          "type Codec {\n\tEncode(n int) int,\n\n\tlaw Stable(n int) { Encode(n) == Encode(n) },\n}",
		neverLowered: "laws are not run (#162)"}},
}

// A Seq is an implicit call of a function value, not a CallKind. Keep it
// beside the explicit call rules so implicit re-entry cannot disappear
// behind the ordinary loop-form axis.
var i1SeqRule = i1CallRule{
	name: "seq", behavior: "opaque", src: "func subject(s Seq[int], n int) int { for _ in s {}; n }", graph: "seq-call",
	generated: i1OpaqueGraph("seq-call", "let s = Seq[int](func(yield func(int) bool) { let _ = yield(1) }); for _ in s {}"),
}

func i1OpaqueGraph(name, effect string) *i1Graph {
	return &i1Graph{name: name, opaque: true, frames: []string{"left"}, call: "left(%d)", decls: func(row, clause string) string {
		defs := ""
		if name == "context-reentry" {
			defs = "func makeSilent() Silent { let xs = []int{}; let _ = xs.Append(1); Silent }\nlet Loud = context { Probe: makeSilent() }\n"
		}
		return defs + i1Fib("left(n int)", "left(n - 1)", "left(n - 2)")(row, "if n < 0 { "+effect+" }\n"+clause)
	}}
}

func i1CallGraphs() []i1Graph {
	var out []i1Graph
	for kind := check.CallKind(0); kind < check.CallKindCount; kind++ {
		for _, r := range i1CallRules[kind] {
			if r.generated != nil {
				out = append(out, *r.generated)
			}
		}
	}
	return append(out, *i1SeqRule.generated)
}

func TestRuntime199CallKindCoverage(t *testing.T) {
	cases := i1RecursionCases()
	live := i1LivePrograms()
	checkRule := func(t *testing.T, kind check.CallKind, r i1CallRule, seq bool) {
		if r.behavior == "" {
			t.Fatal("missing rows.md footprint rule")
		}
		if r.retired {
			if kind != check.CallPrelude || r.graph != "" || r.src != "" {
				t.Fatal("only CallPrelude is retired; new kinds require a source and recursion shape")
			}
			return // Its classifier is tested in check's compatibility test.
		}
		if r.neverLowered != "" {
			// never lowered: laws are not run (#162). The kind stands only
			// in a law body, which neither build lowers (stage E3b-2 proves
			// both builds are free of laws).
			if kind != check.CallLawOp || r.graph != "" || r.generated != nil {
				t.Fatal("only CallLawOp is never lowered; a lowered kind requires a recursion shape")
			}
			pkg, diags := check.CheckSource(i1Defs + r.src + "\nfunc main() {}\n")
			if len(diags) > 0 {
				t.Fatalf("checker diagnostics %v", diags)
			}
			inLaw := map[*syntax.CallExpr]bool{}
			for _, l := range pkg.Laws {
				syntax.Inspect(l.Decl.Body, func(n syntax.Node) bool {
					if call, ok := n.(*syntax.CallExpr); ok {
						inLaw[call] = true
					}
					return true
				})
			}
			seen := 0
			for x, call := range pkg.Info.Calls {
				if call.Kind != kind {
					continue
				}
				seen++
				if !inLaw[x] {
					t.Errorf("a call of kind %d at %d stands outside a law body", kind, x.Pos())
				}
			}
			if seen == 0 {
				t.Fatalf("source does not exercise call kind %d", kind)
			}
			return
		}
		o := mustLower(t, "callkind", i1Defs+r.src+"\nfunc main() {}\n")
		var subject *syntax.FuncDecl
		for _, f := range o.pkg.Funcs {
			if f.Name == "subject" {
				subject = f.Decl
			}
		}
		if subject == nil {
			t.Fatal("no subject frame")
		}
		seen := false
		syntax.Inspect(subject, func(n syntax.Node) bool {
			if call, ok := n.(*syntax.CallExpr); ok && o.pkg.Info.Calls[call].Kind == kind && !seq {
				seen = true
			}
			if loop, ok := n.(*syntax.RangeStmt); ok && seq && o.pkg.Info.Loops[loop].Domain == check.LoopSeq {
				seen = true
			}
			return true
		})
		if !seen {
			t.Fatalf("source does not exercise call kind %d (Seq %v)", kind, seq)
		}
		fp, ok := o.pkg.Info.Footprints[subject]
		caps := 0
		if strings.HasSuffix(r.behavior, "capability") {
			caps = 1
		}
		opaque := r.behavior == "opaque"
		if !ok || len(fp.Caps) != caps || fp.Tasks || fp.Opaque != opaque || fp.EntryPoll != opaque {
			t.Errorf("%s: subject footprint %+v, want caps=%d tasks=false opaque=%v entry=%v", r.behavior, fp, caps, opaque, opaque)
		}
		for _, cap := range fp.Caps {
			if n, ok := cap.(*check.Named); !ok || n.Obj.Name != "Probe" {
				t.Errorf("footprint carries %v, want Probe", cap)
			}
		}
		graphs := 0
		for _, c := range cases {
			if strings.HasPrefix(c.name, "recursion/"+r.graph+"/") {
				graphs++
				// Run the actual axis, including pure and impure cells.
				t.Run(c.name, func(t *testing.T) {
					o := mustLower(t, "graph", c.src)
					// A graph name is insufficient: the recursive frame
					// itself must exercise the promised kind, not a helper
					// or an unrelated literal elsewhere in the fixture.
					seen := false
					for _, f := range o.pkg.Funcs {
						if _, target := c.entries[f.Name]; !target || f.Decl == nil {
							continue
						}
						syntax.Inspect(f.Decl.Body, func(n syntax.Node) bool {
							if _, nested := n.(*syntax.FuncLit); nested {
								return false
							}
							if call, ok := n.(*syntax.CallExpr); ok && !seq && o.pkg.Info.Calls[call].Kind == kind {
								seen = true
							}
							if loop, ok := n.(*syntax.RangeStmt); ok && seq && o.pkg.Info.Loops[loop].Domain == check.LoopSeq {
								seen = true
							}
							return true
						})
					}
					if !seen {
						t.Errorf("recursion frame never exercises kind %d (Seq %v)", kind, seq)
					}
					i1CheckPolls(t, o, c)
				})
			}
		}
		if r.graph == "" || graphs == 0 {
			t.Errorf("no generated recursion-axis shape for call kind %d, rule %s", kind, r.name)
		}
		if opaque && live["recursion-"+r.graph] == "" {
			t.Errorf("opaque rule %s has no live stop witness", r.name)
		}
	}
	for kind := check.CallKind(0); kind < check.CallKindCount; kind++ {
		rules := i1CallRules[kind]
		if len(rules) == 0 {
			t.Errorf("call kind %d has no footprint/recursion obligation", kind)
		}
		for _, r := range rules {
			t.Run(fmt.Sprintf("kind-%d/%s", kind, r.name), func(t *testing.T) { checkRule(t, kind, r, false) })
		}
	}
	for kind := range i1CallRules {
		if kind < 0 || kind >= check.CallKindCount {
			t.Errorf("obligation for out-of-domain call kind %d", kind)
		}
	}
	t.Run("LoopSeq", func(t *testing.T) { checkRule(t, 0, i1SeqRule, true) })
}
