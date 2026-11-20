package check_test

import (
	"fmt"
	"math/rand"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// namedRowProgram is a seeded program over a named failure row F of two or
// three of the error types E1 to E4 (types.md, Error types 4): a function
// that fails F, a handler typed by F that a row parameter bound by the
// function's row takes (recoverWith), values of F flowing to and from the
// caught row value, a catch arm of F or of each member, and a match over F
// that may leave one member out (short). spelled is F's set as a row.
func namedRowProgram(r *rand.Rand) (src, spelled string, short bool) {
	members := []string{"E1", "E2", "E3", "E4"}
	r.Shuffle(len(members), func(i, j int) { members[i], members[j] = members[j], members[i] })
	members = members[:2+r.Intn(2)]
	slices.Sort(members)
	spelled = strings.Join(members, " | ")
	var b strings.Builder
	b.WriteString("type E1 {}\n\ntype E2 {}\n\ntype E3 {}\n\ntype E4 {}\n\n")
	b.WriteString("type F = " + spelled + "\n\n")
	b.WriteString("func recoverWith[T, E, E2](f func() T fails E, h func(e E) T fails E2) T fails E2 {\n\tlet v = f() catch {\n\tcase err:\n\t\treturn h(err)\n\t}\n\tv\n}\n\n")
	// The failing function writes its row as F or as one member and F.
	row := "F"
	if r.Intn(2) == 0 {
		row = members[0] + " | F"
	}
	fmt.Fprintf(&b, "func look(n int) int fails %s {\n", row)
	for i, m := range members {
		fmt.Fprintf(&b, "\tif n == %d {\n\t\treturn %s{}\n\t}\n", i, m)
	}
	b.WriteString("\tn\n}\n\n")
	b.WriteString("func keep(e F) F { e }\n\n")
	if r.Intn(2) == 0 {
		b.WriteString("func use() int { recoverWith(func() int fails F { look(1) }, func(e F) int { 0 }) }\n\n")
	}
	// A match over F, every member a case unless short.
	drop := -1
	if r.Intn(3) == 0 {
		drop, short = r.Intn(len(members)), true
	}
	b.WriteString("func which(n int) int {\n\tlook(n) catch {\n\tcase err:\n\t\tlet named F = err\n\t\tmatch keep(named) {\n")
	for i, m := range members {
		if i != drop {
			fmt.Fprintf(&b, "\t\tcase %s:\n\t\t\t%d\n", m, i)
		}
	}
	b.WriteString("\t\t}\n\t}\n}\n\n")
	// A catch whose arm tests F, or each member.
	if r.Intn(2) == 0 {
		b.WriteString("func caught(n int) int {\n\tlook(n) catch {\n\tcase F as f:\n\t\twhich(0) + one(f)\n\t}\n}\n\nfunc one(e F) int { 1 }\n\n")
	} else {
		b.WriteString("func caught(n int) int {\n\tlook(n) catch {\n")
		for i, m := range members {
			fmt.Fprintf(&b, "\tcase %s:\n\t\t%d\n", m, i)
		}
		b.WriteString("\t}\n}\n\n")
	}
	b.WriteString("func main() {}\n")
	return b.String(), spelled, short
}

var namedRowUse = regexp.MustCompile(`\bF\b`)

// namedRowReference is src with F written as its set wherever it is used,
// and its declaration dropped.
func namedRowReference(src, spelled string) string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(line, "type F = ") {
			continue
		}
		out = append(out, namedRowUse.ReplaceAllString(line, spelled))
	}
	return strings.Join(out, "\n")
}

// TestNamedRowChecksAsItsRow is NR1's property (types.md, Error types 4;
// NAMED-ROWS): a named failure row is its row, so a seeded program typed by
// the name checks exactly as the program with each use written as the
// row's set: a handler typed by the name fits a row parameter its row
// binds, values cross between the name and the caught row value with no
// conversion, a catch arm of the name covers its members, and a match
// over the name left short is the same S-code as over the set.
// ERGO_NAMED_ROW_SEED replays one seed.
//
// Mutants: nr-named-row-not-unfolded (unify.go rowOf binds the name, not
// its members) and nr-named-row-ident-nominal (intern.go gives the name its
// own id) each make a seed's named program report S108 where its
// reference reports nothing.
func TestNamedRowChecksAsItsRow(t *testing.T) {
	seeds := 200
	first := int64(1)
	if s := os.Getenv("ERGO_NAMED_ROW_SEED"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, seeds = v, 1
	}
	short, clean := 0, 0
	for seed := first; seed < first+int64(seeds); seed++ {
		src, spelled, isShort := namedRowProgram(rand.New(rand.NewSource(seed)))
		ref := namedRowReference(src, spelled)
		got, want := aliasCodes(src), aliasCodes(ref)
		if !slices.Equal(got, want) || isShort != (len(want) > 0) {
			t.Fatalf("seed %d (ERGO_NAMED_ROW_SEED=%d): the named program reports %v, its reference %v (a match left short: %v)\n--- named program\n%s\n--- reference\n%s", seed, seed, got, want, isShort, src, ref)
		}
		if isShort {
			short++
		} else {
			clean++
		}
	}
	if seeds > 1 && (short == 0 || clean == 0) {
		t.Fatalf("the seeds left %d matches short and %d whole: the property must compare both", short, clean)
	}
}
