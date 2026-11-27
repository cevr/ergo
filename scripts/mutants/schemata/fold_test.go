package schemata

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

const shapes = `package p

import "strings"

type List[T any] struct{ items []T }

func (List[T]) Len() int { return 0 }

func (l *List[T]) Push(_ T, _ int) { l.items = l.items[:0] }

func Join(sep string, parts ...string) string { return strings.Join(parts, sep) }

func Pick[K comparable, V any](m map[K]V, k K) (v V, ok bool) {
	v, ok = m[k]
	return
}

func Note(int, string) {}

type Shape int
`

// TestFoldedTreeTypeChecks folds one mutant of each function shape the
// dispatcher must call (a method on a generic type with an unnamed and
// with blank parameters, a variadic function, a generic function with
// named results, a function with unnamed parameters and no results) and
// type-checks the folded file in process; a mutant that changes a type is
// left out with its reason. Mutants: schemata-variadic-spread-dropped,
// schemata-blank-param-kept, schemata-type-change-folds
// (scripts/mutants/testdata/mutants.json).
func TestFoldedTreeTypeChecks(t *testing.T) {
	edits := []struct{ name, from, to string }{
		{"len", "return 0 }", "return 1 }"},
		{"push", "l.items[:0]", "l.items[:1]"},
		{"join", "strings.Join(parts, sep)", "strings.Join(parts, sep+sep)"},
		{"pick", "v, ok = m[k]", "_, ok = m[k]"},
		{"note", "func Note(int, string) {}", "func Note(int, string) { panic(0) }"},
		{"shape", "type Shape int", "type Shape uint"},
	}
	base := File{Path: "p/p.go", Data: []byte(shapes)}
	var variants []Variant
	for i, e := range edits {
		if !strings.Contains(shapes, e.from) {
			t.Fatalf("%s: %q not in the source", e.name, e.from)
		}
		variants = append(variants, Variant{ID: i + 1, Name: e.name, File: File{Path: base.Path, Data: []byte(strings.Replace(shapes, e.from, e.to, 1))}})
	}
	res := Fold([]File{base}, variants)
	if why := res.Isolated[6]; !strings.Contains(why, "type") {
		t.Fatalf("the type change folded: %q", why)
	}
	for id := 1; id <= 5; id++ {
		if len(res.Folded[id]) != 1 {
			t.Fatalf("mutant %d: folded %v, isolated %q", id, res.Folded[id], res.Isolated[id])
		}
	}
	if len(res.Files) != 1 {
		t.Fatalf("folded files: %d", len(res.Files))
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", res.Files[0].Data, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("the folded file does not parse: %v\n%s", err, res.Files[0].Data)
	}
	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	if _, err := conf.Check("p", fset, []*ast.File{f}, nil); err != nil {
		t.Fatalf("the folded file does not type-check: %v\n%s", err, res.Files[0].Data)
	}
}
