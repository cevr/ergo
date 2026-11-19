package facts

import (
	"slices"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The rows relations (SEMTOOLS SEM2b, SEM2c, SEM2e): each function's derived
// needs and fails rows, and what each call site charges its declaration.
// A call charges the capabilities of its callee's row at the site
// (check.Call.Needs); those a with around the call discharges (its block
// holds the call, and the with's Discharged names the capability) are
// Discharges, each with the provider of the innermost such with's entry
// (Provided), the rest Brings. A call raises the failures of its callee's
// error row at the site (check.Call.Fails); those a catch around it
// handles (the catch's expression holds the call, and the catch is total
// or an arm tests the failure's type or matches a record pattern of it)
// are Caught, the rest Raises. A failure an arm takes only some values of
// (a record pattern that tests a field) is both: the rest still
// propagate. A call in a function literal is charged to
// the declaration that holds the literal, and a with or a catch around
// the literal discharges it.

// charge is what the call whose name is at (file, off) charges, by
// symbol id: capabilities and failures.
type charge struct {
	file  uint16
	off   uint32
	caps  []uint32
	fails []uint32
}

// withBlock is a with's block, by file and offsets, with the capability
// ids it discharges (every operand's) and, for each, the ids of the
// providers its entries bind it to.
type withBlock struct {
	file      uint16
	from, to  uint32
	caps      []uint32
	providers [][]uint32
}

// catchBlock is a catch's expression, by file and offsets, with the
// failure ids its arms catch every value of, or all for a total catch,
// and those an arm catches only some values of (partly).
type catchBlock struct {
	file     uint16
	from, to uint32
	all      bool
	fails    []uint32
	partly   []uint32
}

// irrefutable is whether p matches every value of its type: a wildcard or
// a name, or a tuple or an as of such. A record pattern, a literal or a
// type test inside a pattern tests its value, so the pattern takes only
// some values (a nested record pattern may name a variant of a union).
func irrefutable(p *check.Pattern) bool {
	switch p.Kind {
	case check.PatWildcard, check.PatBind:
		return true
	case check.PatAs, check.PatTuple:
		for _, e := range p.Elems {
			if e == nil || !irrefutable(e) {
				return false
			}
		}
		return true
	}
	return false
}

// callName is the name a call's site is at: the called name of x.Fun.
func callName(x syntax.Expr) *syntax.Ident {
	for {
		switch e := x.(type) {
		case *syntax.Ident:
			return e
		case *syntax.SelectorExpr:
			return e.Sel
		case *syntax.IndexExpr:
			x = e.X
		case *syntax.ParenExpr:
			x = e.X
		default:
			return nil
		}
	}
}

// capability is the symbol of an element of a needs or an error row: a
// named type, by its type's symbol. A row variable has none.
func (b *builder) capability(t check.Type) (uint32, bool) {
	n := named(t)
	if n != nil && n.Orig != nil {
		n = n.Orig
	}
	if n == nil || n.Obj == nil {
		return 0, false
	}
	key, sym, ok := b.symbol(n.Obj, nil, false)
	if !ok {
		return 0, false
	}
	return b.id(key, sym), true
}

// capabilities is the symbols of r's elements, in r's order.
func (b *builder) capabilities(r *check.Row) []uint32 {
	var out []uint32
	for _, t := range r.Elems() {
		if id, ok := b.capability(t); ok && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// entryProvider is the symbol entry's provider expression names, read
// with the Info of the package that declares the entry's context (pkg's
// own for an inline or a local context, an import's for its named one).
func (b *builder) entryProvider(entry *check.ContextEntry) (uint32, bool) {
	info := b.info
	if p := b.entryPkg[entry]; p != nil && p.Info != nil {
		info = p.Info
	}
	return b.provider(entry.Provider, info)
}

// noteContexts records the package of each entry of c and of the
// contexts it embeds.
func (b *builder) noteContexts(c *check.Context) {
	if c == nil || c.Package() == nil {
		return
	}
	if b.entryPkg == nil {
		b.entryPkg = map[*check.ContextEntry]*check.Package{}
	}
	for _, e := range c.Entries {
		if _, done := b.entryPkg[e]; done {
			continue
		}
		b.entryPkg[e] = c.Package()
		b.noteContexts(e.Embedded)
	}
}

// provider is the symbol a context entry's provider expression names, by
// info: the function it calls, the type it builds, or the name it is.
func (b *builder) provider(e syntax.Expr, info *check.Info) (uint32, bool) {
	switch x := e.(type) {
	case *syntax.CallExpr:
		e = x.Fun
	case *syntax.CompositeLit:
		e = x.Type
	}
	name := callName(e)
	if name == nil {
		return 0, false
	}
	o := info.Uses[name]
	if sel, ok := e.(*syntax.SelectorExpr); ok && o == nil {
		if q, ok := sel.X.(*syntax.Ident); ok {
			if pn, ok := info.Uses[q].(*check.PkgName); ok && pn.Pkg != nil {
				o = b.exportedOf(pn.Pkg)[name.Name]
			}
		}
	}
	if o == nil {
		return 0, false
	}
	key, sym, ok := b.symbol(o, name, false)
	if !ok {
		return 0, false
	}
	return b.id(key, sym), true
}

// noteCall records what the call x charges and, for a with, its block.
func (b *builder) noteCall(x *syntax.CallExpr) {
	if c := b.info.Calls[x]; c != nil && (!c.Needs.Empty() || !c.Fails.Empty()) {
		if name := callName(x.Fun); name != nil {
			b.charges = append(b.charges, charge{file: b.file, off: uint32(name.NamePos), caps: b.capabilities(c.Needs), fails: b.capabilities(c.Fails)})
		}
	}
	if w := b.info.Withs[x]; w != nil && x.Trailing != nil {
		wb := withBlock{file: b.file, from: uint32(x.Trailing.Lbrace), to: uint32(x.Trailing.Rbrace)}
		for ; w != nil; w = w.Next {
			for _, c := range w.Contexts {
				b.noteContexts(c)
			}
			for i, t := range w.Discharged {
				id, ok := b.capability(t)
				if !ok {
					continue
				}
				k := slices.Index(wb.caps, id)
				if k < 0 {
					k = len(wb.caps)
					wb.caps = append(wb.caps, id)
					wb.providers = append(wb.providers, nil)
				}
				if i < len(w.Entries) && w.Entries[i] != nil && w.Entries[i].Provider != nil {
					if p, ok := b.entryProvider(w.Entries[i]); ok && !slices.Contains(wb.providers[k], p) {
						wb.providers[k] = append(wb.providers[k], p)
					}
				}
			}
		}
		if len(wb.caps) > 0 {
			b.withs = append(b.withs, wb)
		}
	}
}

// noteCatch records the expression a catch handles failures of.
func (b *builder) noteCatch(x *syntax.CatchExpr) {
	cb := catchBlock{file: b.file, from: uint32(x.X.Pos()), to: uint32(x.Catch)}
	if m := b.info.Matches[x]; m == nil || !m.Partial {
		cb.all = true
	}
	for _, arm := range x.Arms {
		for _, te := range arm.Types {
			core := te
			if a, ok := te.(*syntax.AsPattern); ok {
				core = a.X
			}
			p := b.info.Patterns[core]
			if p == nil {
				continue
			}
			// An arm that catches the rest makes the catch total (all
			// above); an arm that tests a type, or matches a record
			// pattern of it, catches that failure, or each of a union's.
			whole := p.Kind == check.PatTypeTest
			if p.Kind == check.PatRecord {
				whole = true
				for _, fp := range p.Fields {
					whole = whole && fp.Pattern != nil && irrefutable(fp.Pattern)
				}
			}
			switch p.Kind {
			case check.PatRecord:
				if id, ok := b.capability(p.Type); ok && whole {
					cb.fails = append(cb.fails, id)
				} else if ok {
					cb.partly = append(cb.partly, id)
				}
			case check.PatTypeTest:
				if id, ok := b.capability(p.Type); ok {
					cb.fails = append(cb.fails, id)
				}
				if n := named(p.Type); n != nil {
					if u, ok := n.Underlying().(*check.Union); ok {
						for _, v := range u.Variants {
							if id, ok := b.capability(v); ok {
								cb.fails = append(cb.fails, id)
							}
						}
					}
				}
			}
		}
	}
	b.catches = append(b.catches, cb)
}

// needs writes an edge from each function of the package to each
// capability the checker derived for it (Needs), and to each failure
// (Fails), in the package's function order.
func (b *builder) needs() {
	for _, f := range b.pkg.Funcs {
		if f.Sig == nil || f.Sig.Uses.Empty() && f.Sig.Errors.Empty() {
			continue
		}
		key, _, ok := b.funcSymbol(f)
		if !ok {
			continue
		}
		id, ok := b.ids[key]
		if !ok || !b.t.Symbols[id].Declared {
			continue
		}
		for _, c := range b.capabilities(f.Sig.Uses) {
			b.t.Needs = append(b.t.Needs, Edge{From: id, To: c, Site: NoSym})
		}
		for _, c := range b.capabilities(f.Sig.Errors) {
			b.t.Fails = append(b.t.Fails, Edge{From: id, To: c, Site: NoSym})
		}
	}
}

// chargeEdges writes, for each call edge in its order, an edge from the
// caller to each capability the call charges (Brings, or Discharges when
// a with around the call discharges it, with an edge from each provider
// of the innermost such with to the capability, Provided) and to each
// failure it raises (Raises, or Caught when a catch around it handles it;
// both when catches around it take only some of its values).
func (b *builder) chargeEdges() {
	type at struct {
		file uint16
		off  uint32
	}
	byName := map[at]charge{}
	for _, c := range b.charges {
		byName[at{c.file, c.off}] = c
	}
	t := b.t
	for _, e := range t.Calls {
		s := t.Sites[e.Site]
		ch := byName[at{s.File, s.Off}]
		for _, c := range ch.caps {
			discharged := false
			var by []uint32
			span := ^uint32(0)
			for _, w := range b.withs {
				k := slices.Index(w.caps, c)
				if w.file == s.File && w.from < s.Off && s.Off < w.to && k >= 0 {
					discharged = true
					if w.to-w.from < span {
						span, by = w.to-w.from, w.providers[k]
					}
				}
			}
			if !discharged {
				t.Brings = append(t.Brings, Edge{From: e.From, To: c, Site: e.Site})
				continue
			}
			t.Discharges = append(t.Discharges, Edge{From: e.From, To: c, Site: e.Site})
			for _, p := range by {
				t.Provided = append(t.Provided, Edge{From: p, To: c, Site: e.Site})
			}
		}
		for _, f := range ch.fails {
			caught, partly := false, false
			for _, cb := range b.catches {
				if cb.file == s.File && cb.from <= s.Off && s.Off < cb.to {
					caught = caught || cb.all || slices.Contains(cb.fails, f)
					partly = partly || slices.Contains(cb.partly, f)
				}
			}
			if caught || partly {
				t.Caught = append(t.Caught, Edge{From: e.From, To: f, Site: e.Site})
			}
			if !caught {
				t.Raises = append(t.Raises, Edge{From: e.From, To: f, Site: e.Site})
			}
		}
	}
}
