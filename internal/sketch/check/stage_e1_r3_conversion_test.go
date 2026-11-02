package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Put the generic in another file and repeat identical use offsets in two
// files. Every conversion reports once, in its own file, in either order.
func TestE1ConversionSites(t *testing.T) {
	const generic = `type Valid { Check() bool, }
type Plain { N int }
type Box[T] { V T }
type Holder { V Valid }
func Check[T Valid](b Box[T]) bool { b.V.Check() }
func Take(v Valid) bool { v.Check() }
`
	shapes := []string{
		`func LetSUFFIX() bool { let v Valid = VALUE; v.Check() }`,
		`func ArgSUFFIX() bool { Take(VALUE) }`,
		`func FieldSUFFIX() Holder { Holder{V: VALUE} }`,
		`func RetSUFFIX() Valid { VALUE }`,
		`func MapSUFFIX() Map[string, Valid] { Map[string, Valid]{"k": VALUE} }`,
		`func SliceSUFFIX() []Valid { []Valid{VALUE} }`,
	}
	// The nested form checks that a dependency's failure keeps the outer
	// conversion's site, rather than any nested generic declaration's site.
	for _, value := range []string{`Box[Plain]{V: Plain{N: 1}}`, `Box[Box[Plain]]{V: Box[Plain]{V: Plain{N: 1}}}`} {
		t.Run(value, func(t *testing.T) {
			var uses [2]string
			for i := range uses {
				for _, shape := range shapes {
					uses[i] += strings.ReplaceAll(strings.ReplaceAll(shape, "SUFFIX", fmt.Sprint(i)), "VALUE", value) + "\n"
				}
			}
			for _, order := range [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
				sources := []string{generic, uses[0], uses[1]}
				var files []*syntax.File
				for _, i := range order {
					file, err := syntax.Parse(sources[i])
					if err != nil {
						t.Fatal(err)
					}
					files = append(files, file)
				}
				_, ds := check.Check("main", files, nil)
				if len(ds) != 2*len(shapes) {
					t.Errorf("order %v: want %d conversions, got %v", order, 2*len(shapes), ds)
				}
				seen := map[[2]int]bool{}
				for _, d := range ds {
					if d.File < 0 || d.File >= len(files) || order[d.File] == 0 || d.Line < 1 || d.Line > len(shapes) {
						t.Errorf("order %v: diagnostic outside a conversion: %+v", order, d)
						continue
					}
					line := strings.Split(sources[order[d.File]], "\n")[d.Line-1]
					column := strings.Index(line, value) + 1
					key := [2]int{d.File, d.Line}
					if seen[key] || d.Code != "S108" || d.Column != column || d.Length != len(value) ||
						!strings.Contains(d.Message, "cannot use main.Box[") || !strings.Contains(d.Message, "Check") || !strings.Contains(d.Message, "Plain") {
						t.Errorf("order %v: want one S108 at file %d %d:%d length %d naming Check and Plain, got %+v", order, d.File, d.Line, column, len(value), d)
					}
					seen[key] = true
				}
			}
		})
	}
}
