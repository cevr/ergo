package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// A refused definition has one diagnostic. Its name resolves as invalid
// in every type/value context, including contexts outside assignment.
func TestRefusedDefinedTypeUsesStayQuiet(t *testing.T) {
	const cap = "type Store { Get() int }\ntype DStore Store\n"
	const union = "type Mem {}\ntype Other {}\ntype U = Mem | Other\ntype DU U\n"
	for _, tc := range []struct {
		name, src string
		line      int
	}{
		{"needs-row", cap + "func f() needs DStore {}\n", 2},
		{"capability-selector", cap + "func f() { DStore.Get() }\n", 2},
		{"wire-key", cap + "type Mem {}\nfunc Get(m Mem) int { 1 }\nfunc f() { with wire { DStore: Mem } {} }\n", 2},
		{"match-subject", union + "func f(x DU) int { match x {\ncase Mem: 1\ncase Other: 2\n} }\n", 4},
		{"match-pattern", cap + "func f(x Store) { match x {\ncase DStore: _ = x\ncase _: _ = x\n} }\n", 2},
		{"union-pattern", cap + "type Mem {}\ntype Other {}\ntype U = Mem | Other\nfunc f(x U) { match x {\ncase DStore: _ = x\ncase _: _ = x\n} }\n", 2},
		{"fails-row", cap + "func f() fails DStore {}\n", 2},
		{"constraint", cap + "func f[T DStore](x T) { _ = x }\n", 2},
		{"generic-base", "type Store[T] { Get() T }\ntype DStore[T] Store[T]\nfunc f(x DStore[int]) { _ = x }\n", 2},
		{"nested-types", cap + "type Box[T] { V T }\nfunc f(x Box[[]DStore]) Box[[]DStore] { x }\n", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, d := check.CheckSource(tc.src)
			if len(d) != 1 || d[0].Code != check.CodeType || d[0].Line != tc.line || !strings.Contains(d[0].Message, "a defined type is over") {
				t.Fatalf("want one definition S108:%d, got %s", tc.line, describe(d))
			}
		})
	}
}

func TestRefusedImportAliasIsNotUnused(t *testing.T) {
	for _, src := range []string{
		"import t task\n",
		"import t timeout\n",
	} {
		_, d := check.CheckSource(src)
		if len(d) != 1 || d[0].Code != check.CodeType || d[0].Line != 1 || !strings.Contains(d[0].Message, "an import alias is only for a name clash") {
			t.Fatalf("%s: want one alias S108:1, got %s", src, describe(d))
		}
	}
	_, d := check.CheckSource("import task\n")
	if len(d) != 1 || d[0].Code != check.CodeUnused {
		t.Fatalf("valid unused import: want S110, got %s", describe(d))
	}
}
