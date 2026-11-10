package lower

import (
	"ergo.local/prototype/internal/sketch/check"
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
		{name: "host", behavior: "opaque", src: "func subject(n int) int { let _ = hostTrim(\"\"); n }", graph: "host", generated: i1OpaqueGraph("host", "let _ = hostTrim(\"\")")},
		{name: "extern", behavior: "opaque", src: "func subject(n int) int { let s = NewLiveScheduler(); s.Sleep(0s); n }", graph: "extern", generated: i1OpaqueGraph("extern", "let s = NewLiveScheduler(); s.Sleep(0s)")},
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
	check.CallGoError: {
		{name: "go-is", behavior: "pure", src: "func subject(n int) int { len([]bool{go.Is(go.Error{}, go.Error{})}) + n }", graph: "go-is", generated: &i1Graph{name: "go-is", decls: i1Fib("left(n int)", `len([]bool{go.Is(go.Error{Text: "\(left(n - 1))"}, go.Error{})})`, `len([]bool{go.Is(go.Error{Text: "\(left(n - 2))"}, go.Error{})})`), call: "left(%d)", frames: []string{"left"}}},
	},
	check.CallWith: {
		{name: "pure-with", behavior: "transparent-pure", src: "func subject(n int) int { with Local { n } }", graph: "with-body"},
		{name: "capability-with", behavior: "transparent-capability", src: "func subject(n int) int { with Local { Probe.Tick(); n } }", graph: "with-body"},
		{name: "opaque-builder", behavior: "opaque", src: "func makeSilent() Silent { let _ = hostTrim(\"\"); Silent }\nlet Loud = context { Probe: makeSilent() }\nfunc subject(n int) int { with Loud { n } }", graph: "context-reentry", generated: i1OpaqueGraph("context-reentry", "with Loud {}")},
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
	// The extern effect makes the host's LiveScheduler, which needs Ambient
	// (std-align S2b), so a written row of its frames lists Ambient too.
	ambient := name == "extern"
	return &i1Graph{name: name, opaque: true, ambient: ambient, frames: []string{"left"}, call: "left(%d)", decls: func(row, clause string) string {
		if ambient && row != "" {
			row += " | Ambient"
		}
		defs := ""
		if name == "context-reentry" {
			defs = "func makeSilent() Silent { let _ = hostTrim(\"\"); Silent }\nlet Loud = context { Probe: makeSilent() }\n"
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
