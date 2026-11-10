package check_test

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/format"
)

var typeParamsSeed = flag.Uint64("typeparams.seed", 0, "run TestTypeParamGroups on this seed alone")

// TestTypeParamGroups (fu8b) is a seeded property of the one grouping rule
// of a type parameter list (grammar.md, Declarations 3): a constraint
// applies to the names written before it, back to the previous constraint;
// names at the end without one have none; and `any` ends an unconstrained
// group only where a constrained group follows it, anywhere else it is
// S104. A generator writes lists of 1 to 5 groups of 1 to 3 names, each
// followed by nothing, any, comparable, ordered or number, in a type or a
// function declaration. A model reads the same words in order and gives
// each name its constraint and each `any` its verdict. The checker's
// constraint of every parameter and its diagnostics must equal the
// model's, and ergo fmt must keep the source as written. -typeparams.seed=N
// runs the seed N alone; a failure prints the replay command and the
// source.
func TestTypeParamGroups(t *testing.T) {
	seeds := make([]uint64, 400)
	for i := range seeds {
		seeds[i] = uint64(i + 1)
	}
	if *typeParamsSeed != 0 {
		seeds = []uint64{*typeParamsSeed}
	}
	words := []string{"", "any", "comparable", "ordered", "number"}
	admitted, refused := 0, 0
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x7b))
		// The list as written: each entry a name, or a constraint word
		// after the names it ends.
		var seq []string
		var names []string
		var parts []string
		for range 1 + r.IntN(5) {
			var group []string
			for range 1 + r.IntN(3) {
				n := fmt.Sprintf("T%d", len(names))
				names = append(names, n)
				group = append(group, n)
				seq = append(seq, n)
			}
			part := strings.Join(group, ", ")
			if w := words[r.IntN(len(words))]; w != "" {
				part += " " + w
				seq = append(seq, "="+w)
			}
			parts = append(parts, part)
		}
		list := strings.Join(parts, ", ")
		// The model.
		want := map[string]string{}
		var wantDiags []string
		for i, s := range seq {
			if s[0] == '=' {
				if s == "=any" {
					next := ""
					for _, u := range seq[i+1:] {
						if u[0] == '=' {
							next = u
							break
						}
					}
					if next == "" || next == "=any" {
						wantDiags = append(wantDiags, "S104:1")
						refused++
					} else {
						admitted++
					}
				}
				continue
			}
			want[s] = ""
			for _, u := range seq[i+1:] {
				if u[0] == '=' {
					if u != "=any" {
						want[s] = u[1:]
					}
					break
				}
			}
		}
		var src string
		if seed%2 == 0 {
			var fields []string
			for i, n := range names {
				fields = append(fields, fmt.Sprintf("F%d %s", i, n))
			}
			src = fmt.Sprintf("type R[%s] { %s }\n", list, strings.Join(fields, ", "))
		} else {
			var params []string
			for i, n := range names {
				params = append(params, fmt.Sprintf("p%d %s", i, n))
			}
			src = fmt.Sprintf("func f[%s](%s) {}\n", list, strings.Join(params, ", "))
		}
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d: %s\nReplay: go test -run '^TestTypeParamGroups$' ./internal/sketch/check -args -typeparams.seed=%d\nSource:\n%s", seed, fmt.Sprintf(format, args...), seed, src)
		}
		got, d := codeLines(src)
		if got != strings.Join(wantDiags, " ") {
			fail("diagnostics [%s], the model's [%s]: %v", got, strings.Join(wantDiags, " "), d)
		}
		pkg, _ := check.CheckSource(src)
		seen := map[string]bool{}
		for id, o := range pkg.Info.Defs {
			tp, ok := o.(*check.TypeParam)
			if !ok {
				continue
			}
			seen[id.Name] = true
			c := ""
			if tp.Constraint != nil {
				c = tp.Constraint.String()
			}
			if c != want[id.Name] {
				fail("%s has the constraint %q, the model's %q", id.Name, c, want[id.Name])
			}
		}
		if len(seen) != len(names) {
			fail("%d type parameters declared, %d written", len(seen), len(names))
		}
		if out, err := format.Source(src); err != nil || out != src {
			fail("ergo fmt gives %q (%v)", out, err)
		}
	}
	t.Logf("seeds %d..%d: %d any admitted, %d refused", seeds[0], seeds[len(seeds)-1], admitted, refused)
	if *typeParamsSeed == 0 && (admitted == 0 || refused == 0) {
		t.Errorf("the generator wrote %d admitted and %d refused any: it must write both", admitted, refused)
	}
}
