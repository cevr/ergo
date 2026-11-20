package check_test

import (
	"fmt"
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestImportedContextNeedsRow: a with of another package's named context
// charges its header's needs row where it builds it, as a with of the
// package's own does (rows.md, Context 4; contextneeds.go rule 2). The
// sketchfront property TestContextNeedsRowModel covers one package; this
// row is the imported operand's path (withArg's selector), which it does
// not reach. Mutant ctxn-imported-uncharged.
func TestImportedContextNeedsRow(t *testing.T) {
	lf, err := syntax.Parse(`type Clock { Now() int }
type Store { Get() int }
type Timed { at int } implements Store
func Get(t Timed) int { t.at }
func NewTimed() Timed needs Clock { Timed{at: Clock.Now()} }
let Live = context needs Clock { Store: NewTimed() }
`)
	if err != nil {
		t.Fatal(err)
	}
	lib, ds := check.Check("lib", []*syntax.File{lf}, nil)
	if len(ds) != 0 {
		t.Fatalf("package lib: %v", ds)
	}
	mf, err := syntax.Parse(`import lib

test Open {
	with lib.Live { assert lib.Store.Get() > 0 }
}

func provided() int needs lib.Clock {
	with lib.Live { lib.Store.Get() }
}
`)
	if err != nil {
		t.Fatal(err)
	}
	_, ds = check.Check("main", []*syntax.File{mf}, shadowImporter{"lib": lib})
	var got []string
	for _, d := range ds {
		got = append(got, fmt.Sprintf("%d:%d %s %s", d.Line, d.Column, d.Code, d.Message))
	}
	want := []string{"4:7 S102 context Live requires lib.Clock, which is not in the capability row of test Open and no with provides it"}
	if !slices.Equal(got, want) {
		t.Errorf("diagnostics:\n%v\nwant:\n%v", got, want)
	}
}
