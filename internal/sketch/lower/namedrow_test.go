package lower

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

// namedRowSource is a seeded program over a named failure row F of two or
// three of E1 to E4, beside a failure Other that F does not hold: a
// function failing F | Other, a catch that tests F (bound or not) before
// Other, a handler typed by F that a row parameter takes, and a match over
// F. spelled is F's set.
func namedRowSource(r *rand.Rand) (src, spelled string) {
	members := []string{"E1", "E2", "E3", "E4"}
	r.Shuffle(len(members), func(i, j int) { members[i], members[j] = members[j], members[i] })
	members = members[:2+r.Intn(2)]
	slices.Sort(members)
	spelled = strings.Join(members, " | ")
	var b strings.Builder
	b.WriteString("type E1 {}\n\ntype E2 {}\n\ntype E3 {}\n\ntype E4 {}\n\ntype Other {}\n\n")
	b.WriteString("type F = " + spelled + "\n\n")
	b.WriteString("func recoverWith[T, E, E2](f func() T fails E, h func(e E) T fails E2) T fails E2 {\n\tlet v = f() catch {\n\tcase err:\n\t\treturn h(err)\n\t}\n\tv\n}\n\n")
	b.WriteString("func look(n int) int fails F | Other {\n")
	for i, m := range members {
		fmt.Fprintf(&b, "\tif n == %d {\n\t\treturn %s{}\n\t}\n", i, m)
	}
	fmt.Fprintf(&b, "\tif n == %d {\n\t\treturn Other{}\n\t}\n\tn\n}\n\n", len(members))
	b.WriteString("func only(n int) int fails F {\n\tlook(n) catch {\n\tcase F as f:\n\t\treturn f\n\tcase Other:\n\t\t-1\n\t}\n}\n\n")
	b.WriteString("func which(e F) int {\n\tmatch e {\n")
	for i, m := range members {
		fmt.Fprintf(&b, "\tcase %s:\n\t\t%d\n", m, i)
	}
	b.WriteString("\t}\n}\n\n")
	if r.Intn(2) == 0 {
		b.WriteString("func caught(n int) int {\n\tlook(n) catch {\n\tcase F:\n\t\t1\n\tcase Other:\n\t\t2\n\t}\n}\n\n")
	} else {
		b.WriteString("func caught(n int) int {\n\tlook(n) catch {\n\tcase Other:\n\t\t2\n\tcase F as f:\n\t\twhich(f)\n\t}\n}\n\n")
	}
	b.WriteString("func use(n int) int { recoverWith(func() int fails F { only(n) }, func(e F) int { which(e) }) }\n\n")
	b.WriteString("func main() {}\n")
	return b.String(), spelled
}

var (
	namedRowWord = regexp.MustCompile(`\bF\b`)
	goLineMarks  = regexp.MustCompile(`(?m)^//line .*\n|/\*line [^*]*\*/`)
)

// namedRowGo is a lowered program's Go with its line directives dropped,
// and, for the named program, F's declaration dropped and F written as
// Go's error, the form a named row lowers to.
func namedRowGo(code string, named bool) string {
	code = goLineMarks.ReplaceAllString(code, "")
	if !named {
		return code
	}
	code = strings.Replace(code, "type F = error\n\n", "", 1)
	return namedRowWord.ReplaceAllString(code, "error")
}

// TestNamedRowLowersAsItsRow is NR1's lowering property (types.md, Error
// types 4; NAMED-ROWS): a named failure row is its row, so a seeded
// program typed by the name lowers to the Go of the program with each use
// written as the row's set, up to the name itself (Go's error) and line
// directives. Both are Go that go/types accepts. ERGO_NAMED_ROW_SEED
// replays one seed.
//
// Mutants: nr-row-lowers-sealed (namedRow false: the name is a sealed
// interface, its own Go type) and nr-row-case-whole (caseGoTypes lists
// the name, Go's error, which takes every failure: catch's case F would
// take Other) each change the named program's Go away from its
// reference's.
func TestNamedRowLowersAsItsRow(t *testing.T) {
	seeds := 24
	first := int64(1)
	if s := os.Getenv("ERGO_NAMED_ROW_SEED"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, seeds = v, 1
	}
	var programs []*lowered
	for seed := first; seed < first+int64(seeds); seed++ {
		src, spelled := namedRowSource(rand.New(rand.NewSource(seed)))
		ref := namedRowWord.ReplaceAllString(strings.Replace(src, "type F = "+spelled+"\n\n", "", 1), spelled)
		a := mustLower(t, "named"+strconv.FormatInt(seed, 10), src)
		b := mustLower(t, "spelled"+strconv.FormatInt(seed, 10), ref)
		if got, want := namedRowGo(a.mainGo(), true), namedRowGo(b.mainGo(), false); got != want {
			t.Fatalf("seed %d (ERGO_NAMED_ROW_SEED=%d): the named program's Go is not its reference's\n--- named program\n%s\n--- its Go\n%s\n--- reference Go\n%s", seed, seed, src, got, want)
		}
		programs = append(programs, a)
	}
	typecheckGo(t, programs)
}
