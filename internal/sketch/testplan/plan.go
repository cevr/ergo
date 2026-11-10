// Package testplan holds checked, target-independent case decisions shared by
// check-time evaluation and the native test emitter. It contains no Go text.
package testplan

import (
	"math/big"

	"ergo.local/prototype/internal/sketch/check"
)

const (
	TypeCap = 256
	CaseCap = 4096
	Cases   = 100
	Depth   = 4
)

// Value is a member of a finite domain. Scalar holds a bool or a decimal
// integer string; Children are tuple slots, record fields, or one union member.
type Value struct {
	Type     check.Type
	Scalar   any
	Children []Value
}

// Domain lists t's values in source field/variant order, last field fastest.
// Recursive, unbounded and constructor-backed types are not finite domains.
func Domain(t check.Type) ([]Value, bool) { return domain(t, map[*check.Named]bool{}) }

func domain(t check.Type, seen map[*check.Named]bool) ([]Value, bool) {
	product := func(ts []check.Type) ([]Value, bool) {
		out := []Value{{Type: t}}
		for _, ft := range ts {
			vs, ok := domain(ft, seen)
			if !ok || len(vs) == 0 || len(out) > TypeCap/len(vs) {
				return nil, false
			}
			var next []Value
			for _, prefix := range out {
				for _, v := range vs {
					children := append(append([]Value(nil), prefix.Children...), v)
					next = append(next, Value{Type: t, Children: children})
				}
			}
			out = next
		}
		return out, true
	}
	switch u := t.(type) {
	case *check.Basic:
		if u.Kind == check.Bool {
			return []Value{{Type: t, Scalar: false}, {Type: t, Scalar: true}}, true
		}
	case *check.Tuple:
		return product(u.Types)
	case *check.Named:
		if Constructor(u) != nil {
			return nil, false
		}
		if ref := check.RefinementOf(u); ref != nil {
			if ref.Len || ref.Count().Cmp(big.NewInt(TypeCap)) > 0 {
				return nil, false
			}
			var out []Value
			for _, s := range ref.Spans {
				for n := new(big.Int).Set(s.Lo); n.Cmp(s.Hi) <= 0; n.Add(n, big.NewInt(1)) {
					out = append(out, Value{Type: t, Scalar: n.String()})
				}
			}
			return out, true
		}
		if seen[u] {
			return nil, false
		}
		seen[u] = true
		defer delete(seen, u)
		switch under := u.Underlying().(type) {
		case *check.Struct:
			ts := make([]check.Type, len(under.Fields))
			for i, f := range under.Fields {
				ts[i] = f.Type
			}
			return product(ts)
		case *check.Union:
			var out []Value
			for _, variant := range under.Variants {
				vs, ok := domain(variant, seen)
				if !ok || len(out)+len(vs) > TypeCap {
					return nil, false
				}
				for _, v := range vs {
					out = append(out, Value{Type: t, Children: []Value{v}})
				}
			}
			return out, len(out) > 0
		case *check.Basic:
			vs, ok := domain(under, seen)
			for i := range vs {
				vs[i] = Value{Type: t, Children: []Value{vs[i]}}
			}
			return vs, ok
		}
	}
	return nil, false
}

// Input is one generated value, in draw order. Param -1 is the owner provider;
// Provider is nil for an ordinary parameter. Constructors are called anew in
// each case; they never make a finite proof over their output type.
type Input struct {
	Name     string
	Type     check.Type
	Param    int
	Provider *check.LawProvider
	Where    *check.Refinement
}

type Plan struct {
	Inputs []Input
	Lists  [][]Value
	Count  int
	Listed bool
	Reason string
}

func makePlan(inputs []Input) Plan {
	p := Plan{Inputs: inputs, Count: 1, Listed: true}
	for _, in := range inputs {
		if in.Provider != nil {
			if in.Provider.Unrun != "" {
				p.Reason = in.Provider.Unrun
			}
			if in.Provider.Ctor != nil {
				p.Listed = false
			}
		}
		vs, ok := Domain(in.Type)
		// A where is a generator restriction, not proof over the unfiltered
		// type. Until a finite filtered domain is planned, sample it.
		if in.Where != nil {
			ok = false
		}
		if !ok || len(vs) == 0 || p.Count > CaseCap/len(vs) {
			p.Listed = false
		}
		if p.Listed {
			p.Count *= len(vs)
			p.Lists = append(p.Lists, vs)
		}
	}
	if !p.Listed {
		p.Count, p.Lists = Cases, nil
	}
	return p
}

func Test(t *check.Test) Plan {
	var ins []Input
	for i, v := range t.Params {
		ins = append(ins, Input{Name: v.Name, Type: v.Type, Param: i})
	}
	return makePlan(ins)
}

func Law(l *check.Law, bd []check.LawProvider) Plan {
	if len(bd) != len(l.Slots) {
		return Plan{Reason: "no binding of the law runs in this package"}
	}
	bound := map[int]*check.LawProvider{}
	var ins []Input
	for k, slot := range l.Slots {
		if slot.Param < 0 {
			ins = append(ins, Input{Name: "provider", Type: bd[k].Type, Param: -1, Provider: &bd[k]})
		} else {
			bound[slot.Param] = &bd[k]
		}
	}
	for i, v := range l.Params {
		t := v.Type
		if GenericOwner(l) != nil {
			t = bd[0].Params[i]
		}
		if b := bound[i]; b != nil {
			t = b.Type
		}
		in := Input{Name: v.Name, Type: t, Param: i, Provider: bound[i]}
		if i < len(l.Where) {
			in.Where = l.Where[i]
		}
		ins = append(ins, in)
	}
	p := makePlan(ins)
	if l.Unrun != "" {
		p.Reason = l.Unrun
	}
	return p
}

func GenericOwner(l *check.Law) *check.Named {
	if len(l.Slots) > 0 && l.Slots[0].Param < 0 && len(l.Slots[0].Type.TParams) > 0 {
		return l.Slots[0].Type
	}
	return nil
}

func Constructor(n *check.Named) *check.Func {
	if n.Obj == nil || n.Orig != nil {
		return nil
	}
	return n.Obj.Constructor()
}

// At returns the listed case k (zero based), the last input fastest.
func (p Plan) At(k int) []Value {
	out := make([]Value, len(p.Lists))
	for i := len(out) - 1; i >= 0; i-- {
		out[i] = p.Lists[i][k%len(p.Lists[i])]
		k /= len(p.Lists[i])
	}
	return out
}

// SmallVariants is the checked value-rank decision at the depth boundary.
func SmallVariants(variants []check.Type) []check.Type {
	ranks, least := check.NewValueRanks(), -1
	var small []check.Type
	for _, v := range variants {
		switch k := ranks.Rank(v); {
		case k < 0:
		case least < 0 || k < least:
			least, small = k, []check.Type{v}
		case k == least:
			small = append(small, v)
		}
	}
	return small
}
