package check

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// contextInfoPrelude declares two capabilities, a provider record that
// satisfies both through its functions (196m), and a function that needs
// both.
const contextInfoPrelude = `type Store { Get() string }
type Log { Put(s string) }
type Mem { V string }
func Get(m Mem) string { return m.V }
func Put(m Mem, s string) {}
func Run() string needs Store, Log {
	Log.Put("x")
	return Store.Get()
}
`

// withSummary writes each with site of src, in source order, as the
// provider text bound to each discharged capability, then the overrides:
// "Live: Log=Mem{V: "log"} Store=Mem{V: "pkg"}; Store: Mem{V: "a"} by Mem{V: "b"}".
func withSummary(t *testing.T, src string) []string {
	t.Helper()
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("want a clean check, got %s", describeDiags(diags))
	}
	if gaps := infoGaps(file, src, pkg.Info); len(gaps) > 0 {
		t.Fatalf("Info gaps:\n\t%s", strings.Join(gaps, "\n\t"))
	}
	text := func(x syntax.Expr) string { return src[x.Pos():x.End()] }
	type site struct {
		pos  int
		line string
	}
	var sites []site
	for x, w := range pkg.Info.Withs {
		var parts []string
		for i, d := range w.Discharged {
			parts = append(parts, fmt.Sprintf("%s=%s", strings.TrimPrefix(typeString(d), "main."), text(w.Entries[i].Provider)))
		}
		line := w.Contexts[0].Name + ": " + strings.Join(parts, " ")
		// The overrides live on each context (Context.Overrides): the with's
		// context first, then the contexts it embeds, each once.
		var over []string
		seen := map[*Context]bool{}
		var walk func(context *Context)
		walk = func(context *Context) {
			if seen[context] {
				return
			}
			seen[context] = true
			for _, o := range context.Overrides {
				over = append(over, strings.TrimPrefix(typeString(o.Cap), "main.")+": "+text(o.Entry.Provider)+" by "+text(o.By.Provider))
			}
			for _, e := range context.Entries {
				if e.Embedded != nil {
					walk(e.Embedded)
				}
			}
		}
		walk(w.Contexts[0])
		if len(over) > 0 {
			line += "; " + strings.Join(over, ", ")
		}
		sites = append(sites, site{int(x.Pos()), line})
	}
	sort.Slice(sites, func(i, j int) bool { return sites[i].pos < sites[j].pos })
	var out []string
	for _, s := range sites {
		out = append(out, s.line)
	}
	return out
}

// TestContextInfo (spec D-A4; round-1 review M4, round-2 review M1): a named
// context's bindings are in Info, so the lowering reads them and never
// re-derives a resolution. Each with holds exactly one provider entry per
// discharged capability, after replace and embedding; the providers a
// replace overrode are in Overridden with the entry that replaced each.
func TestContextInfo(t *testing.T) {
	cases := []struct {
		name, contexts string
		want           []string
	}{
		{"own entries and an embedding", `let Base = wire { Log: Mem{V: "log"} }
let Live = wire { Store: Mem{V: "pkg"}, Base }
`, []string{`Live: Log=Mem{V: "log"} Store=Mem{V: "pkg"}`}},
		{"an entry of two capabilities", `let Both = wire { Store, Log: Mem{V: "both"} }
`, []string{`Both: Log=Mem{V: "both"} Store=Mem{V: "both"}`}},
		{"a replace of an embedded entry", `let Live = wire { Store: Mem{V: "live"}, Log: Mem{V: "log"} }
let Test = wire { replace Store: Mem{V: "test"}, Live }
`, []string{`Test: Log=Mem{V: "log"} Store=Mem{V: "test"}; Store: Mem{V: "live"} by Mem{V: "test"}`}},
		{"a replace through two levels of embedding", `let Shared = wire { Store: Mem{V: "live"} }
let Accounts = wire { Log: Mem{V: "log"}, Shared }
let Fixture = wire { replace Store: Mem{V: "fixture"}, Accounts }
`, []string{`Fixture: Log=Mem{V: "log"} Store=Mem{V: "fixture"}; Store: Mem{V: "live"} by Mem{V: "fixture"}`}},
		{"a replace of a replace", `let Shared = wire { Store: Mem{V: "live"}, Log: Mem{V: "log"} }
let Fixture = wire { replace Store: Mem{V: "fixture"}, Shared }
let Outer = wire { replace Store: Mem{V: "outer"}, Fixture }
`, []string{`Outer: Log=Mem{V: "log"} Store=Mem{V: "outer"}; Store: Mem{V: "fixture"} by Mem{V: "outer"}, Store: Mem{V: "live"} by Mem{V: "fixture"}`}},
		// A shared entry replaced for one capability still binds the other
		// (round-3 review m1): it is in Entries for Log, overridden for
		// Store only.
		{"a replace of one capability of a shared entry", `let Both = wire { Store, Log: Mem{V: "both"} }
let W = wire { replace Store: Mem{V: "one"}, Both }
`, []string{`W: Log=Mem{V: "both"} Store=Mem{V: "one"}; Store: Mem{V: "both"} by Mem{V: "one"}`}},
		// Round-4 review M1: a with lists only what its block's row needs;
		// the context's other bindings stay on the shared context.
		{"a with lists only what its block needs", `let Live = wire { Store: Mem{V: "pkg"}, Log: Mem{V: "log"} }
func b() string {
	return with Live { Store.Get() }
}
`, []string{`Live: Store=Mem{V: "pkg"}`, `Live: Log=Mem{V: "log"} Store=Mem{V: "pkg"}`}},
		// An inner with discharges what it binds; the outer one only the
		// rest (rows.md, With 3).
		{"nested withs split the block's needs", `let Inner = wire { Store: Mem{V: "inner"} }
let Outer = wire { Store: Mem{V: "outer"}, Log: Mem{V: "log"} }
func b() string {
	return with Outer { with Inner { Run() } }
}
`, []string{`Outer: Log=Mem{V: "log"}`, `Inner: Store=Mem{V: "inner"}`, `Outer: Log=Mem{V: "log"} Store=Mem{V: "outer"}`}},
		// Two embeddings that bring one entry and a replace of it: one
		// override per overridden entry.
		{"a replaced diamond overrides its entry once", `let Base = wire { Store: Mem{V: "base"} }
let L = wire { Base }
let R = wire { Base }
let W = wire { replace Store: Mem{V: "w"}, Log: Mem{V: "log"}, L, R }
`, []string{`W: Log=Mem{V: "log"} Store=Mem{V: "w"}; Store: Mem{V: "base"} by Mem{V: "w"}`}},
		{"a replace of two embedded entries", `let A = wire { Store: Mem{V: "a"} }
let B = wire { Store: Mem{V: "b"}, Log: Mem{V: "log"} }
let W = wire { replace Store: Mem{V: "w"}, B, A }
`, []string{`W: Log=Mem{V: "log"} Store=Mem{V: "w"}; Store: Mem{V: "a"} by Mem{V: "w"}, Store: Mem{V: "b"} by Mem{V: "w"}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The with uses the last context declared.
			var name string
			for _, line := range strings.Split(tc.contexts, "\n") {
				if strings.HasPrefix(line, "let ") {
					name = strings.Fields(line)[1]
				}
			}
			src := contextInfoPrelude + tc.contexts + "func a() string {\n\treturn with " + name + " { Run() }\n}\n"
			got := withSummary(t, src)
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Fatalf("want\n\t%s\ngot\n\t%s", strings.Join(tc.want, "\n\t"), strings.Join(got, "\n\t"))
			}
		})
	}
}

// TestContextInfoLinear: the work that resolves the withs' bindings and the
// Info that holds them are linear in the source (round 3, "Runaway
// allocation"; round-3 and round-4 review M1).
//
// A with's bindings are the context pass's per-component summaries, whose
// nodes carry their providing entries (contextsum.go): no second structure
// is built after the pass, and a with records only the capabilities its
// block's charges found (With.Discharged), with the entries the discharge
// test read. So two counters bound everything:
//   - the counted work: every operation of the context pass, repeatable or
//     single-pass (contextOp), at most contextInfoWorkPerUnit × (context nodes +
//     withs), on top of the pass's own two-part bound (bounded);
//   - the Info size: Discharged and Entries summed over every with, and
//     each context's Overrides, at most contextInfoSizePerUnit × the source's
//     syntax nodes.
//
// The shapes are the round-4 reviewer's owned chain (W0 binds C0, Wi binds
// Ci and embeds W(i-1), one with per context: at 3f5d6ac1 each with held i
// bindings, n²/2 in all), the round-3 with chain, and the budget's entry
// chain, large-base chain, wide fan-in and replaced diamond. Each also
// pins its with and override counts. Timing is accept's scale leg's, not
// go test's (round-4 ruling 4).
func TestContextInfoLinear(t *testing.T) {
	const n = 4000
	for _, tc := range []struct {
		shape     string
		src       func() string
		withs     int
		overrides int // summed over the contexts
	}{
		{"owned chain", func() string { return ownedChain(n) }, n, 0},
		{"with chain", func() string { return withChain(n) }, n, 0},
		{"entry chain", func() string { return contextFamily("entry chain", n) }, 1, 0},
		{"large-base chain", func() string { return contextFamily("large-base chain", n) }, 1, 0},
		{"wide fan-in", func() string { return contextFamily("wide fan-in", n) }, 1, 0},
		{"replace diamond", func() string { return contextFamily("replace diamond", n) }, 1, n - 1},
	} {
		t.Run(tc.shape, func(t *testing.T) {
			src := tc.src()
			oc := countOps(t)
			diags, c := checkLimited(t, src, nil)
			if len(diags) > 0 {
				t.Fatalf("want a clean check, got %s", describeDiags(diags[:1]))
			}
			oc.bounded(t, tc.shape, src, c)
			pkg := c.pkg
			if len(pkg.Info.Withs) != tc.withs {
				t.Fatalf("want %d withs, got %d", tc.withs, len(pkg.Info.Withs))
			}
			m := c.contexts
			work, units := oc.repeat[m]+oc.single[m], contextNodes(t, src)+len(pkg.Info.Withs)
			if work > contextInfoWorkPerUnit*units {
				t.Errorf("the wire pass did %d operations for %d units, over %d per unit", work, units, contextInfoWorkPerUnit)
			}
			size, overrides := 0, 0
			for _, w := range pkg.Info.Withs {
				size += len(w.Discharged) + len(w.Entries)
				if len(w.Entries) != len(w.Discharged) || len(w.Discharged) != 1 {
					t.Fatalf("a with of %d capabilities and %d entries; its block needs one", len(w.Discharged), len(w.Entries))
				}
				if e := w.Entries[0]; e == nil || !listsCap(e, w.Discharged[0]) || w.Contexts[0].Provider(w.Discharged[0]) != e {
					t.Fatalf("the entry for %s does not provide it", typeString(w.Discharged[0]))
				}
			}
			for _, w := range pkg.Contexts {
				size += len(w.Overrides)
				overrides += len(w.Overrides)
			}
			if overrides != tc.overrides {
				t.Errorf("%d overrides, want %d", overrides, tc.overrides)
			}
			if nodes := syntaxNodes(t, src); size > contextInfoSizePerUnit*nodes {
				t.Errorf("Info holds %d with bindings and overrides for %d syntax nodes, over %d per node", size, nodes, contextInfoSizePerUnit)
			}
			t.Logf("%d operations for %d units (%.1f each); Info size %d", work, units, float64(work)/float64(units), size)
		})
	}
}

// contextInfoWorkPerUnit bounds the context pass's operations per context node and
// with (TestContextInfoLinear): the pass's summaries cost O(log n) per
// capability added along a chain (contextBudgetPerUnit's comment), so the
// constant leaves room for log₂ 4000 = 12 a few times over.
const contextInfoWorkPerUnit = 48

// contextInfoSizePerUnit bounds the with bindings and overrides Info holds per
// syntax node (TestContextInfoLinear).
const contextInfoSizePerUnit = 1

// syntaxNodes counts src's syntax nodes.
func syntaxNodes(t *testing.T, src string) int {
	t.Helper()
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	syntax.Inspect(file, func(syntax.Node) bool { n++; return true })
	return n
}

// withChain is the reviewer's chain (round-3 review M1): W0 binds Store,
// each Wi embeds W(i-1), and one function per context runs with Wi.
func withChain(n int) string {
	var b strings.Builder
	b.WriteString("type Store { M() }\ntype P {}\nfunc M(_ P) {}\nlet W0 = wire { Store: P }\n")
	for i := 1; i < n; i++ {
		fmt.Fprintf(&b, "let W%d = wire { W%d }\n", i, i-1)
	}
	for i := range n {
		fmt.Fprintf(&b, "func F%d() { with W%d { Store.M() } }\n", i, i)
	}
	return b.String()
}

// ownedChain is the round-4 reviewer's shape (gen-contexts.ts owned): W0
// binds Store and C0, each Wi binds Ci and embeds W(i-1), and one function
// per context runs with Wi a body that needs Store.
func ownedChain(n int) string {
	var b strings.Builder
	b.WriteString("type Store { Get() string }\ntype Mem { V string }\nfunc Get(m Mem) string { return m.V }\nfunc Run() string needs Store {\n\treturn Store.Get()\n}\n")
	for i := range n {
		fmt.Fprintf(&b, "type C%d { Get() string }\n", i)
	}
	b.WriteString("let W0 = wire { Store: Mem{V: \"w0\"}, C0: Mem{V: \"c0\"} }\n")
	for i := 1; i < n; i++ {
		fmt.Fprintf(&b, "let W%d = wire { C%d: Mem{V: \"c\"}, W%d }\n", i, i, i-1)
	}
	for i := range n {
		fmt.Fprintf(&b, "func f%d() string {\n\treturn with W%d { Run() }\n}\n", i, i)
	}
	return b.String()
}

// TestContextInfoConversions: each provider entry has a conversion per
// capability (the 196m table: Store.Get is Get(Mem)), and a provider of
// one capability has its ConvOperations Implicit.
func TestContextInfoConversions(t *testing.T) {
	src := contextInfoPrelude + `let Live = wire { Store: Mem{V: "pkg"}, Log: Mem{V: "log"} }
let Both = wire { Store, Log: Mem{V: "both"} }
`
	pkg, diags := CheckSource(src)
	if len(diags) > 0 {
		t.Fatalf("want a clean check, got %s", describeDiags(diags))
	}
	table := func(conv Conversion) string {
		var parts []string
		for _, op := range []string{"Get", "Put"} {
			if fn := conv.Table[op]; fn != nil {
				parts = append(parts, op+"="+fn.Name)
			}
		}
		return strings.Join(parts, ",")
	}
	store := pkg.Contexts["Live"].Entries[0]
	if conv := store.Conversions[0]; conv.Kind != ConvOperations || table(conv) != "Get=Get" {
		t.Fatalf("Live's Store conversion: kind %d, table %s", conv.Kind, table(conv))
	}
	if conv, ok := pkg.Info.Implicit[store.Provider]; !ok || table(conv) != "Get=Get" {
		t.Fatalf("Live's Store provider has no Implicit conversion")
	}
	both := pkg.Contexts["Both"].Entries[0]
	if got := table(both.Conversions[0]) + ";" + table(both.Conversions[1]); got != "Get=Get;Put=Put" {
		t.Fatalf("Both's conversions: %s", got)
	}
}
