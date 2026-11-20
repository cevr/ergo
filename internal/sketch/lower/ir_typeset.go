package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The Go form of a type set (types.md, Generics 2; CONSTRAINTS C1). An
// operation type with a type-set member, the prelude's ordered and number
// as much as a user's `type Count { ~int | ~int64 }`, lowers to a Go
// interface of type terms, emitted once per program and named after its
// declaration: the Go a person writes for the same constraint. A generic
// body under it takes exactly the operators Go's type set admits, and Go
// stencils it by GC shape as it does cmp.Ordered. No rule here reads a
// declaration's name or package: every type set takes the same path.

// typeSetOrigin is the declaration of the type set that tp's constraint
// has, or nil when the constraint has none.
func typeSetOrigin(tp *check.TypeParam) *check.TypeName {
	n, ok := tp.Constraint.(*check.Named)
	if !ok {
		return nil
	}
	if it, ok := n.Underlying().(*check.Interface); !ok || it.Terms == nil {
		return nil
	}
	if n.Orig != nil {
		n = n.Orig
	}
	return n.Obj
}

// reserveTypeSets names the Go constraint of every type set the packages
// declare (the naming pass, naming.go).
func (l *lowerer) reserveTypeSets(pkgs []*check.Package) {
	for _, p := range pkgs {
		for _, tn := range l.canon.types(p) {
			n, ok := tn.Type.(*check.Named)
			if !ok {
				continue
			}
			if it, ok := n.Underlying().(*check.Interface); ok && it.Terms != nil {
				l.typeSetName(tn)
			}
		}
	}
}

// typeSetName is the Go name of the type set tn declares.
func (l *lowerer) typeSetName(tn *check.TypeName) string {
	return l.pkgName(pkgKey{"constraint", tn}, "ergo"+strings.ToUpper(tn.Name[:1])+tn.Name[1:])
}

// typeSetConstraint is the Go constraint of the type set tn declares:
// comparable when a term is comparable (Go admits comparable in no union,
// and the set then gives == alone), else the generated interface.
func (l *lowerer) typeSetConstraint(tn *check.TypeName, site syntax.Node) string {
	it := tn.Type.(*check.Named).Underlying().(*check.Interface)
	terms, comparable := l.goTerms(it.Terms, site)
	if comparable {
		return "comparable"
	}
	name := l.typeSetName(tn)
	if l.sets[tn] {
		return name
	}
	l.sets[tn] = true
	l.gen.WriteString("// " + name + " is the type set of " + tn.Name + ".\n")
	l.gen.WriteString("type " + name + " interface {\n\t" + strings.Join(terms, " | ") + "\n}\n\n")
	return name
}

// goTerms spells the terms in Go, in order, each once. Go wants the terms
// of a union disjoint: a bare term whose underlying type a ~ term covers
// is left out (the ~ term admits it), and ~duration is ~int64, which
// time.Duration is defined over.
func (l *lowerer) goTerms(terms []*check.Term, site syntax.Node) ([]string, bool) {
	tilde := func(t check.Type) string {
		b, ok := t.(*check.Basic)
		if !ok {
			return ""
		}
		if b.Kind == check.Duration {
			return "~int64"
		}
		return "~" + b.Name
	}
	have := map[string]bool{}
	for _, term := range terms {
		if it, ok := term.Type.(*check.Interface); ok && it.Set == check.SetComparable {
			return nil, true
		}
		if term.Tilde {
			have[tilde(term.Type)] = true
		}
	}
	tc := &typeCtx{l: l, site: site}
	var out []string
	seen := map[string]bool{}
	for _, term := range terms {
		s := ""
		if term.Tilde {
			s = tilde(term.Type)
		} else {
			u := term.Type
			if n, ok := u.(*check.Named); ok {
				u = n.Underlying()
			}
			if have[tilde(u)] {
				continue
			}
			s = tc.goType(term.Type)
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, false
}
