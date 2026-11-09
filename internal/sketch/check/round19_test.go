package check

import (
	"fmt"
	"iter"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// meterOf checks src as one package and returns its diagnostics and its
// context meter (nil when nothing was summarised).
func meterOf(t *testing.T, src string) ([]Diagnostic, *contextMeter) {
	t.Helper()
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	c := newChecker("main", Std, prelude().scope)
	c.run([]*syntax.File{file})
	return c.sorted(), c.contexts
}

// contextFamily writes one generated context program of size n. A context lists its
// entries before its embedded contexts, as the decided syntax writes them.
func contextFamily(shape string, n int) string {
	var b strings.Builder
	b.WriteString("type P {}\nfunc M(_ P) {}\n")
	caps := n
	if shape == "disjoint" || shape == "large-base chain" {
		caps = 2 * n
	}
	if shape == "empty independent" || shape == "inline embeddings" {
		caps = 1
	}
	for i := range caps {
		fmt.Fprintf(&b, "type C%d { M() }\n", i)
	}
	last := fmt.Sprintf("W%d", n-1)
	switch shape {
	case "entry chain": // every context adds an entry
		b.WriteString("let W0 = context {\n\tC0: P,\n}\n")
		for i := 1; i < n; i++ {
			fmt.Fprintf(&b, "let W%d = context {\n\tC%d: P,\n\tW%d,\n}\n", i, i, i-1)
		}
	case "wide fan-in": // one context embeds n one-entry contexts
		for i := range n {
			fmt.Fprintf(&b, "let X%d = context {\n\tC%d: P,\n}\n", i, i)
		}
		b.WriteString("let W = context {\n")
		for i := range n {
			fmt.Fprintf(&b, "\tX%d,\n", i)
		}
		b.WriteString("}\n")
		last = "W"
	case "disjoint": // the reviewer's ladder: W_i embeds two disjoint chains
		b.WriteString("let A0 = context {\n\tC0: P,\n}\n")
		fmt.Fprintf(&b, "let B0 = context {\n\tC%d: P,\n}\n", n)
		for i := 1; i < n; i++ {
			fmt.Fprintf(&b, "let A%d = context {\n\tC%d: P,\n\tA%d,\n}\nlet B%d = context {\n\tC%d: P,\n\tB%d,\n}\n", i, i, i-1, i, n+i, i-1)
		}
		for i := range n {
			fmt.Fprintf(&b, "let W%d = context {\n\tA%d,\n\tB%d,\n}\n", i, i, i)
		}
	case "large-base chain": // one large context, then a chain adding one entry each
		b.WriteString("let W0 = context {\n")
		for i := range n {
			fmt.Fprintf(&b, "\tC%d: P,\n", i)
		}
		b.WriteString("}\n")
		for i := 1; i < n; i++ {
			fmt.Fprintf(&b, "let W%d = context {\n\tC%d: P,\n\tW%d,\n}\n", i, n+i-1, i-1)
		}
	case "replace diamond": // each layer joins two branches and replaces the repeated entry
		b.WriteString("let W0 = context {\n\tC0: P,\n}\n")
		for i := 1; i < n; i++ {
			fmt.Fprintf(&b, "let L%d = context { W%d }\nlet R%d = context { W%d }\nlet W%d = context {\n\treplace C0: P,\n\tL%d,\n\tR%d,\n}\n", i, i-1, i, i-1, i, i, i)
		}
	case "empty independent": // n contexts with nothing in them (round 19 M1)
		for i := range n {
			fmt.Fprintf(&b, "let W%d = context {}\n", i)
		}
		b.WriteString("func F() { with context { C0: P } { C0.M() } }\n")
		return decided(b.String())
	case "inline fan-in": // an inline context embeds n one-entry contexts and replaces one
		for i := range n {
			fmt.Fprintf(&b, "let X%d = context {\n\tC%d: P,\n}\n", i, i)
		}
		b.WriteString("func F() {\n\twith context {\n\t\treplace C0: P,\n")
		for i := range n {
			fmt.Fprintf(&b, "\t\tX%d,\n", i)
		}
		b.WriteString("\t} { C0.M() }\n}\n")
		return decided(b.String())
	case "multi-key": // one entry binds n capabilities to one provider (round 20)
		b.WriteString("let W = context {\n\t")
		for i := range n {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "C%d", i)
		}
		b.WriteString(": P,\n}\n")
		last = "W"
	case "inline embeddings": // an inline context of n empty embeddings and no entry (round 20)
		b.WriteString("let Empty = context {}\nfunc F() {\n\twith context {\n")
		for range n {
			b.WriteString("\t\tEmpty,\n")
		}
		b.WriteString("\t} {}\n}\n")
		return decided(b.String())
	default:
		panic(shape)
	}
	fmt.Fprintf(&b, "func F() { with %s { C0.M() } }\n", last)
	return decided(b.String())
}

// TestContextBudget: on every embedding shape, the context pass's repeatable
// work stays within the package's budget and was charged before it ran,
// its single-pass work within contextSinglePerUnit per context node (bounded),
// and the verdict is the exact one (acceptance) or the one budget S111
// (D16 as amended in rounds 18 and 20). The disjoint ladder's answer is
// quadratic, so it is refused at every size here; the other shapes are
// accepted, and their charged share of the budget at the largest size is
// at most GrowthRatio times that at the smallest. Each shape runs at three
// sizes up to GrowthMaxNodes source nodes (test-speed plan T4).
//
// Each shape is one case of a split (gate-fast T1): part k checks case k,
// as a top-level test of its own. Under -short a part keeps its first
// case.
var contextBudget = NewSplit(Split[contextBudgetCase]{
	Name: "TestContextBudget", Parts: 9, Short: 2,
	Cases: SeqOf(contextBudgetCases),
	Key: func(c contextBudgetCase) string {
		return fmt.Sprintf("%s refused=%v\n%s", c.shape, c.refused, contextFamily(c.shape, 3))
	},
	Run: runContextBudget,
})

func TestContextBudgetPart0(t *testing.T) { contextBudget.Part(t, 0) }
func TestContextBudgetPart1(t *testing.T) { contextBudget.Part(t, 1) }
func TestContextBudgetPart2(t *testing.T) { contextBudget.Part(t, 2) }
func TestContextBudgetPart3(t *testing.T) { contextBudget.Part(t, 3) }
func TestContextBudgetPart4(t *testing.T) { contextBudget.Part(t, 4) }
func TestContextBudgetPart5(t *testing.T) { contextBudget.Part(t, 5) }
func TestContextBudgetPart6(t *testing.T) { contextBudget.Part(t, 6) }
func TestContextBudgetPart7(t *testing.T) { contextBudget.Part(t, 7) }
func TestContextBudgetPart8(t *testing.T) { contextBudget.Part(t, 8) }

// contextBudgetCase is one shape of TestContextBudget; sizes nil runs it
// at GrowthSizes.
type contextBudgetCase struct {
	shape   string
	sizes   []int
	refused bool
}

func contextBudgetCases() []contextBudgetCase {
	var out []contextBudgetCase
	for _, family := range []struct {
		shape   string
		sizes   []int
		refused bool
	}{
		{"entry chain", nil, false},
		{"wide fan-in", nil, false},
		{"large-base chain", nil, false},
		{"replace diamond", nil, false},
		{"empty independent", nil, false},
		{"inline fan-in", nil, false},
		{"multi-key", nil, false},
		{"inline embeddings", nil, false},
		// The ladder's quadratic answer passes the linear budget only above
		// about 60 rungs, so it runs at one size over GrowthMaxNodes: the
		// verdict under test is the refusal.
		{"disjoint", []int{128}, true},
	} {
		out = append(out, contextBudgetCase{family.shape, family.sizes, family.refused})
	}
	return out
}

func runContextBudget(t *testing.T, cases iter.Seq[contextBudgetCase]) {
	oc := countOps(t)
	for family := range cases {
		sizes := family.sizes
		if sizes == nil {
			sizes = GrowthSizes(t, GrowthMaxNodes, func(n int) int { return sourceNodes(t, contextFamily(family.shape, n)) })
		}
		var share []float64
		for _, n := range sizes {
			src := contextFamily(family.shape, n)
			diags, c := checkLimited(t, src, nil)
			m := c.contexts
			if m == nil {
				t.Fatalf("%s n=%d: no context meter", family.shape, n)
			}
			oc.bounded(t, fmt.Sprintf("%s n=%d", family.shape, n), src, c)
			switch {
			case len(diags) == 0 && !family.refused:
			case len(diags) == 1 && diags[0].Code == CodeDuplicateCap && diags[0].Message == contextBudgetMessage && family.refused:
			default:
				t.Errorf("%s n=%d: want %s, got %v", family.shape, n, map[bool]string{false: "acceptance", true: "the budget S111"}[family.refused], diags)
			}
			t.Logf("%s n=%d: %d of %d steps charged (%.1f%%), %d repeatable and %d single-pass operations, %d diagnostics", family.shape, n, m.used, m.limit, 100*float64(m.used)/float64(m.limit), oc.repeat[m], oc.single[m], len(diags))
			// The limit is contextBudgetPerUnit per unit of context, so the
			// charged share is the work per unit; the summaries are treaps,
			// whose logarithmic factor grows at these small sizes as much as
			// the size does, so the share is also per log2 of the size.
			share = append(share, float64(m.used)/float64(m.limit)/math.Log2(float64(n)))
		}
		if !family.refused {
			GrowthBounded(t, family.shape+" charged context steps per unit and log2 n", sizes, share)
		}
	}
}

// TestContextBudgetRefusal: past the budget, the context being summarised gets
// the one S111 and nothing else follows from it: a with of a refused context
// discharges everything, and a context body summarised later reports no
// duplicate (D3). A context whose own body passes the budget gets the S111 at
// its name.
func TestContextBudgetRefusal(t *testing.T) {
	src := contextFamily("disjoint", 512) + "let Dup = context {\n\tA3,\n\tA2,\n}\nfunc G() { with Dup { C0.M() } }\n"
	diags, m := meterOf(t, src)
	if len(diags) != 1 || diags[0].Message != contextBudgetMessage || !m.out {
		t.Fatalf("want one budget S111, got %v", diags)
	}
	line := strings.Split(src, "\n")[diags[0].Line-1]
	if !strings.HasPrefix(line, "let ") {
		t.Errorf("the budget S111 is at %q, want a context's name", line)
	}
	// Under the budget the same duplicate is found.
	diags, _ = meterOf(t, contextFamily("disjoint", 4)+"let Dup = context {\n\tA3,\n\tA2,\n}\n")
	if len(diags) == 0 || diags[0].Code != CodeDuplicateCap || diags[0].Message == contextBudgetMessage {
		t.Errorf("want the duplicate S111 under the budget, got %v", diags)
	}
}

var usesKeyword = regexp.MustCompile(`\buses\b`)

// TestContextBudgetHeadroom: every fixture, witness, prototype fixture,
// corpus file and example uses under 10% of its context budget, so the
// budget refuses only sources far from the ones written so far. The
// prototype fixtures and the corpus are their decided translations (#196).
func TestContextBudgetHeadroom(t *testing.T) {
	var files []string
	for _, pattern := range []string{
		"testdata/fixtures/*/*.ergo",
		"testdata/classf/*.ergo",
		"testdata/totality/*.ergo",
		"../testdata/*.ergo",
		"../testdata/*/*.ergo",
		"../syntax/testdata/corpus/*/*.ergo",
		"../syntax/testdata/corpus/*/*/*.ergo",
		"../../../examples/*/*/*.ergo",
		"../../../docs/research/ef-migrate/*.ergo",
	} {
		m, _ := filepath.Glob(pattern)
		files = append(files, m...)
	}
	if len(files) < 300 {
		t.Fatalf("found %d sources", len(files))
	}
	oc := countOps(t)
	worst, worstFile, metered := 0.0, "", 0
	worstOps, worstOpsFile := 0.0, ""
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := usesKeyword.ReplaceAllString(string(data), "needs")
		if _, err := syntax.Parse(src); err != nil {
			continue
		}
		_, c := checkLimited(t, src, nil)
		if c.contexts == nil {
			continue
		}
		metered++
		oc.bounded(t, f, src, c)
		share := float64(c.contexts.used) / float64(c.contexts.limit)
		if share >= 0.10 {
			t.Errorf("%s: %d of %d context steps (%.1f%%), want under 10%%", f, c.contexts.used, c.contexts.limit, 100*share)
		}
		if share > worst {
			worst, worstFile = share, f
		}
		if ops := float64(oc.repeat[c.contexts]+oc.single[c.contexts]) / float64(c.contexts.limit); ops > worstOps {
			worstOps, worstOpsFile = ops, f
		}
	}
	t.Logf("%d sources, %d with contexts; the largest charged share is %.2f%% (%s); the most operations of both classes, against the budget, %.2f%% (%s)", len(files), metered, 100*worst, worstFile, 100*worstOps, worstOpsFile)
	// E5 Design B4: a local context's summary is charged to the package's
	// context budget as an inline context's is. A chain of locals, each
	// embedding the one before, and one local named by many withs each
	// charge under 25% of their budget, and check clean.
	for _, shape := range []string{"local chain", "local many"} {
		for _, n := range GrowthSizes(t, GrowthMaxNodes, func(n int) int { return sourceNodes(t, withShape(shape, n)) }) {
			diags, c := checkLimited(t, withShape(shape, n), nil)
			if len(diags) > 0 {
				t.Errorf("%s n=%d: %v", shape, n, diags[0])
				continue
			}
			share := float64(c.contexts.used) / float64(c.contexts.limit)
			t.Logf("%s n=%d: %d of %d context steps (%.1f%%)", shape, n, c.contexts.used, c.contexts.limit, 100*share)
			if share >= 0.25 {
				t.Errorf("%s n=%d: %d of %d context steps (%.1f%%), want under 25%%", shape, n, c.contexts.used, c.contexts.limit, 100*share)
			}
		}
	}
}
