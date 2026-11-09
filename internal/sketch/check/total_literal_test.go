package check

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
)

// totalField is a field type of the generated records and a value of it.
type totalField struct{ typ, val string }

// totalFields are the field types of TestRecordLiteralNamesEveryField: basic
// types, a slice, a Map, a union, a record, a generic instance and a defined tuple
// that holds a record.
var totalFields = []totalField{
	{"int", "1"},
	{"string", `"s"`},
	{"bool", "true"},
	{"[]int", "[]int{1}"},
	{"Map[string, int]", `Map[string, int]{"a": 1}`},
	{"Shape", "Dot"},
	{"In", "In{A: 1}"},
	{"Box[int]", "Box[int]{V: 1}"},
	{"Pair", "(1, In{A: 2})"},
}

const totalDecls = `type Dot {}
type Line { N int }
type Shape = Dot | Line
type In { A int }
type Box[T] { V T }
type Pair (int, In)
`

// TestRecordLiteralNamesEveryField is the property of types.md, Zero values
// 2 and 3 (owner q44, sp-total-literal): for generated records of 1 to 6
// fields and every subset of named fields, a keyed literal (alone or elided
// in a slice) is S108 exactly when it leaves a field out, and its message
// lists exactly the left-out fields in declaration order; a full
// positional literal checks; `let x T`, `let x (int, T)` and `let x Box[T]`
// with no value are S108, and `let x []T` checks (a slice keeps its zero
// value). ERGO_SEED=<n> replays one seed; a failure prints the seed and
// the source.
func TestRecordLiteralNamesEveryField(t *testing.T) {
	seeds := make([]int64, 24)
	for i := range seeds {
		seeds[i] = int64(i + 1)
	}
	if s := os.Getenv("ERGO_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_SEED: %v", err)
		}
		seeds = []int64{n}
	}
	for _, seed := range seeds {
		rng := rand.New(rand.NewSource(seed))
		n := 1 + rng.Intn(6)
		names := make([]string, n)
		fields := make([]totalField, n)
		var decl []string
		for i := range n {
			names[i] = fmt.Sprintf("F%d", i)
			fields[i] = totalFields[rng.Intn(len(totalFields))]
			decl = append(decl, names[i]+" "+fields[i].typ)
		}
		decls := totalDecls + "type T { " + strings.Join(decl, ", ") + " }\n"
		run := func(body string, want string) {
			t.Helper()
			src := decls + "func f() {\n" + body + "\n}\n"
			_, ds := CheckSource(src)
			got := ""
			if len(ds) > 0 {
				got = ds[0].Code + " " + ds[0].Message
			}
			if len(ds) > 1 || (want == "") != (got == "") || want != "" && !strings.HasPrefix(got, want) {
				t.Errorf("seed %d (replay: ERGO_SEED=%d): want %q, got %s\n%s", seed, seed, want, describeDiags(ds), src)
			}
		}
		for mask := 0; mask < 1<<n; mask++ {
			var set, missing []string
			for i := range n {
				if mask&(1<<i) != 0 {
					set = append(set, names[i]+": "+fields[i].val)
				} else {
					missing = append(missing, names[i])
				}
			}
			lit := "T{" + strings.Join(set, ", ") + "}"
			want := ""
			if len(missing) > 0 {
				want = "S108 T{…} does not set " + strings.Join(missing, ", ") + ":"
			}
			if rng.Intn(2) == 0 {
				run("\tlet _ = "+lit, want)
			} else {
				run("\tlet _ = []T{{"+strings.Join(set, ", ")+"}}", want)
			}
		}
		var vals []string
		for _, f := range fields {
			vals = append(vals, f.val)
		}
		run("\tlet _ = T{"+strings.Join(vals, ", ")+"}", "")
		noValue := "S108 let x of type "
		run("\tlet x T\n\tlet _ = x", noValue+"T needs a value")
		run("\tlet x (int, T)\n\tlet _ = x", noValue+"(int, T) needs a value")
		run("\tlet x Box[T]\n\tlet _ = x", noValue+"Box[T] needs a value")
		run("\tlet x []T\n\tlet _ = x", "")
	}
}
