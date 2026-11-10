package check_test

import (
	"fmt"
	"math/rand"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// aliasBase is the declarations every generated alias program shares: a
// record, a union, a variant, an operation type, a recursive record whose
// field names an alias of itself, and an alias of a generic instance whose
// argument's variant names the alias (order-builder's EditActor shape),
// each alias before the declarations it reaches (types.md, Unions 7).
const aliasBase = `type AlHold = Hold[Sig]

type Hold[T] { V Option[T] }

type Sig = Go{} | Wrap{Of AlHold}

func hold(s Sig) AlHold { Hold[Sig]{V: Some{s}} }

func unwrap(s Sig) Option[Sig] {
	match s {
	case Wrap as w:
		w.Of.V
	case Go:
		None
	}
}

type AlNode = Node

type Node { N int, Next Option[AlNode] }

type R { N int }

type V1 { N int }

type V2 {}

type U = V1 | V2

type S { Get() int }

func depth(n AlNode) int {
	match n.Next {
	case Some as s:
		1 + depth(s.Value)
	case None:
		1
	}
}
`

// aliasTargets are the existing types an alias names, with the uses each
// kind admits.
var aliasTargets = []string{"int", "string", "bool", "R", "U", "V1", "V2", "S", "Option[int]", "Option[R]", "Node"}

// aliasProgram is a seeded program of aliases over aliasTargets, some of
// them aliases of aliases declared before or after them, with values of
// each alias passed both ways to and from its target, records built and
// read through the alias, unions matched through it and operations called
// through it. A seed may leave a match short of a case. It returns the
// program and the target text of each alias.
func aliasProgram(r *rand.Rand) (string, map[string]string, bool) {
	short := false
	n := 2 + r.Intn(6)
	written := make([]string, n) // the variant each alias writes
	base := make([]string, n)    // the existing type it ends at
	// An alias names a target or an alias of a higher index, so the
	// chains end and some point forward in source order.
	for i := n - 1; i >= 0; i-- {
		if i < n-1 && r.Intn(3) == 0 {
			j := i + 1 + r.Intn(n-1-i)
			written[i], base[i] = "Al"+strconv.Itoa(j), base[j]
			continue
		}
		t := aliasTargets[r.Intn(len(aliasTargets))]
		written[i], base[i] = t, t
	}
	targets := map[string]string{}
	var b strings.Builder
	b.WriteString(aliasBase)
	order := r.Perm(n)
	for _, i := range order {
		fmt.Fprintf(&b, "\ntype Al%d = %s\n", i, written[i])
		targets["Al"+strconv.Itoa(i)] = base[i]
	}
	for i := 0; i < n; i++ {
		a, t := "Al"+strconv.Itoa(i), base[i]
		fmt.Fprintf(&b, "\nfunc to%d(a %s) %s { a }\n", i, a, t)
		fmt.Fprintf(&b, "\nfunc from%d(t %s) %s { t }\n", i, t, a)
		fmt.Fprintf(&b, "\nfunc round%d(t %s) %s {\n\tlet x %s = t\n\tlet y %s = x\n\tto%d(from%d(y))\n}\n", i, t, t, a, t, i, i)
		switch t {
		case "int":
			fmt.Fprintf(&b, "\nfunc add%d(x %s, y int) %s { x + y }\n", i, a, a)
			// A conversion to the alias is a conversion to its target.
			fmt.Fprintf(&b, "\nfunc conv%d(f float64) int { %s(f) }\n", i, a)
		case "R", "V1":
			fmt.Fprintf(&b, "\nfunc mk%d(n int) %s { %s{N: n} }\n", i, t, a)
			fmt.Fprintf(&b, "\nfunc get%d(x %s) int { x.N }\n", i, a)
			// A function whose first parameter is the alias is the
			// target's (196m): a target's value calls it.
			fmt.Fprintf(&b, "\nfunc Dbl%d(x %s) int { x.N * 2 }\n", i, a)
			fmt.Fprintf(&b, "\nfunc use%d(n int) int { mk%d(n).Dbl%d() }\n", i, i, i)
		case "U":
			fmt.Fprintf(&b, "\nfunc m%d(x %s) int {\n\tmatch x {\n\tcase V1 as v:\n\t\tv.N\n", i, a)
			if r.Intn(4) != 0 {
				b.WriteString("\tcase V2:\n\t\t0\n")
			} else {
				short = true
			}
			b.WriteString("\t}\n}\n")
			// The union's own value, matched with an alias of its variant.
			if j := slices.Index(base, "V1"); j >= 0 {
				fmt.Fprintf(&b, "\nfunc v%d(x U) int {\n\tmatch x {\n\tcase Al%d as v:\n\t\tget%d(v)\n\tcase V2:\n\t\t0\n\t}\n}\n", i, j, j)
			}
		case "V2":
			// A record with no fields is a value by its bare name, and so
			// is its alias.
			fmt.Fprintf(&b, "\nfunc z%d() U { %s }\n", i, a)
		case "S":
			fmt.Fprintf(&b, "\nfunc call%d(s %s) int { s.Get() }\n", i, a)
		case "Option[int]", "Option[R]":
			fmt.Fprintf(&b, "\nfunc none%d() %s { None }\n", i, a)
		case "Node":
			fmt.Fprintf(&b, "\nfunc d%d(x %s) int { depth(x) }\n", i, a)
		}
	}
	return b.String(), targets, short
}

var aliasUse, aliasDecl = regexp.MustCompile(`\bAl[0-9]+\b`), regexp.MustCompile(`^type Al[0-9]+ =`)

// aliasReference is the program with every alias written as the type it
// ends at and the alias declarations gone: the program the alias program
// must check as.
func aliasReference(src string, targets map[string]string) string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		if aliasDecl.MatchString(line) {
			continue
		}
		out = append(out, aliasUse.ReplaceAllStringFunc(line, func(a string) string { return targets[a] }))
	}
	return strings.Join(out, "\n")
}

func aliasCodes(src string) []string {
	_, ds := check.CheckSource(src)
	var out []string
	for _, d := range ds {
		out = append(out, d.Code)
	}
	slices.Sort(out)
	return out
}

// TestAliasChecksAsItsTarget is the alias rule's property (types.md, Unions
// 7; F11): a union whose only variant is an existing type is that type, so
// a seeded program of aliases checks exactly as the same program with each
// alias written as its target: values flow both ways with no conversion, a
// record is built and read through the alias, a match on an alias is the
// target's match (a short one is the same S-code), and a variant's alias is
// a case of its union. ERGO_ALIAS_SEED replays one seed.
func TestAliasChecksAsItsTarget(t *testing.T) {
	seeds := 300
	first := int64(1)
	if s := os.Getenv("ERGO_ALIAS_SEED"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, seeds = v, 1
	}
	short := 0
	for seed := first; seed < first+int64(seeds); seed++ {
		src, targets, isShort := aliasProgram(rand.New(rand.NewSource(seed)))
		ref := aliasReference(src, targets)
		got, want := aliasCodes(src), aliasCodes(ref)
		if !slices.Equal(got, want) || isShort != (len(want) > 0) {
			t.Fatalf("seed %d (ERGO_ALIAS_SEED=%d): the alias program reports %v, its reference %v (a match left short: %v)\n--- alias program\n%s\n--- reference\n%s", seed, seed, got, want, isShort, src, ref)
		}
		if isShort {
			short++
		}
	}
	if seeds > 1 && short == 0 {
		t.Fatal("no seed left a match short of a case: the property never compares a diagnostic")
	}
}

// TestAliasRefusals pins the alias diagnostics' text and positions: an
// alias that names itself, directly or through another alias, and an alias
// with type parameters. A union of one variant declared in place stays a
// new type.
func TestAliasRefusals(t *testing.T) {
	for _, c := range []struct{ name, src, want string }{
		{"self", "type A = A\n", "S108@1:10 the alias A names itself (types.md, Unions 7)"},
		{"pair", "type A = B\n\ntype B = A\n", "S108@3:10 the alias A names itself (types.md, Unions 7)"},
		{"generic", "type R[T] { V T }\n\ntype A[T] = R[T]\n", "S108@3:7 A is an alias of its one variant, an existing type, and an alias takes no type parameters yet; write the type where it is used (types.md, Unions 7)"},
		{"same-type", "type B { N int }\n\ntype A = B\n\nfunc Show(a A) int { a.N }\n\nfunc Show(b B) int { b.N }\n", "S108@7:6 Show is already declared in this package; functions share a name only when each one's first parameter is a distinct named type declared in this package (types.md, Methods 3)"},
		{"in-place", "type W = R{ N int }\n\nfunc f(w W) R { w }\n", "S108@3:17"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, ds := check.CheckSource(c.src)
			var got []string
			for _, d := range ds {
				s := fmt.Sprintf("%s@%d:%d", d.Code, d.Line, d.Column)
				if strings.Contains(c.want, " ") {
					s += " " + d.Message
				}
				got = append(got, s)
			}
			if strings.Join(got, "\n") != c.want {
				t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), c.want)
			}
		})
	}
}
