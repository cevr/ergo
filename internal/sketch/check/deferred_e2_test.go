package check_test

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// These reports run before their declaration's ordinary pass reaches it:
// an operation constraint resolves a method signature in another file,
// and a recursive reference postpones a defined type's base verdict.
func TestDeferredReportsNameTheirFile(t *testing.T) {
	type report struct {
		file    int
		marker  string
		skip    int
		message string
	}
	for _, tc := range []struct {
		name  string
		files [2]string
		want  []report
	}{
		{
			"lazy-signature",
			[2]string{
				"type I { Show() int }\ntype Box[T I] { V T }\nfunc early(x Box[R]) {}\n",
				"type R {}\nfunc Show[T, T](r R) int { 1 }\n",
			},
			[]report{
				{0, "[R]", 1, "R does not satisfy I: R.Show has a type parameter of its own, T; an operation takes none"},
				{1, "T](r", 0, "type parameter T is declared twice"},
			},
		},
		{
			"defined-type",
			[2]string{"type I { M() A }\n", "type A I\n"},
			[]report{{1, "A I", 2, "a defined type is over a basic, slice, Map, function, tuple or record type, and I is an operation type; use I directly (types.md, Defined types)"}},
		},
	} {
		for order := range 2 {
			t.Run(fmt.Sprintf("%s/order=%d", tc.name, order), func(t *testing.T) {
				texts := [2]string{tc.files[order], tc.files[1-order]}
				var files []*syntax.File
				for _, src := range texts {
					f, err := syntax.Parse(src)
					if err != nil {
						t.Fatal(err)
					}
					files = append(files, f)
				}
				got, ice := checkPlaced(files)
				if ice != "" {
					t.Fatal(ice)
				}
				var want []check.Diagnostic
				for _, r := range tc.want {
					file := r.file ^ order
					at := strings.Index(texts[file], r.marker)
					if at < 0 {
						t.Fatalf("missing marker %q", r.marker)
					}
					offset := at + r.skip
					before := texts[file][:offset]
					want = append(want, check.Diagnostic{
						Code: "S108", Message: r.message, File: file,
						Offset: offset, Length: 1,
						Line: strings.Count(before, "\n") + 1, Column: offset - strings.LastIndexByte(before, '\n'),
					})
				}
				// Check lists them in the canonical order of their files:
				// for unnamed files with no imports, by text.
				sort.Slice(want, func(i, j int) bool { return texts[want[i].File] < texts[want[j].File] })
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("deferred reports across files:\n got: %#v\nwant: %#v", got, want)
				}
			})
		}
	}
}
