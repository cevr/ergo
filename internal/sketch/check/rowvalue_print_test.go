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

// rowValueProgram writes a program whose type R has a field B[x | y | …],
// a row value over elems written in the order of written, after record
// types that name the elements in the order of uses: the order in which
// the check first meets each element.
func rowValueProgram(elems, written, uses []string) string {
	var b strings.Builder
	if strings.Contains(strings.Join(elems, " "), "fs.") {
		b.WriteString("import fs\n\n")
	}
	b.WriteString("type B[T] { v T }\n\n")
	for i, e := range uses {
		fmt.Fprintf(&b, "type U%d { a %s }\n", i, e)
	}
	for _, e := range elems {
		if !strings.Contains(e, ".") {
			fmt.Fprintf(&b, "type %s { n int }\n", e)
		}
	}
	fmt.Fprintf(&b, "type R { f B[%s] }\n", strings.Join(written, " | "))
	return b.String()
}

// rowValueText is how the checker prints the type of R's field f.
func rowValueText(t *testing.T, src string) string {
	t.Helper()
	pkg, diags := check.CheckSource(src)
	if len(diags) > 0 {
		t.Fatalf("the program does not check: %v\n%s", diags, src)
	}
	for _, tn := range pkg.Types {
		if tn.Name != "R" {
			continue
		}
		st, ok := tn.Type.(*check.Named).Underlying().(*check.Struct)
		if !ok || len(st.Fields) != 1 {
			t.Fatalf("R is not a record of one field\n%s", src)
		}
		return st.Fields[0].Type.String()
	}
	t.Fatalf("no type R\n%s", src)
	return ""
}

// TestRowValuePrintIsSorted (CK-ROWVALUE-PRINT): a row value is a set
// (rows.md), so the checker prints it the same whatever order it is
// written in and whatever order the package's other declarations first
// use its elements in, and its elements in sorted order, as every row
// prints. Each seed draws two to four elements, std failures and local
// records, and two programs that differ only in those orders.
// ERGO_ROWVALUE_SEED=N replays one seed.
func TestRowValuePrintIsSorted(t *testing.T) {
	seeds := []int64{}
	if s := os.Getenv("ERGO_ROWVALUE_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, n)
	} else {
		for i := int64(1); i <= 60; i++ {
			seeds = append(seeds, i)
		}
	}
	pool := []string{"fs.NotFound", "fs.Denied", "fs.Failed", "E0", "E1", "E2"}
	for _, seed := range seeds {
		rng := rand.New(rand.NewSource(seed))
		elems := slices.Clone(pool)
		rng.Shuffle(len(elems), func(i, j int) { elems[i], elems[j] = elems[j], elems[i] })
		elems = elems[:2+rng.Intn(3)]
		draw := func() (written, uses []string) {
			written = slices.Clone(elems)
			rng.Shuffle(len(written), func(i, j int) { written[i], written[j] = written[j], written[i] })
			uses = slices.Clone(elems)
			rng.Shuffle(len(uses), func(i, j int) { uses[i], uses[j] = uses[j], uses[i] })
			return written, uses[:rng.Intn(len(uses)+1)]
		}
		w1, u1 := draw()
		w2, u2 := draw()
		a, b := rowValueProgram(elems, w1, u1), rowValueProgram(elems, w2, u2)
		ta, tb := rowValueText(t, a), rowValueText(t, b)
		replay := fmt.Sprintf("replay: ERGO_ROWVALUE_SEED=%d go test -run TestRowValuePrintIsSorted ./internal/sketch/check", seed)
		if ta != tb {
			t.Fatalf("seed %d: one row value prints %q and %q\n%s\n%s\n%s", seed, ta, tb, a, b, replay)
		}
		inner := strings.TrimSuffix(ta[strings.Index(ta, "[")+1:], "]")
		parts := strings.Split(inner, " | ")
		if len(parts) != len(elems) || !slices.IsSorted(parts) {
			t.Fatalf("seed %d: %q does not list the %d elements sorted\n%s\n%s", seed, ta, len(elems), a, replay)
		}
	}
}
