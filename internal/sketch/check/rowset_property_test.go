package check_test

import (
	"fmt"
	"math/rand"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// rowSetBase declares the elements the generated rows draw from: three
// error types, two named rows that overlap (U and V share E2), two
// capabilities, and a generic type with an error row parameter and one
// with a capability row parameter.
const rowSetBase = `type E1 { N int }
type E2 { N int }
type E3 { N int }
type U = E1 | E2
type V = E2 | E3

type A { Ping() int }
type B { Pong() int }

type Box[F] { Run func() int fails F }
type Job[R] { Run func() int needs R }
`

// rowSetElems are the written elements of an error row argument and the
// set each one means: a named row means its variants, and X, the
// function's own row parameter, is opaque and means itself.
var rowSetElems = map[string][]string{
	"E1": {"E1"}, "E2": {"E2"}, "E3": {"E3"},
	"U": {"E1", "E2"}, "V": {"E2", "E3"}, "X": {"X"},
}

var rowSetCaps = map[string][]string{"A": {"A"}, "B": {"B"}}

// rowSetMeaning is the set a written row means: the union of its
// elements' sets, with no order and no repetition (rows.md, Error rows 1
// and Capability rows 1).
func rowSetMeaning(row []string, elems map[string][]string) string {
	var set []string
	for _, e := range row {
		for _, m := range elems[e] {
			if !slices.Contains(set, m) {
				set = append(set, m)
			}
		}
	}
	slices.Sort(set)
	return strings.Join(set, " | ")
}

// rowSetRow writes a random non-empty row over elems, each element maybe
// repeated, in a random order.
func rowSetRow(r *rand.Rand, elems map[string][]string) []string {
	names := slices.Sorted(func(yield func(string) bool) {
		for k := range elems {
			if !yield(k) {
				return
			}
		}
	})
	n := 1 + r.Intn(4)
	row := make([]string, n)
	for i := range row {
		row[i] = names[r.Intn(len(names))]
	}
	return row
}

// rowSetSame writes another row that means the same set as row: elements
// whose sets lie inside it, in a random order and with repeats, until they
// cover it.
func rowSetSame(r *rand.Rand, row []string, elems map[string][]string) []string {
	want := rowSetMeaning(row, elems)
	var inside []string
	for k, ms := range elems {
		ok := true
		for _, m := range ms {
			if !strings.Contains(" "+want+" ", " "+m+" ") {
				ok = false
			}
		}
		if ok {
			inside = append(inside, k)
		}
	}
	slices.Sort(inside)
	var out []string
	for rowSetMeaning(out, elems) != want || r.Intn(3) == 0 {
		out = append(out, inside[r.Intn(len(inside))])
	}
	return out
}

// TestRowArgsAreSets: a written row argument means the set of its
// elements, so two instances of a generic type are one type exactly when
// their rows mean one set, whatever the order, the repetition (A | A) or
// the overlap of named rows (U | V with U = E1 | E2, V = E2 | E3), with an
// opaque row parameter among them (wf10 F8, owner q1: rows stay sets, no
// refusal of a repeated element). A seeded property against the set model
// rowSetMeaning: each case returns a Box (an error row argument) and a Job
// (a capability row argument) of one row as an instance of another, and
// the checker must accept exactly the cases whose sets agree. The mutant
// it kills that no other test of the package does (measured, F8 request;
// ledger F8-row-arg-keeps-named-row-whole): rowArg keeps a named row whole
// instead of its variants, so Box[U] is not Box[E1 | E2]. A failing case
// prints its source, both verdicts and the replay command.
func TestRowArgsAreSets(t *testing.T) {
	seed := int64(1)
	if s := os.Getenv("ERGO_ROWSET_SEED"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = v
	}
	r := rand.New(rand.NewSource(seed))
	for i := range 200 {
		caseSeed := r.Int63()
		cr := rand.New(rand.NewSource(caseSeed))
		e1 := rowSetRow(cr, rowSetElems)
		e2 := rowSetRow(cr, rowSetElems)
		if cr.Intn(2) == 0 {
			e2 = rowSetSame(cr, e1, rowSetElems)
		}
		c1 := rowSetRow(cr, rowSetCaps)
		c2 := rowSetRow(cr, rowSetCaps)
		if cr.Intn(2) == 0 {
			c2 = rowSetSame(cr, c1, rowSetCaps)
		}
		src := rowSetBase + fmt.Sprintf(`
func errs[X](b Box[%s]) Box[%s] fails X { b }

func caps(j Job[%s]) Job[%s] { j }
`, strings.Join(e1, " | "), strings.Join(e2, " | "), strings.Join(c1, " | "), strings.Join(c2, " | "))
		want := []string{}
		if rowSetMeaning(e1, rowSetElems) != rowSetMeaning(e2, rowSetElems) {
			want = append(want, "errs")
		}
		if rowSetMeaning(c1, rowSetCaps) != rowSetMeaning(c2, rowSetCaps) {
			want = append(want, "caps")
		}
		_, diags := check.CheckSource(src)
		have := []string{}
		for _, d := range diags {
			name := "other"
			switch d.Line {
			case strings.Count(rowSetBase, "\n") + 2:
				name = "errs"
			case strings.Count(rowSetBase, "\n") + 4:
				name = "caps"
			}
			if d.Code != "S108" {
				name += "-" + d.Code
			}
			have = append(have, name)
		}
		if strings.Join(want, " ") != strings.Join(have, " ") {
			t.Fatalf("case %d (seed %d, case seed %d): the set model refuses %v, the checker %v\n%s\n%s\nreplay: ERGO_ROWSET_SEED=%d go test -run TestRowArgsAreSets ./internal/sketch/check",
				i, seed, caseSeed, want, have, describe(diags), src, seed)
		}
	}
}
