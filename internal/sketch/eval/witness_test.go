package eval

import "testing"

// TestWitnessedCallsRunAtCheckTime: a generic call whose type parameter an
// operation type constrains runs at check time, as the test build runs it
// (lower/witness.go): an operation on a value of the parameter, through a
// borrow (`S lent Counter` at `&b`) or not, calls the function that
// implements it for the argument's type, with the receiver first; a
// parameter passed on to another constrained call keeps its witness; an
// operation-type argument runs its own provider. Before MX-EVWITNESS each
// was skipped ("a generic call with unresolved operation witnesses"). The
// examples that use it (vt's screen laws) stop at the budget at check
// time, so no example verdict reaches the mutants it kills
// (testdata/mutants.json): evwitness-skipped, evwitness-outer-lost,
// evwitness-receiver-dropped.
func TestWitnessedCallsRunAtCheckTime(t *testing.T) {
	pkg := checked(t, `type Counter {
	Get() int,
	mut Add(n int),
}

type box { n int } implements Counter

func Get(b box) int { b.n }

func Add(b mut box, n int) { b.n += n }

type fixed { at int } implements Counter

func Get(f fixed) int { f.at }

func Add(f fixed, n int) {}

// bump adds n to s, then reads it.
func bump[S Counter](s mut S, n int) int {
	s.Add(n)
	s.Get()
}

// twice bumps s through bump: S's witness is twice's own.
func twice[S Counter](s mut S, n int) int {
	let _ = bump(s, n)
	bump(s, n)
}

test ThroughABorrow {
	let mut b = box{n: 1}
	assert bump(&b, 2) == 3
	assert twice(&b, 10) == 23
	assert b.n == 23
}

test ByValue {
	let mut f = fixed{at: 7}
	assert bump(&f, 5) == 7
}

test AnOperationType {
	let mut c Counter = fixed{at: 9}
	assert bump(&c, 1) == 9
	assert twice(&c, 1) == 9
}
`)
	rep, err := Run(pkg, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"ThroughABorrow": true, "ByValue": true, "AnOperationType": true}
	seen := 0
	for _, r := range rep.Results {
		if !want[r.Name] {
			continue
		}
		seen++
		if r.Status != Passed {
			t.Errorf("%s: %s (%s), want passed", r.Name, r.Status, r.Reason)
		}
	}
	if seen != len(want) {
		t.Errorf("%d of the %d tests reported: %+v", seen, len(want), rep.Results)
	}
}

// TestOwnedWritesStayLinear is a growth guard: a function that grows two
// slices of a record through a borrow (g.cells = g.cells.Append(…), each
// new element then written in place), then
// reads each element, a record of basic values, reads a slice's len, and
// writes the element back, does work linear in n at check time, as Go
// does: its steps at 2n are under three times its steps at n. A copy of
// the whole slice on each Append or each write, which each mutant it kills
// makes (testdata/mutants.json: evwitness-handover-local-only,
// evwitness-record-read-lends, evwitness-len-lends), makes the work
// quadratic, about four times. Found on vt's screen laws (MX-EVWITNESS),
// whose cases each copied the screen per cell.
func TestOwnedWritesStayLinear(t *testing.T) {
	pkg := checked(t, `type Cell { A int, B int }

type Grid { cells []Cell, flags []int }

func fill(g mut Grid, n int) {
	for i in 0..n {
		g.cells = g.cells.Append(Cell{A: 0, B: 0})
		g.cells[i] = Cell{A: i, B: 0}
		g.flags = g.flags.Append(0)
	}
	for i in 0..n {
		let c = g.cells[i]
		if len(g.flags) == n {
			g.cells[i] = Cell{A: c.A, B: 1}
		}
	}
}

func work(n int) int {
	let mut g = Grid{cells: []Cell{}, flags: []int{}}
	fill(&g, n)
	let mut sum = 0
	for c in g.cells {
		sum += c.B
	}
	sum
}

test Small {
	assert work(200) == 200
}

test Large {
	assert work(400) == 400
}
`)
	rep, err := Run(pkg, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	steps := map[string]int64{}
	for _, r := range rep.Results {
		if r.Status != Passed {
			t.Fatalf("%s: %s (%s)", r.Name, r.Status, r.Reason)
		}
		steps[r.Name] = r.Steps
	}
	small, large := steps["Small"], steps["Large"]
	if small == 0 || large == 0 {
		t.Fatalf("the tests did not run: %+v", rep.Results)
	}
	if large >= 3*small {
		t.Errorf("steps %d at n=200 and %d at n=400: the work grows faster than n", small, large)
	}
}
