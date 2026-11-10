package api

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestOwnershipAPIModel checks generated packages and edits against the
// generator's model: taking, copying, export-only and plain-data changes.
// Mutants: omitted copy, missed takes/copy change, plain change breaking,
// and foreign copy facts omitted by reading only recorded body facts.
// This seam tests checked docs and diff, not the lowering or Go execution.
func TestOwnershipAPIModel(t *testing.T) {
	// Smallest repro of the foreign-fact omission: both declarations have
	// no body, but only Clone's reviewed runtime body may copy its T.
	std, err := check.Std.Import("slices")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range Functions(std) {
		if f.Name == "Clone" && (len(f.Copies) != 1 || f.Copies[0].Parameter != "T" || f.Copies[0].Where != "" || f.Copies[0].What == "" || !strings.Contains(Text([]Function{f}), "copies T at foreign body (plain data only)")) {
			t.Fatalf("foreign copy policy lost: %+v", f)
		}
		if f.Name == "WithCapacity" && len(f.Copies) != 0 {
			t.Fatalf("reviewed moving body reported as copying: %+v", f)
		}
	}
	start, end := int64(1), int64(100)
	if s := os.Getenv("ERGO_API_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		start, end = n, n
	}
	for seed := start; seed <= end; seed++ {
		r := rand.New(rand.NewSource(seed))
		oldCopy, oldTake, oldExport := r.Intn(2) == 0, r.Intn(2) == 0, r.Intn(2) == 0
		newCopy, newTake, newExport := r.Intn(2) == 0, r.Intn(2) == 0, r.Intn(2) == 0
		plainEdit := r.Intn(2) == 0
		source := func(copying, taking, public, plain bool) string {
			body := "1"
			if copying {
				body = "let y = x; Read(y)"
			}
			clause := ""
			if taking {
				clause = " takes r"
			}
			name := "hidden"
			if public {
				name = "Visible"
			}
			result := "int"
			if plain {
				result = "int64"
			}
			return fmt.Sprintf("type Res {}\nfunc Close(r Res) {}\nfunc Read[T](x T) int { 1 }\nfunc Inspect[T](x T) int { %s }\nfunc Hand(r Res) int%s { 1 }\nfunc %s() int { 1 }\nfunc Plain() %s { 1 }\n", body, clause, name, result)
		}
		a, b := source(oldCopy, oldTake, oldExport, false), source(newCopy, newTake, newExport, plainEdit)
		read := func(src string) []Function {
			p, ds := check.CheckSource(src)
			if len(ds) > 0 {
				t.Fatalf("seed %d source:\n%s\ndiagnostics %v", seed, src, ds)
			}
			return Functions(p)
		}
		old, next := read(a), read(b)
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d (ERGO_API_SEED=%d go test -run TestOwnershipAPIModel ./internal/sketch/api)\nold:\n%s\nnew:\n%s\n%s", seed, seed, a, b, fmt.Sprintf(format, args...))
		}
		for _, f := range next {
			if f.Name == "Res.Close" && !f.TakesInferred {
				fail("implicit Close take labeled written: %+v", f)
			}
			if f.Name == "Inspect" {
				if (len(f.Copies) > 0) != newCopy {
					fail("copies: %+v", f)
				}
				if newCopy && !strings.Contains(Text(next), "copies T at ") {
					fail("docs omit copies: %s", Text(next))
				}
				if newCopy && (f.Copies[0].Where == "" || f.Copies[0].What == "") {
					fail("copy site lost its location or reason: %+v", f)
				}
			}
			if f.Name == "Res.Hand" && ((len(f.Takes) > 0) != newTake || !strings.Contains(Text([]Function{f}), "takes ")) {
				fail("takes: %+v", f)
			}
		}
		changes := Diff(old, next)
		got := map[string]bool{}
		for _, c := range changes {
			got[c.Name] = c.BreaksResourceCallers
		}
		want := map[string]bool{}
		if oldCopy != newCopy {
			want["Inspect"] = newCopy
		}
		if oldTake != newTake {
			want["Res.Hand"] = newTake
		}
		if oldExport != newExport {
			want["Visible"] = false
		}
		if plainEdit {
			want["Plain"] = false
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			fail("diff %+v, model %v; docs:\n%s", changes, want, DiffText(changes))
		}
	}
}
