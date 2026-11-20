package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

var subUnionSeed = flag.Uint64("subunion.seed", 0, "run TestSubUnionsAgree on this seed alone")

// subUnionProgram is the property's program for seed (UNION-FLAT;
// types.md, Unions 1, 2 and 4): records R0…Rn-1, a sub-union A of some of
// them, a union B that lists A among the others, and a union C that holds
// A's records written out (widening by subset, no listing). For every
// value of A it prints show(widen(a)) and show(rebuild(a)), the hand
// rebuild, and for every value of B the match through `case A as a`
// beside the hand match over A's variants; a difference prints DIFFER.
// paths names what the seed reaches.
func subUnionProgram(seed uint64, paths map[string]bool) string {
	r := rand.New(rand.NewPCG(seed, 0x5b0))
	n := 3 + r.IntN(4)
	var b strings.Builder
	fields := make([]string, n)
	for i := range n {
		switch r.IntN(3) {
		case 0:
			fmt.Fprintf(&b, "type R%d {}\n", i)
			paths["a variant with no fields"] = true
		case 1:
			fields[i] = "N"
			fmt.Fprintf(&b, "type R%d { N int }\n", i)
			paths["a variant with fields"] = true
		default:
			fields[i] = "S"
			fmt.Fprintf(&b, "type R%d { S string }\n", i)
			paths["a variant with fields"] = true
		}
	}
	// A: a proper subset of two or more (one existing type would make A
	// an alias of it, types.md Unions 7), in a shuffled order.
	perm := r.Perm(n)
	k := 2 + r.IntN(n-2)
	inA, rest := perm[:k], perm[k:]
	alt := func(is []int) string {
		var parts []string
		for _, i := range is {
			parts = append(parts, fmt.Sprintf("R%d", i))
		}
		return strings.Join(parts, " | ")
	}
	fmt.Fprintf(&b, "type A = %s\n", alt(inA))
	// B lists A at a seeded place among the rest; C writes A's records out.
	at := r.IntN(len(rest) + 1)
	bParts := []string{}
	for j, i := range rest {
		if j == at {
			bParts = append(bParts, "A")
		}
		bParts = append(bParts, fmt.Sprintf("R%d", i))
	}
	if at == len(rest) {
		bParts = append(bParts, "A")
	}
	if at == 0 {
		paths["A listed first"] = true
	} else {
		paths["A listed after a record"] = true
	}
	fmt.Fprintf(&b, "type B = %s\n", strings.Join(bParts, " | "))
	cs := append(append([]int(nil), rest...), inA...)
	r.Shuffle(len(cs), func(i, j int) { cs[i], cs[j] = cs[j], cs[i] })
	fmt.Fprintf(&b, "type C = %s\n\n", alt(cs))
	name := func(i int) string { return fmt.Sprintf("R%d", i) }
	text := func(i int, v string) string {
		switch fields[i] {
		case "N":
			return fmt.Sprintf("\"%s \\(%s.N)\"", name(i), v)
		case "S":
			return fmt.Sprintf("\"%s \" + %s.S", name(i), v)
		}
		return fmt.Sprintf("\"%s\"", name(i))
	}
	// arm is the case of record i whose body is its text: a record with no
	// fields binds nothing (S110 for an unread binding).
	arm := func(i int) string {
		if fields[i] == "" {
			return fmt.Sprintf("\tcase %s:\n\t\t%s\n", name(i), text(i, ""))
		}
		return fmt.Sprintf("\tcase %s as v:\n\t\t%s\n", name(i), text(i, "v"))
	}
	all := append(append([]int(nil), inA...), rest...)
	show := func(fn, typ string, of []int) {
		fmt.Fprintf(&b, "func %s(x %s) string {\n\tmatch x {\n", fn, typ)
		for _, i := range of {
			b.WriteString(arm(i))
		}
		b.WriteString("\t}\n}\n\n")
	}
	show("showA", "A", inA)
	show("showB", "B", all)
	show("showC", "C", all)
	fmt.Fprintf(&b, "func widenB(a A) B { a }\n\nfunc widenC(a A) C { a }\n\n")
	for _, u := range []string{"B", "C"} {
		fmt.Fprintf(&b, "func rebuild%s(a A) %s {\n\tmatch a {\n", u, u)
		for _, i := range inA {
			fmt.Fprintf(&b, "\tcase %s as v:\n\t\tv\n", name(i))
		}
		b.WriteString("\t}\n}\n\n")
	}
	// The sub-union arm against the hand arms over A's variants.
	b.WriteString("func viaA(x B) string {\n\tmatch x {\n\tcase A as a:\n\t\t\"A \" + showA(a)\n")
	for _, i := range rest {
		b.WriteString(arm(i))
	}
	b.WriteString("\t}\n}\n\nfunc byHand(x B) string {\n\tmatch x {\n")
	for _, i := range inA {
		fmt.Fprintf(&b, "\tcase %s as v:\n\t\t\"A \" + showA(v)\n", name(i))
	}
	for _, i := range rest {
		b.WriteString(arm(i))
	}
	b.WriteString("\t}\n}\n\n")
	value := func(i int) string {
		switch fields[i] {
		case "N":
			return fmt.Sprintf("%s{N: %d}", name(i), r.IntN(100))
		case "S":
			return fmt.Sprintf("%s{S: %q}", name(i), fmt.Sprintf("s%d", r.IntN(100)))
		}
		return name(i)
	}
	b.WriteString("func same(a string, b string) string {\n\tif a == b {\n\t\ta\n\t} else {\n\t\t\"DIFFER \" + a + \" / \" + b\n\t}\n}\n\n")
	b.WriteString("func main() needs Console {\n")
	for _, i := range inA {
		fmt.Fprintf(&b, "\tlet a%d A = %s\n", i, value(i))
		fmt.Fprintf(&b, "\tConsole.Println(same(showB(widenB(a%[1]d)), showB(rebuildB(a%[1]d))))\n", i)
		fmt.Fprintf(&b, "\tConsole.Println(same(showC(widenC(a%[1]d)), showC(rebuildC(a%[1]d))))\n", i)
		fmt.Fprintf(&b, "\tConsole.Println(same(viaA(widenB(a%[1]d)), byHand(rebuildB(a%[1]d))))\n", i)
	}
	for _, i := range rest {
		fmt.Fprintf(&b, "\tlet b%d B = %s\n", i, value(i))
		fmt.Fprintf(&b, "\tConsole.Println(same(viaA(b%[1]d), byHand(b%[1]d)))\n", i)
	}
	b.WriteString("}\n")
	return b.String()
}

// TestSubUnionsAgree is UNION-FLAT's property: over seeded unions and
// sub-unions, widening equals the hand rebuild and `case A as a` equals
// the hand arms over A's variants, under the flat unions and under the
// interface reference (unionLowering "interface"), which must print the
// same; the flat programs keep every union flat (no bail), so the tag
// switches of unionInto run.
//
// It builds Go because the question is what the built programs print,
// which no in-process check observes.
func TestSubUnionsAgree(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4, 5, 6, 7, 8}
	if *subUnionSeed != 0 {
		seeds = []uint64{*subUnionSeed}
	}
	paths := map[string]bool{}
	type pair struct {
		seed      uint64
		src       string
		flat, ref string
	}
	var pairs []pair
	var programs []*lowered
	for _, seed := range seeds {
		src := subUnionProgram(seed, paths)
		replay := fmt.Sprintf("Replay: go test -run '^TestSubUnionsAgree$' ./internal/sketch/lower -args -subunion.seed=%d", seed)
		p := pair{seed: seed, src: src, flat: fmt.Sprintf("sub%d", seed), ref: fmt.Sprintf("sub%dref", seed)}
		flat, s := lowerOptionMode(t, p.flat, src, false, "", false)
		if s.UnionBails != 0 || s.FlatUnions == 0 {
			t.Errorf("seed %d: the flat lowering did not keep the unions flat: %+v\n%s\n%s", seed, s, replay, src)
		}
		ref, rs := lowerOptionMode(t, p.ref, src, false, "interface", false)
		if rs.FlatUnions != 0 {
			t.Errorf("seed %d: the reference built a flat value: %+v", seed, rs)
		}
		programs = append(programs, flat, ref)
		pairs = append(pairs, p)
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the sub-union programs do not build (seeds %v)", seeds)
	}
	for _, p := range pairs {
		replay := fmt.Sprintf("Replay: go test -run '^TestSubUnionsAgree$' ./internal/sketch/lower -args -subunion.seed=%d", p.seed)
		rout, rerr, rcode := runExe(t, exes[p.ref])
		out, errOut, code := runExe(t, exes[p.flat])
		if out != rout || errOut != rerr || code != rcode {
			t.Errorf("seed %d: the flat unions and the reference differ\n%s\n%s\n--- flat (exit %d)\n%s%s\n--- reference (exit %d)\n%s%s", p.seed, replay, p.src, code, out, errOut, rcode, rout, rerr)
		}
		if strings.Contains(rout, "DIFFER") || rcode != 0 {
			t.Errorf("seed %d: widening or case A as a differs from the hand form (exit %d)\n%s\n%s\n%s%s", p.seed, rcode, replay, p.src, rout, rerr)
		}
	}
	if *subUnionSeed != 0 {
		return
	}
	for _, path := range []string{"a variant with no fields", "a variant with fields", "A listed first", "A listed after a record"} {
		if !paths[path] {
			t.Errorf("the fixed seeds never reach %q", path)
		}
	}
}
