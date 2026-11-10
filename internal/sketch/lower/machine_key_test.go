package lower

import (
	"fmt"
	"math/rand"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// keyGen writes generated Ergo types and values of them for
// TestKeyEqualsEquality. Its values come from small domains, so equal
// values recur, and a Map literal lists its entries in a random order, so
// two equal Maps are often written apart.
type keyGen struct {
	r     *rand.Rand
	decls strings.Builder
	n     int
	sizes map[string]int // the number of values of each family
}

// keyType is a generated type: its Ergo spelling and a function that
// writes a random value of it as an Ergo literal.
type keyType struct {
	name string
	lit  func() string
}

func (g *keyGen) basic() keyType {
	r := g.r
	switch r.Intn(4) {
	case 0:
		return keyType{"int", func() string { return strconv.Itoa(r.Intn(3) - 1) }}
	case 1:
		return keyType{"string", func() string { return strconv.Quote([]string{"", "a", "ab"}[r.Intn(3)]) }}
	case 2:
		return keyType{"bool", func() string { return strconv.FormatBool(r.Intn(2) == 0) }}
	}
	return keyType{"float64", func() string { return []string{"0.5", "1.0", "-2.25"}[r.Intn(3)] }}
}

// typ is a generated type of at most depth d levels.
func (g *keyGen) typ(d int) keyType {
	r := g.r
	if d <= 0 {
		return g.basic()
	}
	switch r.Intn(5) {
	case 0, 3:
		return g.basic()
	case 1:
		e := g.typ(d - 1)
		name := "[]" + e.name
		return keyType{name, func() string {
			parts := make([]string, r.Intn(3))
			for i := range parts {
				parts[i] = e.lit()
			}
			return name + "{" + strings.Join(parts, ", ") + "}"
		}}
	case 2:
		e := g.typ(d - 1)
		name := "Map[string, " + e.name + "]"
		return keyType{name, func() string {
			keys := []string{"x", "y", "z"}[:r.Intn(4)]
			r.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
			parts := make([]string, len(keys))
			for i, k := range keys {
				parts[i] = strconv.Quote(k) + ": " + e.lit()
			}
			return name + "{" + strings.Join(parts, ", ") + "}"
		}}
	case 4:
		g.n++
		name := fmt.Sprintf("KRec%d", g.n)
		fields := make([]keyType, 1+r.Intn(3))
		var decl []string
		for i := range fields {
			fields[i] = g.typ(d - 1)
			decl = append(decl, fmt.Sprintf("F%d %s", i, fields[i].name))
		}
		fmt.Fprintf(&g.decls, "type %s { %s }\n\n", name, strings.Join(decl, ", "))
		return keyType{name, func() string {
			parts := make([]string, len(fields))
			for i, f := range fields {
				parts[i] = fmt.Sprintf("F%d: %s", i, f.lit())
			}
			return name + "{" + strings.Join(parts, ", ") + "}"
		}}
	}
	return g.union(g.typ(d-1), g.typ(d-1))
}

// union is a generated union over a and b, with two variants of one shape
// (A and D), so only the variant tells their values apart.
func (g *keyGen) union(a, b keyType) keyType {
	r := g.r
	g.n++
	name := fmt.Sprintf("KUnion%d", g.n)
	fmt.Fprintf(&g.decls, "type %s = %sA{X %s} | %sB{X %s, Y %s} | %sC{} | %sD{X %s}\n\n", name, name, a.name, name, b.name, a.name, name, name, a.name)
	return keyType{name, func() string {
		switch r.Intn(4) {
		case 0:
			return name + "A{X: " + a.lit() + "}"
		case 2:
			return name + "D{X: " + a.lit() + "}"
		case 1:
			return name + "B{X: " + b.lit() + ", Y: " + a.lit() + "}"
		}
		return name + "C"
	}}
}

// keyFamily writes one family k: a type T, values of it, a machine whose
// states are those values (law Distinct, which a search proves over one
// state more than there are values with distinct keys), and a machine that
// counts the values that are distinct by `==` (law Count, whose witness
// prints the count).
func (g *keyGen) keyFamily(k int) string {
	t := g.typ(3)
	if k == 0 {
		// Two strings side by side, so a key that writes a string with no
		// length ("a" then "", against "" then "a") merges unequal values.
		g.n++
		name := fmt.Sprintf("KRec%d", g.n)
		fmt.Fprintf(&g.decls, "type %s { F0 string, F1 string }\n\n", name)
		str := func() string { return strconv.Quote([]string{"", "a"}[g.r.Intn(2)]) }
		t = keyType{name, func() string { return name + "{F0: " + str() + ", F1: " + str() + "}" }}
	}
	if k == 1 {
		// A union over one small basic type: A{X: v} and D{X: v} are apart
		// by their variant alone.
		small := keyType{"int", func() string { return strconv.Itoa(g.r.Intn(2)) }}
		t = g.union(small, small)
	}
	vals := make([]string, 6+g.r.Intn(10))
	for i := range vals {
		vals[i] = t.lit()
	}
	g.sizes[strconv.Itoa(k)] = len(vals)
	p := fmt.Sprintf("K%d", k)
	var b strings.Builder
	picks := make([]string, len(vals))
	for i := range picks {
		picks[i] = fmt.Sprintf("%sP%d{}", p, i)
	}
	fmt.Fprintf(&b, "type %sPick = %s\n\n", p, strings.Join(picks, " | "))
	fmt.Fprintf(&b, "type %sBox = %sEmpty{} | %sFull{V %s} | %sGone{}\n\n", p, p, p, t.name, p)
	fmt.Fprintf(&b, "func %sVals() []%s {\n\t[]%s{%s}\n}\n\n", p, t.name, t.name, strings.Join(vals, ", "))
	fmt.Fprintf(&b, "func %sStep(s %sBox, e %sPick) Decision[%sBox, int] {\n\tmatch s {\n\tcase %sEmpty:\n\t\tmatch e {\n", p, p, p, p, p)
	for i := range vals {
		fmt.Fprintf(&b, "\t\tcase %sP%d:\n\t\t\tEnter[%sBox]{Next: %sFull{V: %sVals()[%d]}}\n", p, i, p, p, p, i)
	}
	fmt.Fprintf(&b, "\t\t}\n\tcase _:\n\t\tIgnore\n\t}\n}\n\n")
	fmt.Fprintf(&b, "type %sKeys machine[%sBox, %sPick] {\n\tinitial: %sEmpty,\n\tstep:    %sStep,\n\n\tlaw Distinct never reaches %sGone,\n}\n\n", p, p, p, p, p, p)
	fmt.Fprintf(&b, "func %sDistinct() int {\n\tlet vs = %sVals()\n\tlet mut n = 0\n\tfor i, v in vs {\n\t\tlet mut seen = false\n\t\tfor j, w in vs {\n\t\t\tif j < i && w == v {\n\t\t\t\tseen = true\n\t\t\t}\n\t\t}\n\t\tif !seen {\n\t\t\tn = n + 1\n\t\t}\n\t}\n\tn\n}\n\n", p, p)
	fmt.Fprintf(&b, "type %sCount = %sSeen{N int} | %sNever{}\n\ntype %sNone = %sNo{}\n\n", p, p, p, p, p)
	fmt.Fprintf(&b, "func %sStay(s %sCount, e %sNone) Decision[%sCount, int] { Ignore }\n\n", p, p, p, p)
	fmt.Fprintf(&b, "type %sEq machine[%sCount, %sNone] {\n\tinitial: %sSeen{N: %sDistinct()},\n\tstep:    %sStay,\n\n\tlaw Count reaches %sSeen,\n}\n\n", p, p, p, p, p, p, p)
	return b.String()
}

// TestKeyEqualsEquality is the property of state keys (machinelaws.go,
// State keys): for generated types (records, unions, slices, Maps and
// basic types, nested; a tuple field reads as a method, so no tuples) and generated values of them, two values have
// one key exactly when Ergo's `==` holds. It counts each family's values
// two ways in one test binary: the search's states (one per key, plus the
// start) and a count by `==`; the two must agree, and some values must be
// equal and some not, so the families compare both ways.
// It builds Go because the key is the lowered Go's: the question is the
// value an encoding gives, which no in-process check computes.
// ERGO_KEYEQ_SEED replays a seed; a failure prints the source.
func TestKeyEqualsEquality(t *testing.T) {
	needsBuild(t)
	seed := int64(11)
	if s := os.Getenv("ERGO_KEYEQ_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_KEYEQ_SEED: %v", err)
		}
		seed = n
	}
	g := &keyGen{r: rand.New(rand.NewSource(seed)), sizes: map[string]int{}}
	const families = 40
	var body strings.Builder
	for k := 0; k < families; k++ {
		body.WriteString(g.keyFamily(k))
	}
	src := g.decls.String() + body.String() + "func main() {}\n"
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "keyeq", src)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["keyeq"])
	keys := regexp.MustCompile(`^ok law K(\d+)Keys\.Distinct \(proved: (\d+) states`)
	eqs := regexp.MustCompile(`^ok law K(\d+)Eq\.Count \(proved by the run from K\d+Seen\{N: (\d+)\}`)
	byKey, byEq := map[string]int{}, map[string]int{}
	for _, line := range verdicts(stdout) {
		if m := keys.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[2])
			byKey[m[1]] = n - 1
		}
		if m := eqs.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[2])
			byEq[m[1]] = n
		}
	}
	report := func(format string, args ...any) {
		t.Helper()
		t.Fatalf("seed %d (ERGO_KEYEQ_SEED=%d): %s\nstdout:\n%s\nstderr:\n%s\nsource:\n%s", seed, seed, fmt.Sprintf(format, args...), stdout, firstLines(stderr, 8), src)
	}
	if code != 0 || len(byKey) != families || len(byEq) != families {
		report("exit %d; %d key counts and %d equality counts of %d families", code, len(byKey), len(byEq), families)
	}
	merged, apart := false, false
	for k, n := range byEq {
		if byKey[k] != n {
			report("family K%s: %d values are distinct by ==, %d by key", k, n, byKey[k])
		}
		merged = merged || g.sizes[k] > n
		apart = apart || n > 1
	}
	if !merged || !apart {
		report("the families have equal values %v and unequal values %v; want both", merged, apart)
	}
}
