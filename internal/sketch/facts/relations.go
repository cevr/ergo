package facts

import (
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/machinegraph"
	"ergo.local/prototype/internal/sketch/syntax"
)

// variants writes an edge from each union of the package to each of its
// variants.
func (b *builder) variants() {
	for _, tn := range b.pkg.Types {
		u, ok := b.ids[b.pkg.Path+"."+tn.Name]
		if !ok {
			continue
		}
		n, _ := tn.Type.(*check.Named)
		if n == nil {
			continue
		}
		un, ok := n.Underlying().(*check.Union)
		if !ok || un.Row != nil {
			continue
		}
		for _, v := range un.Variants {
			vn := named(v)
			if vn == nil || vn.Obj == nil {
				continue
			}
			if key, sym, ok := b.symbol(vn.Obj, nil, false); ok {
				b.t.Variants = append(b.t.Variants, Edge{From: u, To: b.id(key, sym), Site: NoSym})
			}
		}
	}
}

// callEdges writes an edge from each declaration to each function or
// operation it calls, one per call site, in the order of the sites.
func (b *builder) callEdges() {
	t := b.t
	for i, s := range t.Sites {
		if s.Kind == Call && s.Encl != NoSym {
			t.Calls = append(t.Calls, Edge{From: s.Encl, To: s.Sym, Site: uint32(i)})
		}
	}
	slices.SortFunc(t.Calls, func(x, y Edge) int {
		if x.From != y.From {
			return cmp(x.From, y.From)
		}
		a, c := t.Sites[x.Site], t.Sites[y.Site]
		if a.File != c.File {
			return cmp(uint32(a.File), uint32(c.File))
		}
		return cmp(a.Off, c.Off)
	})
}

// ops writes an edge from each operation to the package function that
// implements it at a conversion of this package into an operation type:
// an implicit conversion, or a context entry's provider.
func (b *builder) ops() {
	type end struct {
		key any
		sym Symbol
	}
	type pair struct{ op, fn end }
	var pairs []pair
	add := func(c check.Conversion) {
		if c.Kind != check.ConvOperations {
			return
		}
		n := named(c.To)
		if n == nil {
			return
		}
		i, _ := n.Underlying().(*check.Interface)
		if i == nil {
			return
		}
		for _, m := range i.Methods {
			f := c.Table[m.Name]
			if f == nil {
				continue
			}
			okey, osym, ok := b.opOf(m, n)
			if !ok || osym.Handle == "" {
				continue
			}
			fkey, fsym, ok := b.funcSymbol(f)
			if !ok {
				continue
			}
			pairs = append(pairs, pair{end{okey, osym}, end{fkey, fsym}})
		}
	}
	for _, c := range b.info.Implicit {
		add(c)
	}
	for _, ctx := range b.pkg.Contexts {
		for _, e := range ctx.Entries {
			for _, c := range e.Conversions {
				add(c)
			}
		}
	}
	// The maps gave the pairs in no order: sort them by handle (both ends
	// have one) before any new symbol gets its id, and drop repeats.
	slices.SortFunc(pairs, func(x, y pair) int {
		if c := strings.Compare(x.op.sym.Handle, y.op.sym.Handle); c != 0 {
			return c
		}
		return strings.Compare(x.fn.sym.Handle, y.fn.sym.Handle)
	})
	for i, p := range pairs {
		if i > 0 && p.op.sym.Handle == pairs[i-1].op.sym.Handle && p.fn.sym.Handle == pairs[i-1].fn.sym.Handle {
			continue
		}
		b.t.Ops = append(b.t.Ops, Edge{From: b.id(p.op.key, p.op.sym), To: b.id(p.fn.key, p.fn.sym), Site: NoSym})
	}
}

// machines reads each machine declaration's graph.
func (b *builder) machines(files []File, encls map[syntax.Decl]uint32) {
	fileOf := map[syntax.Node]int{}
	for fi, f := range files {
		if f.Syntax == nil {
			continue
		}
		for _, d := range f.Syntax.Decls {
			fileOf[d] = fi
		}
	}
	at := func(decl syntax.Node, pos syntax.Pos) machinegraph.Site {
		fi, ok := fileOf[decl]
		if !ok {
			return machinegraph.Site{}
		}
		return machinegraph.Site{File: files[fi].Name, Line: files[fi].Syntax.Line(pos)}
	}
	place := func(what string, s machinegraph.Site) Place { return Place{What: what, File: s.File, Line: s.Line} }
	for _, tn := range b.pkg.Types {
		spec, ok := tn.Decl.Spec.(*syntax.MachineType)
		if !ok {
			continue
		}
		id, ok := encls[tn.Decl]
		if !ok || id == NoSym {
			continue
		}
		g := machinegraph.Of(b.pkg, tn, spec, at)
		if g == nil {
			continue
		}
		m := MachineGraph{Sym: id, Initial: g.Initial, Unsure: g.Unsure}
		for _, v := range g.States {
			m.States = append(m.States, v.Name)
		}
		for _, v := range g.Signals {
			m.Signals = append(m.Signals, v.Name)
		}
		for _, a := range g.Arms {
			arm := Arm{At: place("", a.Site), States: a.States, Signals: a.Signals, Partial: a.Partial}
			for _, e := range a.Edges {
				arm.Edges = append(arm.Edges, Transition{Decision: e.Decision, To: e.To, At: place("", e.Site)})
			}
			for _, x := range a.Alts {
				arm.Alts = append(arm.Alts, Alt{States: x.States, Signals: x.Signals, Partial: x.Partial})
			}
			m.Arms = append(m.Arms, arm)
		}
		// Timers, work and exit actions are maps: list them in the
		// states' order.
		for _, s := range m.States {
			if tm, ok := g.Timers[s]; ok {
				m.Timers = append(m.Timers, Timer{State: s, Signal: tm.Signal, At: place("", tm.Site)})
			}
			if w, ok := g.Work[s]; ok {
				m.Work = append(m.Work, place(s, w))
			}
			if x, ok := g.Exit[s]; ok {
				m.Exit = append(m.Exit, place(s, x))
			}
		}
		b.t.Machines = append(b.t.Machines, m)
	}
}
