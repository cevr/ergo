package check_test

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// The rule of types.md, Generics 7 (std-strengths SS4a): a generic type's
// trailing row parameters may be left out of its written type arguments,
// and each one left out is the empty row. A type parameter, a row
// parameter that a written argument follows, and an extra argument stay
// S108; a generic type with no written argument stays S108 as well.

// elidedRowsPrelude is the generic type of the fixtures: a command whose
// Build makes a C and may fail with E, the shape of flag.Command.
const elidedRowsPrelude = `type Bad {}

type Command[C, E] { Name string, Build func() C fails E }

func one() int { 1 }

func bad() int fails Bad { Bad }
`

// paramKind is the kind of one generated type parameter.
type paramKind int

const (
	kindType paramKind = iota
	kindFails
	kindNeeds
)

// TestElidedRowArgProperty: for generated generic types with 1 to 5
// parameters of mixed kinds, each written arity a from 0 to n+1 checks
// exactly when 1 <= a <= n and every parameter after the a-th is a row;
// and when it checks, each row left out is empty: a function whose own
// rows hold only the written rows calls the field with no try. Seeded and
// replayable: a failure prints the seed and the program.
func TestElidedRowArgProperty(t *testing.T) {
	const seeds = 400
	checked, refused, elided := 0, 0, 0
	for seed := uint64(1); seed <= seeds; seed++ {
		r := rand.New(rand.NewPCG(seed, 0x55a4))
		kinds := make([]paramKind, 1+r.IntN(5))
		for i := range kinds {
			kinds[i] = paramKind(r.IntN(3))
		}
		for a := 0; a <= len(kinds)+1; a++ {
			src := elisionProgram(kinds, a)
			_, diags := check.CheckSource(src)
			wantOK := a >= 1 && a <= len(kinds) && allRows(kinds[a:])
			switch {
			case wantOK && len(diags) != 0:
				t.Fatalf("seed %d, arity %d: want no diagnostic, got %s\n%s", seed, a, describe(diags), src)
			case !wantOK && (len(diags) != 1 || diags[0].Code != "S108"):
				t.Fatalf("seed %d, arity %d: want one S108, got %s\n%s", seed, a, describe(diags), src)
			case wantOK:
				checked++
				if a < len(kinds) {
					elided++
				}
			default:
				refused++
			}
		}
	}
	// The generator reaches every branch: programs that check with rows
	// left out, programs that check in full, and programs refused.
	if elided < 100 || checked-elided < 100 || refused < 100 {
		t.Errorf("weak generator: %d checked (%d with rows left out), %d refused", checked, elided, refused)
	}
}

func allRows(kinds []paramKind) bool {
	for _, k := range kinds {
		if k == kindType {
			return false
		}
	}
	return true
}

// elisionProgram declares T with the parameters kinds and a function Use
// that takes T with its first a parameters written: int for a type
// parameter, Bad for a failure row, Cap for a capability row. Use's own
// rows hold only the rows written, and it calls T's field with no try, so
// it checks only when every row left out is empty.
func elisionProgram(kinds []paramKind, a int) string {
	var params, fieldParams, args, fails, needs, written []string
	wroteFail, wroteNeed := false, false
	for i, k := range kinds {
		name := fmt.Sprintf("P%d", i)
		params = append(params, name)
		switch k {
		case kindType:
			fieldParams = append(fieldParams, fmt.Sprintf("p%d %s", i, name))
			args = append(args, "1")
			if i < a {
				written = append(written, "int")
			}
		case kindFails:
			fails = append(fails, name)
			if i < a {
				written = append(written, "Bad")
				wroteFail = true
			}
		case kindNeeds:
			needs = append(needs, name)
			if i < a {
				written = append(written, "Cap")
				wroteNeed = true
			}
		}
	}
	if a > len(kinds) {
		written = append(written, "int")
	}
	field := "func(" + strings.Join(fieldParams, ", ") + ") int"
	if len(fails) > 0 {
		field += " fails " + strings.Join(fails, " | ")
	}
	if len(needs) > 0 {
		field += " needs " + strings.Join(needs, " | ")
	}
	typ := "T"
	if len(written) > 0 {
		typ += "[" + strings.Join(written, ", ") + "]"
	}
	use := "func Use(t " + typ + ") int"
	call := "t.f(" + strings.Join(args, ", ") + ")"
	if wroteFail {
		use += " fails Bad"
		call = "try " + call
	}
	if wroteNeed {
		use += " needs Cap"
	}
	return fmt.Sprintf("type Bad {}\n\ntype Cap {\n\tUse(),\n}\n\ntype T[%s] { f %s }\n\n%s { %s }\n",
		strings.Join(params, ", "), field, use, call)
}
