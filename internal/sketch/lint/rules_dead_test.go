package lint_test

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// The dead and duplicate family of the strict preset (LH2a, catalog §4):
// for each rule a generator of declarations and a reference that decides
// from what the generator wrote, never from the program text.

// TestUnusedDeclarationAgreesWithReference declares unexported and
// exported functions and types over a random use graph (a function may
// call itself, others, or take a declared type) and checks that
// unused-declaration fires on exactly the unexported declarations no
// other declaration uses: a self-call, an exported name and main are no
// finding's reason; a dot call is a use, and a method that provides an
// operation is out of scope. Mutants: lh2a-self-use-counts,
// lh2a-dot-call-unread, lh2a-method-reported.
func TestUnusedDeclarationAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(40) {
		r := rand.New(rand.NewPCG(seed, 0xd1))
		n := 2 + r.IntN(5)
		type fn struct {
			name     string
			exported bool
			calls    []int
			self     bool
			takes    int // a type index, or -1
		}
		types := 1 + r.IntN(3)
		typeUsed := make([]bool, types)
		fns := make([]fn, n)
		used := make([]bool, n)
		for i := range fns {
			f := &fns[i]
			f.exported = r.IntN(4) == 0
			f.name = fmt.Sprintf("f%d", i)
			if f.exported {
				f.name = fmt.Sprintf("F%d", i)
			}
			f.self = r.IntN(3) == 0
			f.takes = -1
			if r.IntN(2) == 0 {
				f.takes = r.IntN(types)
				typeUsed[f.takes] = true
			}
			for j := range n {
				if j != i && r.IntN(4) == 0 {
					f.calls = append(f.calls, j)
					used[j] = true
				}
			}
		}
		fromMain := r.Perm(n)[:r.IntN(n)]
		for _, j := range fromMain {
			used[j] = true
		}
		p := newPlanted()
		// val is a method box provides Valued with: no call names it
		p.write("type Valued {\n\tval() int,\n}\n\ntype box { N int } implements Valued\n\nfunc val(b box) int { b.N }\n\nfunc read(v Valued) int { v.val() }\n", false, 0)
		for k := range types {
			p.write(fmt.Sprintf("type t%d { N int }", k), !typeUsed[k], 0)
		}
		for i, f := range fns {
			param := "n int"
			if f.takes >= 0 {
				param = fmt.Sprintf("v t%d, n int", f.takes)
			}
			p.write(fmt.Sprintf("\nfunc %s(%s) int {", f.name, param), !f.exported && !used[i], 1)
			for _, j := range f.calls {
				p.write("\tlet _ = "+call(fns[j].name, fns[j].takes, r.IntN(2) == 0), false, 0)
			}
			if f.self {
				p.write("\tif n > 3 {\n\t\treturn "+call(f.name, f.takes, r.IntN(2) == 0)+"\n\t}", false, 0)
			}
			p.write("\tn\n}", false, 0)
		}
		p.write("\nfunc main() {\n\tlet _ = read(box{N: 1})", false, 0)
		for _, j := range fromMain {
			p.write("\tlet _ = "+call(fns[j].name, fns[j].takes, r.IntN(2) == 0), false, 0)
		}
		p.write("}", false, 0)
		p.check(t, "unused-declaration", "TestUnusedDeclarationAgreesWithReference", seed)
	}
}

// call is a call of name, in dot form (a use only Info.Calls records)
// when its first parameter is a type and dot is set.
func call(name string, takes int, dot bool) string {
	switch {
	case takes < 0:
		return name + "(1)"
	case dot:
		return fmt.Sprintf("t%d{N: 1}.%s(1)", takes, name)
	}
	return fmt.Sprintf("%s(t%d{N: 1}, 1)", name, takes)
}

// TestUnusedTypeParameterAgreesWithReference declares generic functions
// whose type parameters are named in a parameter, the result, another's
// constraint position, only the body, or nowhere, and checks that
// unused-type-parameter fires on exactly the ones named nowhere. Mutant:
// lh2a-type-parameter-body-unread.
func TestUnusedTypeParameterAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(40) {
		r := rand.New(rand.NewPCG(seed, 0xd2))
		p := newPlanted()
		for i := range 1 + r.IntN(4) {
			// where each of A and B is named: 0 parameter, 1 result,
			// 2 body only, 3 nowhere
			a, b := r.IntN(4), r.IntN(4)
			var params, results, body []string
			place := func(name string, where int) {
				switch where {
				case 0:
					params = append(params, "x"+name+" "+name)
				case 1:
					results = append(results, name)
				case 2:
					body = append(body, "\tlet _ []"+name+" = []"+name+"{}")
				}
			}
			place("A", a)
			place("B", b)
			result := ""
			switch len(results) {
			case 1:
				result = " " + results[0]
			case 2:
				result = " (A, B)"
			}
			line := p.line + 1
			p.write(fmt.Sprintf("\nfunc g%d[A, B](%s)%s {", i, strings.Join(params, ", "), result), a == 3, 1)
			if b == 3 {
				p.want = append(p.want, line)
			}
			if len(results) > 0 {
				body = append(body, "\tUnreachable(\"never called\")")
			}
			p.write(strings.Join(append(body, "}"), "\n"), false, 0)
		}
		p.write("\nfunc main() {\n}", false, 0)
		p.check(t, "unused-type-parameter", "TestUnusedTypeParameterAgreesWithReference", seed)
	}
}

// A test body of the duplicate-test generator: its text over a local
// name, and its class (bodies of one class do the same).
var testBodies = []struct {
	class int
	text  func(local string) string
}{
	{0, func(l string) string { return "let " + l + " = 3\n\tassert " + l + " + 1 == 4" }},
	{1, func(string) string { return "assert one() == 1" }},
	// the same tree as class 1, but another function: no copy
	{2, func(string) string { return "assert uno() == 1" }},
	{3, func(string) string { return "assert 2 + 2 == 4" }},
	// the same tree as class 3, other constants: no copy
	{4, func(string) string { return "assert 2 + 3 == 5" }},
}

// TestDuplicateTestAgreesWithReference declares tests and laws drawn from
// a few bodies, under random names in random order, the locals renamed,
// and checks that duplicate-test fires on each copy but the first by name
// of its class: never on bodies that differ only in the function they call
// or in a constant. Mutants: lh2a-duplicate-first-by-position,
// lh2a-shape-locals-by-object, lh2a-shape-drops-literal.
func TestDuplicateTestAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(40) {
		r := rand.New(rand.NewPCG(seed, 0xd3))
		p := newPlanted()
		p.write("func one() int { 1 }\n\nfunc uno() int { 1 }\n\nfunc main() {\n}", false, 0)
		type decl struct {
			name  string
			class int
			law   bool
		}
		var decls []decl
		for i := range 2 + r.IntN(6) {
			law := r.IntN(3) == 0
			class := testBodies[r.IntN(len(testBodies))].class
			if law {
				class = 10 + r.IntN(2)
			}
			decls = append(decls, decl{fmt.Sprintf("C%c%d", 'a'+rune(r.IntN(26)), i), class, law})
		}
		first := map[int]string{}
		for _, d := range decls {
			if f, ok := first[d.class]; !ok || d.name < f {
				first[d.class] = d.name
			}
		}
		locals := []string{"x", "y", "count"}
		for _, d := range decls {
			fires := first[d.class] != d.name
			local := locals[r.IntN(len(locals))]
			if d.law {
				body := local + " + 0 == " + local
				if d.class == 11 {
					body = local + " * 1 == " + local
				}
				p.write(fmt.Sprintf("\nlaw %s(%s int) {\n\t%s\n}", d.name, local, body), fires, 1)
				continue
			}
			p.write(fmt.Sprintf("\ntest %s {\n\t%s\n}", d.name, testBodies[d.class].text(local)), fires, 1)
		}
		p.check(t, "duplicate-test", "TestDuplicateTestAgreesWithReference", seed)
	}
}

// TestDuplicateShapeAgreesWithReference declares unions over a few
// variants, members in random order, and records over a few fields, and
// checks that duplicate-shape fires on each union but the first by name of
// its variant set, and on no record: records with the same fields are
// nominal on purpose. Mutants: lh2a-shape-union-order, lh2a-shape-records-compared.
func TestDuplicateShapeAgreesWithReference(t *testing.T) {
	fields := []string{"A int", "B string", "C bool", "A string"}
	for _, seed := range propertySeeds(40) {
		r := rand.New(rand.NewPCG(seed, 0xd4))
		p := newPlanted()
		// V2's fields are a record's: a variant, so no copy
		p.write("type V0 { N int }\n\ntype V1 { S string }\n\ntype V2 { A int, B string }\n\ntype V3 {}", false, 0)
		type decl struct {
			name, text, key string
			counted         bool
		}
		decls := []decl{{"Uses", "type Uses = V2 | V3", "uV2,V3", true}}
		for i := range 2 + r.IntN(6) {
			name := fmt.Sprintf("T%c%d", 'a'+rune(r.IntN(26)), i)
			if r.IntN(2) == 0 {
				members := r.Perm(4)[:2+r.IntN(3)]
				var names []string
				for _, m := range members {
					names = append(names, fmt.Sprintf("V%d", m))
				}
				text := "type " + name + " = " + strings.Join(names, " | ")
				slices.Sort(names)
				decls = append(decls, decl{name, text, "u" + strings.Join(names, ","), true})
				continue
			}
			var picked []string
			seen := map[byte]bool{}
			for _, f := range r.Perm(len(fields))[:1+r.IntN(3)] {
				if !seen[fields[f][0]] {
					seen[fields[f][0]] = true
					picked = append(picked, fields[f])
				}
			}
			text := "type " + name + " { " + strings.Join(picked, ", ") + " }"
			slices.Sort(picked)
			decls = append(decls, decl{name, text, "r" + strings.Join(picked, ","), false})
		}
		first := map[string]string{}
		for _, d := range decls {
			if f, ok := first[d.key]; d.counted && (!ok || d.name < f) {
				first[d.key] = d.name
			}
		}
		for _, d := range decls {
			p.write("\n"+d.text, d.counted && first[d.key] != d.name, 1)
		}
		p.write("\nfunc main() {\n}", false, 0)
		p.check(t, "duplicate-shape", "TestDuplicateShapeAgreesWithReference", seed)
	}
}

// An arm body of the identical-arms generator: its text over the arm's
// binding b and a local name, its class (bodies of one class do the
// same), and whether it binds b. A body that reads its own binding has a
// class of its own.
var armBodies = []struct {
	class int
	binds bool
	text  func(local string) string
}{
	{0, false, func(string) string { return "0" }},
	{1, false, func(string) string { return "1" }},
	{2, false, func(l string) string { return "let " + l + " = n\n\t\t" + l + " + 1" }},
	{3, false, func(string) string { return "n + 2" }},
	// reads its own binding, spelled alike in every arm: never the same
	{-1, true, func(string) string { return "b.N" }},
}

// TestIdenticalArmsAgreesWithReference writes matches over a union and
// over an int, with arms drawn from a few bodies, locals renamed and
// bindings spelled alike, and checks that identical-arms fires once per
// group of arms that do the same, at its second arm. Mutant:
// lh2a-identical-pairs-ignored.
func TestIdenticalArmsAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(40) {
		r := rand.New(rand.NewPCG(seed, 0xd5))
		p := newPlanted()
		p.write("type A { N int }\n\ntype B { N int }\n\ntype C { N int }\n\ntype D { N int }\n\ntype U = A | B | C | D", false, 0)
		for i := range 1 + r.IntN(4) {
			union := r.IntN(2) == 0
			if union {
				p.write(fmt.Sprintf("\nfunc m%d(u U, n int) int {\n\tmatch u {", i), false, 0)
			} else {
				p.write(fmt.Sprintf("\nfunc m%d(k int, n int) int {\n\tmatch k {", i), false, 0)
			}
			patterns := []string{"A", "B", "C", "D"}
			if !union {
				patterns = []string{"1", "2", "3", "_"}
			}
			seen := map[int]int{}
			for j, pat := range patterns {
				body := armBodies[r.IntN(len(armBodies))]
				if body.binds && !union {
					body = armBodies[0]
				}
				head := "\tcase " + pat + ":"
				if body.binds {
					head = "\tcase " + pat + " as b:"
				}
				class := body.class
				if class < 0 {
					class = -1 - j
				}
				seen[class]++
				p.write(head+"\n\t\t"+body.text([]string{"x", "y"}[r.IntN(2)]), seen[class] == 2, 0)
			}
			p.write("\t}\n}", false, 0)
		}
		p.write("\nfunc main() {\n}", false, 0)
		p.check(t, "identical-arms", "TestIdenticalArmsAgreesWithReference", seed)
	}
}

// TestUnbuiltVariantAgreesWithReference declares a union of record
// variants, exported and not, one of them inline, builds a random subset
// in functions or tests, and checks that unbuilt-variant fires on exactly
// the unbuilt variants the run sees every builder of: all of them in a
// program with main, the unexported ones without. A record in no union is
// no variant; a field-less variant is built by its bare name, not by a
// pattern; a property's parameter builds what it reaches. Mutants:
// lh2a-variant-closed-ignored, lh2a-bare-variant-unbuilt,
// lh2a-pattern-builds, lh2a-generator-builds-nothing,
// lh2a-type-argument-builds-nothing.
func TestUnbuiltVariantAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(40) {
		r := rand.New(rand.NewPCG(seed, 0xd6))
		closed := r.IntN(2) == 0
		names := []string{"Open", "shut", "Held", "gone"}
		built := make([]bool, len(names)+1)
		for i := range built {
			built[i] = r.IntN(2) == 0
		}
		p := newPlanted()
		for i, name := range names {
			p.write(fmt.Sprintf("type %s { N int }\n", name), !built[i] && (closed || i%2 == 1), 0)
		}
		p.write("type Lone { N int }\n", false, 0)
		// Idle has no fields: its bare name builds it; a pattern does not
		idleBuilt := r.IntN(2) == 0
		stateLine := p.line
		p.write("type State = Open | shut | Held | gone | Inline{N int} | Idle{}", !built[len(names)] && closed, 0)
		if !idleBuilt && closed {
			p.want = append(p.want, stateLine)
		}
		// Op's variants are built only by a property's generator, if any
		by := r.IntN(3) // 0 nothing, 1 a property, 2 a generic call
		generated := by != 0
		opLine := p.line + 1
		p.write("\ntype Op = Push{N int} | Pop{N int}", false, 0)
		if !generated && closed {
			p.want = append(p.want, opLine, opLine)
		}
		switch by {
		case 1:
			p.write("\ntest ops(o Op) {\n\tlet _ = o\n}", false, 0)
		case 2:
			p.write("\nfunc none[T]() []T { []T{} }\n\nfunc decoded() []Op { none[Op]() }", false, 0)
		}
		p.write("\nfunc isIdle(s State) bool {\n\tmatch s {\n\tcase Idle:\n\t\ttrue\n\tcase _:\n\t\tfalse\n\t}\n}", false, 0)
		if idleBuilt {
			p.write("\nfunc idle() State {\n\tlet s = Idle\n\ts\n}", false, 0)
		}
		all := append(slices.Clone(names), "Inline")
		for i, name := range all {
			if !built[i] {
				continue
			}
			if r.IntN(2) == 0 {
				p.write(fmt.Sprintf("\nfunc make%d() State { %s{N: 1} }", i, name), false, 0)
			} else {
				p.write(fmt.Sprintf("\ntest built%d {\n\tlet _ State = %s{N: 1}\n}", i, name), false, 0)
			}
		}
		if closed {
			p.write("\nfunc main() {\n}", false, 0)
		}
		p.check(t, "unbuilt-variant", "TestUnbuiltVariantAgreesWithReference", seed)
	}
}
