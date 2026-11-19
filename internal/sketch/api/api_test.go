package api

import (
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestOwnershipAPIModel checks generated packages and edits against the
// generator's model: taking, the own mark, export-only and plain-data
// changes. Mutants: omitted mark, a dropped mark not breaking (an added one
// breaking), plain change breaking, and an extern's mark omitted.
// This seam tests the projected contracts, not the lowering or Go
// execution; diff is the reference below (std code's ApiText and ApiDiff
// have their own laws, ApiDiffBreaksOnANewTake).
func TestOwnershipAPIModel(t *testing.T) {
	// An extern's mark is its signature's: slices.Clone writes none, and
	// WithCapacity, whose Go body runtime/v1 reviews as moving its values
	// only, writes [T own].
	std, err := check.Std.Import("slices")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range Functions(std) {
		if f.Name == "Clone" && len(f.Own) != 0 {
			t.Fatalf("Clone reported as own: %+v", f)
		}
		if f.Name == "WithCapacity" && (len(f.Own) != 1 || f.Own[0] != "T") {
			t.Fatalf("WithCapacity's mark lost: %+v", f)
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
		oldOwn, oldTake, oldExport := r.Intn(2) == 0, r.Intn(2) == 0, r.Intn(2) == 0
		newOwn, newTake, newExport := r.Intn(2) == 0, r.Intn(2) == 0, r.Intn(2) == 0
		plainEdit := r.Intn(2) == 0
		source := func(marked, taking, public, plain bool) string {
			mark := ""
			if marked {
				mark = " own"
			}
			mode := ""
			if taking {
				mode = "own "
			}
			name := "hidden"
			if public {
				name = "Visible"
			}
			result := "int"
			if plain {
				result = "int64"
			}
			return fmt.Sprintf("type Res {} implements Closer\nfunc Close(r Res) {}\nfunc Inspect[T%s](x T) int { 1 }\nfunc Hand(r %sRes) int { 1 }\nfunc %s() int { 1 }\nfunc Plain() %s { 1 }\n", mark, mode, name, result)
		}
		a, b := source(oldOwn, oldTake, oldExport, false), source(newOwn, newTake, newExport, plainEdit)
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
				if (len(f.Own) > 0) != newOwn {
					fail("own: %+v", f)
				}
			}
			if f.Name == "Res.Hand" && ((len(f.Takes) > 0) != newTake) {
				fail("takes: %+v", f)
			}
		}
		got := diff(old, next)
		want := map[string]bool{}
		if oldOwn != newOwn {
			want["Inspect"] = oldOwn // dropping the mark breaks a caller that binds a resource
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
			fail("diff %v, model %v", got, want)
		}
	}
}

// diff is the reference diff of two projections: each name whose contract
// differs, with whether the new side takes a parameter or self the old did
// not, or drops an own mark the old wrote (a caller binding a resource
// breaks).
func diff(old, next []Function) map[string]bool {
	before := map[string]Function{}
	for _, f := range old {
		before[f.Name] = f
	}
	out := map[string]bool{}
	for _, f := range next {
		a, had := before[f.Name]
		delete(before, f.Name)
		if had && reflect.DeepEqual(a, f) {
			continue
		}
		breaks := false
		if had {
			breaks = f.TakesSelf && !a.TakesSelf
			for _, p := range f.TakesParameters {
				breaks = breaks || !slices.Contains(a.TakesParameters, p)
			}
			for _, n := range a.Own {
				breaks = breaks || !slices.Contains(f.Own, n)
			}
		}
		out[f.Name] = breaks
	}
	for n := range before {
		out[n] = false
	}
	return out
}
