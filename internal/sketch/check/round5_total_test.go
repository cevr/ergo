package check_test

import (
	"fmt"
	"math/rand"
	"runtime"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// D16 as amended in round 5: the totality solver is deterministic for a
// given source (Maranget's necessity heuristic, ties broken by source
// column), counts its steps against a fixed budget, refuses over budget
// (S103), and never accepts an unproven switch. Reordering the subjects
// may change whether a switch near the budget is refused, never a proved
// verdict. These tests replace round 4's order-invariance tests: the same
// source gives the same steps and diagnostics, and every verdict, in every
// column order, agrees with an independent DPLL oracle.

// ---------------------------------------------------------------- the oracle

// dpllMissing reports whether some value vector matches q (-1 is _) and
// none of rows. It is a DPLL search over multi-valued variables: a row is
// the clause "some subject differs from this row's variant", unit
// propagation removes a variant from a subject's domain when one cell of
// a row is left, and the search branches on a cell of an open row. It
// shares no code with the checker's matrix solver.
func dpllMissing(rows [][]int, q []int, sizes []int) bool {
	dom := make([]uint64, len(sizes))
	for j, n := range sizes {
		if q[j] >= 0 {
			dom[j] = 1 << q[j]
		} else {
			dom[j] = 1<<n - 1
		}
	}
	return dpll(rows, dom)
}

func dpll(rows [][]int, dom []uint64) bool {
	for _, d := range dom {
		if d == 0 {
			return false
		}
	}
	for {
		changed := false
		open, best := -1, 0
		for i, r := range rows {
			satisfied, undetermined, last := false, 0, -1
			for j, v := range r {
				if v < 0 {
					continue
				}
				switch {
				case dom[j]&(1<<v) == 0:
					satisfied = true
				case dom[j] != 1<<v:
					undetermined++
					last = j
				}
				if satisfied {
					break
				}
			}
			if satisfied {
				continue
			}
			switch undetermined {
			case 0:
				return false // the row matches every vector left
			case 1:
				dom[last] &^= 1 << r[last]
				if dom[last] == 0 {
					return false
				}
				changed = true
			default:
				// Branch on the most constrained open row.
				if open < 0 || undetermined < best {
					open, best = i, undetermined
				}
			}
		}
		if changed {
			continue
		}
		if open < 0 {
			return true // every row is satisfied: a vector escapes them all
		}
		r := rows[open]
		for j, v := range r {
			if v < 0 || dom[j]&(1<<v) == 0 || dom[j] == 1<<v {
				continue
			}
			// First the branch that escapes this row, then the one that
			// keeps it open.
			rest := append([]uint64(nil), dom...)
			rest[j] &^= 1 << v
			if dpll(rows, rest) {
				return true
			}
			fixed := append([]uint64(nil), dom...)
			fixed[j] = 1 << v
			return dpll(rows, fixed)
		}
		return false
	}
}

// ---------------------------------------------------------------- the soundness check

// soundSwitch checks src, one type switch whose case rows (in source
// order, one case per line) are rows over subjects of sizes variants, and
// holds every verdict against the oracle:
//   - a case reported unreachable is covered by the cases before it;
//   - unless reachability was refused, every other case is reachable;
//   - without default, "total" has no missing vector, "not total" names a
//     vector no case matches;
//   - a refusal (S103 "too many cases") is allowed, never a wrong answer.
//
// It returns the switch's verdict.
func soundSwitch(t *testing.T, name, src string, rows [][]int, sizes []int, hasDefault bool, variant func(string) int) string {
	t.Helper()
	_, diags := check.CheckSource(src)
	caseLine := map[int]int{}
	defaultLine := 0
	for i, line := range strings.Split(src, "\n") {
		l := strings.TrimSpace(line)
		if l == "case _:" {
			defaultLine = i + 1
		} else if strings.HasPrefix(l, "case ") {
			caseLine[i+1] = len(caseLine)
		}
	}
	deadDefault := false
	if len(caseLine) != len(rows) {
		t.Fatalf("%s: %d case lines for %d rows", name, len(caseLine), len(rows))
	}
	unreachable := map[int]bool{}
	refusedReach, refusedTotal := false, false
	var missing string
	for _, d := range diags {
		switch {
		case strings.Contains(d.Message, "already covered") && d.Line == defaultLine:
			deadDefault = true // D-B4: a case _ the cases before it cover
		case strings.Contains(d.Message, "already covered"):
			k, ok := caseLine[d.Line]
			if !ok {
				t.Fatalf("%s: an unreachable diagnostic off a case line: %s", name, d)
			}
			unreachable[k] = true
		case strings.Contains(d.Message, "which cases of this match are reachable"):
			refusedReach = true
		case strings.Contains(d.Message, "too many cases to prove this match total"):
			refusedTotal = true
		case strings.Contains(d.Message, "no case handles"):
			missing = d.Message
		default:
			t.Fatalf("%s: unexpected diagnostic %s", name, d)
		}
	}
	for k, r := range rows {
		reach := dpllMissing(rows[:k], r, sizes)
		if unreachable[k] && reach {
			t.Fatalf("%s: case %d is reported unreachable but a value reaches it\n%s", name, k, src)
		}
		if !unreachable[k] && !refusedReach && !reach {
			t.Fatalf("%s: case %d is covered by the cases before it, unreported\n%s", name, k, src)
		}
	}
	if refusedReach {
		return "refused-reachability"
	}
	escapes := dpllMissing(rows, wildRow(len(sizes)), sizes)
	if hasDefault {
		if refusedTotal || missing != "" {
			t.Fatalf("%s: a switch with default got a totality diagnostic: %s", name, describe(diags))
		}
		if deadDefault == escapes {
			t.Fatalf("%s: case _ reported dead = %v, but the cases before it miss a value = %v\n%s", name, deadDefault, escapes, src)
		}
		return "total"
	}
	switch {
	case refusedTotal:
		return "refused"
	case missing != "":
		if !escapes {
			t.Fatalf("%s: reported not total, but the cases cover every value: %s\n%s", name, missing, src)
		}
		w := witnessOf(missing, len(sizes), variant)
		for _, r := range rows {
			if matches(r, w) {
				t.Fatalf("%s: the named missing value %v (%s) is matched by case %v\n%s", name, w, missing, r, src)
			}
		}
		return "not total"
	default:
		if escapes {
			t.Fatalf("%s: accepted as total, but a value escapes every case\n%s", name, src)
		}
		return "total"
	}
}

func wildRow(n int) []int {
	r := make([]int, n)
	for j := range r {
		r[j] = -1
	}
	return r
}

func matches(r, v []int) bool {
	for j, p := range r {
		if p >= 0 && p != v[j] {
			return false
		}
	}
	return true
}

// witnessOf reads the value an S103 message names: "no case handles X" or
// "no case handles (X, Y, …)".
func witnessOf(msg string, n int, variant func(string) int) []int {
	s := msg[strings.Index(msg, "handles ")+len("handles "):]
	s = s[:strings.Index(s, ";")]
	s = strings.TrimSuffix(strings.TrimPrefix(s, "("), ")")
	parts := strings.Split(s, ", ")
	w := make([]int, n)
	for j := range w {
		p := parts[j]
		w[j] = variant(p[strings.LastIndex(p, ".")+1:]) // main.V0 is V0
	}
	return w
}

// vVariant reads matrixSwitch's variant names V0, V1, ….
func vVariant(s string) int {
	var k int
	fmt.Sscanf(s, "V%d", &k)
	return k
}

func permuteRows(rows [][]int, order []int) [][]int {
	out := make([][]int, len(rows))
	for i, r := range rows {
		out[i] = make([]int, len(order))
		for k, j := range order {
			out[i][k] = r[j]
		}
	}
	return out
}

func withDefault(src string) string {
	return strings.Replace(src, "\t}\n\treturn 0\n}\n", "\tcase _:\n\t\treturn 0\n\t}\n\treturn 0\n}\n", 1)
}

func twos(n int) []int {
	s := make([]int, n)
	for j := range s {
		s[j] = 2
	}
	return s
}

// cycleRows are the equality constraints of disjoint cycles over binary
// subjects: for each edge (v, v+1) of a cycle, the rows (A, A) and (B, B).
// With half=true only the (A, A) rows remain. Every column is alike to
// colour refinement, but the cycles of different lengths are not
// interchangeable (round-4 M2).
func cycleRows(half bool, lengths ...int) ([][]int, []int) {
	n := 0
	for _, k := range lengths {
		n += k
	}
	var rows [][]int
	offset := 0
	for _, k := range lengths {
		for v := 0; v < k; v++ {
			for ctor := 0; ctor < 2; ctor++ {
				if half && ctor == 1 {
					continue
				}
				r := wildRow(n)
				r[offset+v], r[offset+(v+1)%k] = ctor, ctor
				rows = append(rows, r)
			}
		}
		offset += k
	}
	return rows, twos(n)
}

// hornRows are the reviewer's regular Horn matrices: case i is A at i and
// i+1 and B at target[i]; every case has a private witness (all A except
// target[i]), so every case is reachable, and default covers the rest.
func hornRows(n int, rng *rand.Rand) [][]int {
	var target []int
	for {
		target = rng.Perm(n)
		ok := true
		for i, j := range target {
			if j == i || j == (i+1)%n {
				ok = false
				break
			}
		}
		if ok {
			break
		}
	}
	rows := make([][]int, n)
	for i := range rows {
		r := wildRow(n)
		r[i], r[(i+1)%n], r[target[i]] = 0, 0, 1
		rows[i] = r
	}
	return rows
}

// ---------------------------------------------------------------- the tests

// TestTotalitySoundness: random matrices of 2 to 12 subjects, random
// 3-CNF matrices at the satisfiability threshold (near the budget),
// regular cycle matrices whose columns colour refinement cannot tell
// apart, and the reviewer's Horn matrices with default, each in several
// column orders and with rows shuffled. Every verdict in every order is
// sound by the oracle; refusals are allowed and counted.
func TestTotalitySoundness(t *testing.T) {
	rng := rand.New(rand.NewSource(20261027))
	type matrix struct {
		name       string
		sizes      []int
		rows       [][]int
		hasDefault bool
	}
	var ms []matrix
	for trial := 0; trial < 200; trial++ {
		n := 2 + rng.Intn(11)
		sizes := make([]int, n)
		for j := range sizes {
			sizes[j] = 2 + rng.Intn(2)
		}
		rows := make([][]int, 1+rng.Intn(40))
		for i := range rows {
			r := wildRow(n)
			for j := range r {
				if rng.Intn(3) > 0 {
					r[j] = rng.Intn(sizes[j])
				}
			}
			rows[i] = r
		}
		ms = append(ms, matrix{fmt.Sprintf("random%d", trial), sizes, rows, rng.Intn(4) == 0})
	}
	for _, n := range []int{24, 40, 72} {
		var rows [][]int
		for i := 0; i < n*43/10; i++ {
			r := wildRow(n)
			for _, j := range rng.Perm(n)[:3] {
				r[j] = rng.Intn(2)
			}
			rows = append(rows, r)
		}
		ms = append(ms, matrix{fmt.Sprintf("3cnf%d", n), twos(n), rows, false})
	}
	for _, ls := range [][]int{{4, 3}, {6, 3}, {8, 5}, {4, 4, 3}, {10, 7}} {
		for _, half := range []bool{false, true} {
			rows, sizes := cycleRows(half, ls...)
			ms = append(ms, matrix{fmt.Sprintf("cycles%v-half%v", ls, half), sizes, rows, false})
		}
	}
	for _, n := range []int{12, 24, 40, 60} {
		ms = append(ms, matrix{fmt.Sprintf("horn%d", n), twos(n), hornRows(n, rng), true})
	}
	verdicts := map[string]int{}
	for _, m := range ms {
		n := len(m.sizes)
		// Large matrices run near the budget, so they get fewer orders.
		orders := [][]int{identityOrder(n)}
		extra := 3
		if n > 20 {
			extra = 1
		}
		for range extra {
			orders = append(orders, rng.Perm(n))
		}
		for oi, order := range orders {
			rows := permuteRows(m.rows, order)
			sizes := make([]int, n)
			for k, j := range order {
				sizes[k] = m.sizes[j]
			}
			src := matrixSwitch(sizes, rows, identityOrder(n))
			if m.hasDefault {
				src = withDefault(src)
			}
			v := soundSwitch(t, fmt.Sprintf("%s order %d", m.name, oi), src, rows, sizes, m.hasDefault, vVariant)
			verdicts[v]++
			shuffled := append([][]int(nil), rows...)
			rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
			src = matrixSwitch(sizes, shuffled, identityOrder(n))
			if m.hasDefault {
				src = withDefault(src)
			}
			verdicts[soundSwitch(t, fmt.Sprintf("%s order %d shuffled", m.name, oi), src, shuffled, sizes, m.hasDefault, vVariant)]++
		}
	}
	t.Logf("verdicts: %v", verdicts)
	if verdicts["total"] == 0 || verdicts["not total"] == 0 {
		t.Errorf("the matrices miss a verdict: %v", verdicts)
	}
}

// TestDefaultSwitchLinear: a switch over n subjects whose only arm is
// default proves its usefulness with work linear in n, including the
// initial columns. The bytes the whole check allocates also grow linearly,
// counted by the runtime,
// not timed (round-4 M4; D16 as amended in round 5: nothing before the
// counters is superlinear). Round 4's column canonicalization ran here,
// quadratic in n.
func TestDefaultSwitchLinear(t *testing.T) {
	src := func(n int) string {
		return withDefault(matrixSwitch(twos(n), nil, identityOrder(n)))
	}
	bytes := func(s string) uint64 {
		best := uint64(0)
		for range 3 {
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			check.CheckSource(s)
			runtime.ReadMemStats(&after)
			if b := after.TotalAlloc - before.TotalAlloc; best == 0 || b < best {
				best = b
			}
		}
		return best
	}
	for _, n := range []int{1000, 4000} {
		ts, us, d := check.SolverSteps(src(n))
		if len(d) > 0 || len(ts) != 1 || len(us) != 1 || ts[0] > 8*n || us[0] > 8*n {
			t.Fatalf("n=%d: steps %v/%v, diagnostics %s", n, ts, us, describe(d))
		}
	}
	small, large := bytes(src(1000)), bytes(src(4000))
	ratio := float64(large) / float64(small)
	t.Logf("allocated bytes: n=1000 %d, n=4000 %d, ratio %.2f", small, large, ratio)
	if ratio > 6 {
		t.Errorf("4x the subjects allocate %.2fx the bytes (%d vs %d); the check is superlinear before the counters", ratio, large, small)
	}
}
