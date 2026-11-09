package check_test

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync/atomic"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/testwait"
)

// wrapImporter refuses every non-std path with the error its wrap function
// makes from an *ImportError.
type wrapImporter struct{ wrap func(error) error }

func (i wrapImporter) Import(path string) (*check.Package, error) {
	if _, err := check.Std.Import(path); err == nil {
		return check.Std.Import(path)
	}
	return nil, i.wrap(&check.ImportError{Path: path, Reason: "the folder lib is gone"})
}

// cycleErr unwraps to itself: a custom Unwrap with a cycle.
type cycleErr struct{}

func (e *cycleErr) Error() string { return "cycle" }
func (e *cycleErr) Unwrap() error { return e }

// forkErr is a custom Unwrap with a branching cycle: it unwraps to its
// kids, then to itself fan times. With fan >= 2, a search with no visit
// bound walks about fan^depth errors.
type forkErr struct {
	kids []error
	fan  int
}

func (e *forkErr) Error() string { return "fork" }
func (e *forkErr) Unwrap() []error {
	out := append([]error(nil), e.kids...)
	for range e.fan {
		out = append(out, e)
	}
	return out
}

// joinErr is a join that keeps an empty list (errors.Join() is nil).
type joinErr struct{ errs []error }

func (e *joinErr) Error() string   { return "join" }
func (e *joinErr) Unwrap() []error { return e.errs }

// s104Within checks a program that imports lib against imp and returns its
// S104 messages. It fails the test, and does not wait, when the check has
// not ended by the test's deadline: a search that does not end is a failure, never a
// stall of the package.
func s104Within(t *testing.T, imp check.Importer) []string {
	t.Helper()
	f, err := syntax.Parse("import lib\n\nfunc main() {}\n")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan []string, 1)
	go func() {
		_, ds := check.Check("main", []*syntax.File{f}, imp)
		var got []string
		for _, d := range ds {
			if d.Code == "S104" {
				got = append(got, d.Message)
			}
		}
		done <- got
	}()
	select {
	case got := <-done:
		return got
	case <-testwait.After(t):
		t.Fatal("check.Check did not end by the test's deadline")
		return nil
	}
}

// TestImportErrorWrapped: an importer may wrap its *ImportError (once,
// twice, inside errors.Join, inside a join inside a wrap), and the S104
// diagnostic still carries the reason. An error with no *ImportError, or
// with an Unwrap cycle (one branch or several), gives the plain diagnostic
// and ends.
func TestImportErrorWrapped(t *testing.T) {
	const reason = `unknown package "lib": the folder lib is gone`
	other := errors.New("disk on fire")
	for _, c := range []struct {
		name string
		wrap func(error) error
		want string
	}{
		{"plain", func(e error) error { return e }, reason},
		{"once", func(e error) error { return fmt.Errorf("load: %w", e) }, reason},
		{"twice", func(e error) error { return fmt.Errorf("a: %w", fmt.Errorf("b: %w", e)) }, reason},
		{"join", func(e error) error { return errors.Join(other, e) }, reason},
		{"join in wrap", func(e error) error { return fmt.Errorf("a: %w", errors.Join(other, fmt.Errorf("b: %w", e))) }, reason},
		{"multi %w", func(e error) error { return fmt.Errorf("%w; %w", other, e) }, reason},
		{"none", func(error) error { return fmt.Errorf("load: %w", other) }, `unknown package "lib"`},
		{"cycle", func(error) error { return &cycleErr{} }, `unknown package "lib"`},
		{"cycle joined", func(e error) error { return errors.Join(&cycleErr{}, e) }, reason},
		{"fork", func(error) error { return &forkErr{fan: 2} }, `unknown package "lib"`},
		{"fork of three", func(error) error { return &forkErr{kids: []error{other}, fan: 3} }, `unknown package "lib"`},
		{"fork joined after", func(e error) error { return errors.Join(e, &forkErr{fan: 2}) }, reason},
		{"fork wraps", func(e error) error { return &forkErr{kids: []error{fmt.Errorf("a: %w", e)}, fan: 2} }, reason},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := s104Within(t, wrapImporter{c.wrap})
			if len(got) != 1 || got[0] != c.want {
				t.Fatalf("S104 = %q, want [%q]", got, c.want)
			}
			if c.want != reason && strings.Contains(got[0], ":") {
				t.Fatalf("S104 %q has a reason, want none", got[0])
			}
		})
	}
}

// The search bounds of findImportError in decl.go (maxUnwrapDepth,
// maxUnwrapVisits), restated for the model below.
const (
	unwrapDepth  = 64
	unwrapVisits = 4096
)

type gkind int

const (
	gLeaf   gkind = iota // errors.New
	gImport              // an *ImportError
	gWrap                // fmt.Errorf with one %w
	gJoin                // errors.Join of 0-3 errors
	gMulti               // fmt.Errorf with two %w
	gCycle               // forkErr: its kids, then itself fan (1-3) times
)

// gnode is one error of a generated error graph.
type gnode struct {
	kind gkind
	kids []*gnode
	fan  int
	ie   *check.ImportError
}

func genNode(r *rand.Rand, depth int, ies *int) *gnode {
	if depth >= 5 || r.IntN(4) == 0 {
		if r.IntN(3) == 0 {
			*ies++
			return &gnode{kind: gImport, ie: &check.ImportError{Path: "lib", Reason: fmt.Sprintf("r%d", *ies)}}
		}
		return &gnode{kind: gLeaf}
	}
	n := &gnode{}
	kids := 0
	switch r.IntN(4) {
	case 0:
		n.kind, kids = gWrap, 1
	case 1:
		n.kind, kids = gJoin, r.IntN(4)
	case 2:
		n.kind, kids = gMulti, 2
	default:
		n.kind, kids, n.fan = gCycle, r.IntN(3), 1+r.IntN(3)
	}
	for range kids {
		n.kids = append(n.kids, genNode(r, depth+1, ies))
	}
	return n
}

// build makes the error of n. With flat set, a cycle is built as a join of
// its kids alone: the acyclic graph whose errors.As result the bounded
// search must find, budget permitting.
func (n *gnode) build(flat bool) error {
	kids := make([]error, len(n.kids))
	for i, k := range n.kids {
		kids[i] = k.build(flat)
	}
	switch n.kind {
	case gLeaf:
		return errors.New("leaf")
	case gImport:
		return n.ie
	case gWrap:
		return fmt.Errorf("w: %w", kids[0])
	case gJoin:
		if len(kids) == 0 {
			return &joinErr{}
		}
		return errors.Join(kids...)
	case gMulti:
		return fmt.Errorf("%w; %w", kids[0], kids[1])
	}
	if flat {
		return &joinErr{errs: kids}
	}
	return &forkErr{kids: kids, fan: n.fan}
}

func (n *gnode) String() string {
	var b strings.Builder
	switch n.kind {
	case gLeaf:
		return "leaf"
	case gImport:
		return n.ie.Reason
	case gWrap:
		b.WriteString("wrap(")
	case gJoin:
		b.WriteString("join(")
	case gMulti:
		b.WriteString("multi(")
	case gCycle:
		fmt.Fprintf(&b, "cycle%d(", n.fan)
	}
	for i, k := range n.kids {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(k.String())
	}
	b.WriteString(")")
	return b.String()
}

func (n *gnode) holds(x *check.ImportError) bool {
	if n.ie == x {
		return true
	}
	for _, k := range n.kids {
		if k.holds(x) {
			return true
		}
	}
	return false
}

func (n *gnode) cyclic(minFan int) bool {
	if n.kind == gCycle && n.fan >= minFan {
		return true
	}
	for _, k := range n.kids {
		if k.cyclic(minFan) {
			return true
		}
	}
	return false
}

// visitModel counts the errors a depth-first search with the depth bound
// visits, by definition of the unrolled tree: an error at depth d (d below
// the bound) is one visit plus its kids' searches at d+1, and a cycle also
// searches itself at d+1 fan times; an error at the bound is one visit.
type visitModel struct {
	memo map[struct {
		n *gnode
		d int
	}]int
}

// cost is the visits of a whole search of n at depth d (n holds no
// *ImportError the search stops at), capped so it never overflows.
func (m *visitModel) cost(n *gnode, d int) int {
	if d >= unwrapDepth {
		return 1
	}
	key := struct {
		n *gnode
		d int
	}{n, d}
	if c, ok := m.memo[key]; ok {
		return c
	}
	c := 1
	for _, k := range n.kids {
		c += m.cost(k, d+1)
	}
	if n.kind == gCycle {
		c += n.fan * m.cost(n, d+1)
	}
	c = min(c, 1<<40)
	m.memo[key] = c
	return c
}

// before is the visits before the search reaches x in n, and whether a
// cycle was searched on the way. x is the first *ImportError of the flat
// graph, so every subtree before it holds none, and a cycle above it
// reaches it through its kids before itself.
func (m *visitModel) before(n *gnode, d int, x *check.ImportError) (visits int, cycled bool) {
	if n.ie == x {
		return 0, false
	}
	if !n.holds(x) {
		return m.cost(n, d), n.cyclic(1)
	}
	visits = 1
	for _, k := range n.kids {
		v, c := m.before(k, d+1, x)
		visits, cycled = visits+v, cycled || c
		if k.holds(x) {
			return visits, cycled
		}
	}
	panic("unreachable: n holds x")
}

// graphImporter refuses every non-std path with err.
type graphImporter struct{ err error }

func (i graphImporter) Import(path string) (*check.Package, error) {
	if p, err := check.Std.Import(path); err == nil {
		return p, nil
	}
	return nil, i.err
}

// TestImportErrorGraphs is a property over generated error graphs (seeded,
// depth at most 5, joins of 0-3 errors, cycles that branch to 1-3 copies of
// themselves after their kids, any number of *ImportErrors): the S104
// reason is the one errors.As finds in the graph with each cycle replaced by
// the join of its kids, unless the search spends its visit bound first, and
// the check always ends. With no visit bound a branching cycle never ends;
// with no depth bound a one-branch cycle spends the whole visit bound.
func TestImportErrorGraphs(t *testing.T) {
	const seed, graphs = 0xc2c2, 600
	r := rand.New(rand.NewPCG(seed, seed))
	type tcase struct {
		root *gnode
		want string
	}
	var cases []tcase
	cover := map[string]int{}
	for range graphs {
		ies := 0
		root := genNode(r, 0, &ies)
		if root.kind == gLeaf && r.IntN(2) == 0 {
			root = &gnode{kind: gCycle, fan: 2 + r.IntN(2)}
		}
		var x *check.ImportError
		want := `unknown package "lib"`
		switch {
		case !errors.As(root.build(true), &x):
			if root.cyclic(2) {
				cover["no ImportError, branching cycle"]++
			} else {
				cover["no ImportError"]++
			}
		default:
			m := &visitModel{memo: map[struct {
				n *gnode
				d int
			}]int{}}
			visits, cycled := m.before(root, 0, x)
			found := visits < unwrapVisits
			if found {
				want += ": " + x.Reason
			}
			switch {
			case !found:
				cover["cycle before, bound spent"]++
			case cycled:
				cover["cycle before, found"]++
			case root.cyclic(2):
				cover["branching cycle after, found"]++
			case !root.cyclic(1):
				cover["acyclic, found"]++
			}
		}
		cases = append(cases, tcase{root, want})
	}
	for _, class := range []string{"no ImportError", "no ImportError, branching cycle", "cycle before, bound spent", "cycle before, found", "branching cycle after, found", "acyclic, found"} {
		if cover[class] == 0 {
			t.Fatalf("seed %#x: the generator made no %q graph: %v", seed, class, cover)
		}
	}
	t.Logf("seed %#x: %v", seed, cover)

	var at atomic.Int64
	done := make(chan []string, 1)
	go func() {
		var bad []string
		for i, c := range cases {
			at.Store(int64(i))
			f, err := syntax.Parse("import lib\n\nfunc main() {}\n")
			if err != nil {
				bad = append(bad, err.Error())
				break
			}
			_, ds := check.Check("main", []*syntax.File{f}, graphImporter{c.root.build(false)})
			var got []string
			for _, d := range ds {
				if d.Code == "S104" {
					got = append(got, d.Message)
				}
			}
			if len(got) != 1 || got[0] != c.want {
				bad = append(bad, fmt.Sprintf("graph %d %s: S104 = %q, want [%q]", i, c.root, got, c.want))
			}
		}
		done <- bad
	}()
	select {
	case bad := <-done:
		for i, b := range bad {
			if i == 10 {
				t.Fatalf("... %d more", len(bad)-10)
			}
			t.Error(b)
		}
	case <-testwait.After(t):
		i := at.Load()
		t.Fatalf("seed %#x: check.Check did not end by the test's deadline on graph %d %s", seed, i, cases[i].root)
	}
}
