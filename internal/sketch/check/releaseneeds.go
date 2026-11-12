package check

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Release needs (resources R5): a release runs the Close of each part
// of the value it releases (Drop), in the frame that owns the value, so
// the capabilities those Closes require are charged to that frame as a
// call of the Close there would be. One rule for every point that may run
// a release (a scope's end, a return, a failure edge, a handoff into a
// callee that owns the value, a task's end): each is in the scope of the
// value's owner, so the charge is made where the owner acquires the value:
// an owned local or a taken parameter at its name, a temporary a statement
// owns at its expression, the result a std call releases at the call, and
// the fields a record update or spread does not copy at its base. An
// inferred row takes the need, as it takes a call's (rows.md, Inference);
// a written row that lacks it is S102 there, naming the Close, the
// released type and the capability. A with's own providers keep their rule
// (resources.md, Context ownership 6; withown.go chargeRelease).
//
// Ownership is decided after the bodies are typed (ownership.go), and the
// rows are inferred while they are typed. So a package whose owned values
// need a capability is checked twice: the first check finds the owned
// values (releaseSeeds), the second charges each at its node while the
// bodies are typed (chargeOwned), so the inferred rows reach their callers
// by the same fixpoint as any call. Ownership does not read rows, so the
// second check finds the same owned values.
//
// Prior art: Koka types a finally action with the effects of the frame
// around it; Rust's Drop has no effects to charge; Gleam, Elixir
// and ReScript own nothing (a BEAM port closes with its process).

// relSeed is a node whose owned value has a release that needs a
// capability. Fields names the fields of the node's record type that a
// record update or spread replaces; nil means the node's whole value.
type relSeed struct {
	fields []string
}

// releaseSeeds collects, after the ownership pass, each node whose owned
// value's release needs a capability.
func (c *checker) releaseSeeds() map[syntax.Node]relSeed {
	info := c.pkg.Info
	seeds := map[syntax.Node]relSeed{}
	add := func(n syntax.Node, d *Drop, field string) {
		if n == nil || len(c.dropNeeds(d)) == 0 {
			return
		}
		s := seeds[n]
		if field != "" {
			s.fields = append(s.fields, field)
		}
		seeds[n] = s
	}
	replaced := map[syntax.Expr]bool{}
	for lit, rs := range info.Replaced {
		for _, r := range rs {
			replaced[r.Temp] = true
			if lit.Base != nil && r.Field != nil {
				add(lit.Base, info.OwnedTemps[r.Temp], r.Field.Name)
			}
		}
	}
	for v, d := range info.OwnedVars {
		if v.id != nil {
			add(v.id, d, "")
		}
	}
	for x, d := range info.OwnedTemps {
		if !replaced[x] {
			add(x, d, "")
		}
	}
	for call, d := range info.ResultDrops {
		add(call, d, "")
	}
	return seeds
}

// relNeed is one capability a release needs and the Close that needs it.
type relNeed struct {
	cap   Type
	close *Close
}

// dropNeeds are the capabilities the Closes of d's graph require, each
// with the first Close in release order that requires it.
func (c *checker) dropNeeds(d *Drop) []relNeed {
	var out []relNeed
	seenCap := map[capKey]bool{}
	seen := map[*Drop]bool{}
	var walk func(d *Drop)
	walk = func(d *Drop) {
		if d == nil || seen[d] {
			return
		}
		seen[d] = true
		if d.Close != nil {
			for _, cap := range c.releaseNeeds(d.Close) {
				if k, ok := keyOf(cap); ok {
					if seenCap[k] {
						continue
					}
					seenCap[k] = true
				}
				out = append(out, relNeed{cap: cap, close: d.Close})
			}
		}
		for _, f := range d.Fields {
			walk(f.Drop)
		}
		walk(d.Elem)
		for _, v := range d.Variants {
			walk(v)
		}
		walk(d.Drain)
	}
	walk(d)
	return out
}

// chargeOwned charges at n, while its frame is typed, the needs of the
// release of the value n acquires, of type t (relSeed).
func (c *checker) chargeOwned(n syntax.Node, t Type, s relSeed) {
	if t == nil || isInvalid(t) {
		return
	}
	types := []Type{t}
	if s.fields != nil {
		types = types[:0]
		rt := t
		if p, ok := rt.(*Pointer); ok {
			rt = p.Elem
		}
		if st, ok := under(rt).(*Struct); ok {
			for _, name := range s.fields {
				for _, f := range st.Fields {
					if f.Name == name {
						types = append(types, f.Type)
					}
				}
			}
		}
	}
	for _, pt := range types {
		d, _ := c.dropOf(pt)
		for _, need := range c.dropNeeds(d) {
			what := fmt.Sprintf("%s.Close, run by the release of %s here,", need.close.Receiver, pt)
			c.chargeAs(n, need.cap, what)
		}
	}
}
