package eval

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// licenceADTSrc is a user Maybe and a recursive Tree with their combinators
// and fusion laws (ADT-NOPRIV r/p2c-maybe and r/p4b-tree-explicit), and
// one false fold law that swaps a step's arguments.
const licenceADTSrc = `type Just[T] { Value T }
type Nothing {}
type Maybe[T] = Just[T] | Nothing

func Fmap[T, U](m Maybe[T], f func(x T) U) Maybe[U] {
	match m {
	case Just[T] as j:
		Just{Value: f(j.Value)}
	case Nothing:
		Nothing
	}
}

law MapFuses[T, U, V comparable](m Maybe[T], f func(x T) U, g func(x U) V) { Fmap(Fmap(m, f), g) } == { Fmap(m, func(x T) V { g(f(x)) }) }

type Leaf {}
type Node[T] { Left Tree[T], Value T, Right Tree[T] }
type Tree[T] = Leaf | Node[T]

func TreeMap[T, U](t Tree[T], f func(x T) U) Tree[U] {
	match t {
	case Leaf:
		Leaf
	case Node[T] as n:
		Node{Left: TreeMap[T, U](n.Left, f), Value: f(n.Value), Right: TreeMap[T, U](n.Right, f)}
	}
}

func Fold[T, A](t Tree[T], z A, f func(acc A, x T) A) A {
	match t {
	case Leaf:
		z
	case Node[T] as n:
		Fold[T, A](n.Right, f(Fold[T, A](n.Left, z, f), n.Value), f)
	}
}

law TreeMapFuses[T, U, V comparable](t Tree[T], f func(x T) U, g func(x U) V) { TreeMap(TreeMap(t, f), g) } == { TreeMap(t, func(x T) V { g(f(x)) }) }

law FoldOfMap[T, U, A comparable](t Tree[T], f func(x T) U, z A, g func(acc A, x U) A) { Fold(TreeMap(t, f), z, g) } == { Fold(t, z, func(acc A, x T) A { g(acc, f(x)) }) }

law FoldFlips[T comparable](t Tree[T], f func(x T) T, z T, g func(acc T, x T) T) { Fold(TreeMap(t, f), z, g) } == { Fold(t, z, func(acc T, x T) T { g(f(x), acc) }) }
`

// TestLicenceOverUnions (LICENCE-ADT; types.md Laws 14, rule 5): a fusion
// law over a generic union, a recursive one included, runs at int inside
// the union (a Maybe[int], a Tree[int] of any depth), and a fold's
// two-parameter step takes a family of pure functions. The three true
// laws are validated; a law that swaps the step's arguments is refuted at
// a member that tells them apart.
//
// Mutants (eval mutants.json): ladt-union-untyped (a type parameter
// inside a union is no int: "a generator for T"), ladt-no-two-family
// (a two-parameter function takes no family: FoldOfMap is skipped),
// ladt-union-one-class (a union's classes stop at its first variant).
func TestLicenceOverUnions(t *testing.T) {
	pkg, diags := check.CheckSource(licenceADTSrc)
	if pkg == nil || len(diags) > 0 {
		t.Fatalf("the fixture does not check: %v", diags)
	}
	byName := map[string]*check.Law{}
	for _, law := range pkg.Laws {
		byName[law.Name] = law
	}
	run := func(name string) Licence {
		t.Helper()
		law := byName[name]
		if law == nil || law.TopRewrite() == nil {
			t.Fatalf("%s is no rewrite in scope", name)
		}
		l, err := LicenceRun(pkg, law, LicenceBudget(), nil, nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return l
	}
	for _, name := range []string{"MapFuses", "TreeMapFuses", "FoldOfMap"} {
		l := run(name)
		if l.Verdict != "validated" || len(l.Missed) > 0 {
			t.Errorf("%s: %s; want validated, every class reached", name, l.Line())
		}
		// Each variant of the tree is a class of its own.
		if name == "TreeMapFuses" && (!strings.Contains(l.Line(), "Leaf ") || !strings.Contains(l.Line(), "Node[T] ")) {
			t.Errorf("%s: %s; want a class for Leaf and one for Node", name, l.Line())
		}
	}
	if l := run("FoldFlips"); l.Verdict != "refuted" || !strings.Contains(l.Line(), "g = func a") {
		t.Errorf("FoldFlips: %s; want refuted at a step that tells its arguments apart", l.Line())
	}
}
