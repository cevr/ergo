package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A bound by use for flat unions (R3b; union-repr.md, Threshold and
// round 2; types.md Unions 6). The register bound of flatunion.go's step 4
// (32 bytes, 4 fields) was measured for a value carried unchanged through
// calls: above it Go passes the struct through memory, 15 to 19 ns per
// call more than a box's two words. A value built and consumed at once
// (built, stored in a slice or a record, read back and matched) gains at
// every size measured, because the box costs an allocation per value and
// a load per read. So a union past the register bound stays flat when no
// value of it is carried: when no call passes one to a parameter that
// carries it on.
//
// A parameter carries its argument (carries) when the callee returns it
// unchanged (a return or a tail), passes it unchanged to a parameter that
// carries it, binds it whole to another name, captures it in a function
// literal, or hands it to a callee the lowering cannot see (a function
// value, an operation, a conversion). A Go function (an extern) is a leaf:
// it reads or stores its argument, one copy, as a store into a slice is.
// Every other use consumes it: a match, a field read, a store into a
// record, a slice or a Map, a comparison. A recursive function carries
// every parameter while it is decided.
//
// A union is carried (carriedUnions) when some call of the program passes
// a value whose type is the union to a carrying parameter, of an Ergo body
// (a generic one too: a parameter of type T carries a union T stands for)
// or of a callee the lowering cannot see; and when a function value takes
// or returns it (a function literal, or a declared function used other
// than in a call), since its caller is one the lowering cannot see; and
// when a carried union holds it in place (carryPayload).
//
// The flat form keeps one more bound, on memory, not registers: a slice
// of a flat union holds every element at the size of its widest form, so
// a union of more than useBytes bytes stays an interface (each box is the
// size of its own variant).
//
// The reference is unionBound "register": the register bound alone, as
// before R3b. TestFlatUnionsAgree runs generated unions under the rule and
// both references, and checks each union's form against the model,
// carried or not. knownStats.UsedFlat counts the unions R3b keeps flat.
//
// Prior art: Rust and Swift lay every enum out inline at any size and
// leave the box (Box, indirect) to the programmer; Rust's
// clippy::large_enum_variant flags a variant much larger than the others,
// the waste this file bounds. OCaml and ReScript box every constructor
// with fields; Gleam and Elixir keep a variant a tagged tuple on the
// BEAM's heap. None of them decides the form from the uses.
var unionBound string

// paramCarry is false only under the mutant r3b-function-params-consumed.
const paramCarry = true

// useBytes is the memory bound of a union flat by use (above).
const useBytes = 128

// msb3Lowering "off" is the reference of MSB3: a machine's signal keeps
// the bounds (stateFlat), a union with a variant that is an error type is
// an interface (flatunion.go's flatShape), as before MSB3. It reads the
// same packages as the rule (unionPkgs): a std union takes its user
// copy's form in every mode (STD-SPEED-BY-SHAPE). Set by a test or by a
// build with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.msb3Lowering=off.
var msb3Lowering string

// A machine's state is flat past both bounds (MSB2). A step is a function
// value std's run calls, so its state is carried by every step, and each
// step that returns a new state of a variant with fields boxed it: one
// allocation per signal (router's Idle, a courier's Open). Flat, the state
// travels inline in the step's Decision (genericunion.go, no size bound)
// and in the actor's field: a copy of its bytes where the box cost an
// allocation and a load. An actor holds one state, so the use bound,
// which bounds a slice's waste, does not apply; a slice of states pays
// the widest form per element, as Rust's and Swift's enums do.
//
// A machine's signal is flat past both bounds too (MSB3): std's run sends
// each signal through the actor's mailbox and hands it to a step, a
// function value, so it is carried, and each signal of a variant with
// fields boxed (router's Told{Delivered}, three strings).
//
// The reference is unionBound "register" too: there a state or a signal
// follows the register bound alone, as before R3b and MSB2.
func (l *lowerer) stateFlat(tn *check.TypeName, states map[*check.TypeName]bool) bool {
	return unionBound != "register" && states[tn]
}

// flatByUse reports whether the union tn may be flat past the register
// bound: no call carries a value of it, and the reference is not chosen.
func (l *lowerer) flatByUse(tn *check.TypeName) bool {
	return unionBound != "register" && !l.carriedUnions()[tn]
}

// carriedUnions is the set of the program's unions some call carries
// (above), computed once.
func (l *lowerer) carriedUnions() map[*check.TypeName]bool {
	if l.carried != nil {
		return l.carried
	}
	l.carried = map[*check.TypeName]bool{}
	for _, p := range l.unionPkgs() { // std's calls carry std's unions too
		info := p.Info
		for _, f := range p.Files() {
			// called are the names a call calls, seen at the call before
			// its callee (Inspect visits a parent first).
			called := map[*syntax.Ident]bool{}
			syntax.Inspect(f, func(n syntax.Node) bool {
				if x, ok := n.(*syntax.CallExpr); ok {
					fun := unparen(x.Fun)
					if ix, ok := fun.(*syntax.IndexExpr); ok {
						fun = unparen(ix.X)
					}
					switch fn := fun.(type) {
					case *syntax.Ident:
						called[fn] = true
					case *syntax.SelectorExpr:
						called[fn.Sel] = true
					}
				}
				switch y := n.(type) {
				case *syntax.FuncLit:
					// A function value's result goes to a caller the
					// lowering cannot see: it carries.
					if sig, ok := info.Types[y].(*check.Signature); ok {
						l.carryResults(sig)
					}
				case *syntax.Ident:
					if g, ok := info.Uses[y].(*check.Func); ok && g.Sig != nil && !called[y] {
						l.carryResults(g.Sig) // a declared function taken as a value
					}
				}
				x, ok := n.(*syntax.CallExpr)
				if !ok {
					return true
				}
				c := info.Calls[x]
				if c == nil {
					return true
				}
				for j, a := range callOperands(x, c) {
					t, typed := typedIn(info, a)
					if !typed {
						continue
					}
					u, ok := t.(*check.Named)
					if !ok || l.carried[u.Obj] {
						continue
					}
					if _, union := u.Underlying().(*check.Union); !union {
						continue
					}
					if l.passCarries(c, j) {
						l.carried[u.Obj] = true
					}
				}
				return true
			})
		}
	}
	if payloadCarry {
		for tn := range l.carried {
			l.carryPayload(tn.Type, map[check.Type]bool{})
		}
	}
	return l.carried
}

// payloadCarry is false only under the mutant r3b-payload-consumed.
const payloadCarry = true

// carryPayload marks carried every union a value of t holds in place (a
// variant's or a record's field, not through a slice, a Map or a box): a
// carried union carries its payload, and a union flat inside it would
// widen it past the bound it keeps (backups' Signal holds an Event).
func (l *lowerer) carryPayload(t check.Type, seen map[check.Type]bool) {
	if t == nil || seen[t] {
		return
	}
	seen[t] = true
	under := t
	if n, ok := t.(*check.Named); ok {
		under = n.Underlying()
	}
	switch u := under.(type) {
	case *check.Union:
		if n, ok := t.(*check.Named); ok {
			l.carried[n.Obj] = true
		}
		for _, v := range u.Variants {
			l.carryPayload(v, seen)
		}
	case *check.Struct:
		for _, f := range u.Fields {
			l.carryPayload(f.Type, seen)
		}
	}
}

// carryResults marks carried every union a parameter or a result of sig
// is: a function value is called by a caller the lowering cannot see,
// which passes its arguments and takes its result by value (a machine's
// step, called by std's run with the signal). A union inside a tuple
// result is not (tupleCarry, below).
func (l *lowerer) carryResults(sig *check.Signature) {
	types := append([]check.Type{}, sig.Results...)
	if paramCarry {
		for _, p := range sig.Params {
			types = append(types, p.Type)
		}
	}
	for i := 0; i < len(types); i++ {
		t := types[i]
		if tu, ok := t.(*check.Tuple); ok && tupleCarry {
			types = append(types, tu.Types...)
			continue
		}
		if u, ok := t.(*check.Named); ok {
			if _, union := u.Underlying().(*check.Union); union {
				l.carried[u.Obj] = true
			}
		}
	}
}

// tupleCarry is true only under the mutant msb3b-tuple-carried. A union in
// a function value's tuple result is not carried (MSB3b): sync.Modify's
// literal returns (R, T), and Go's RefModify hands R back once, so a flat
// R is one copy where a boxed one is an allocation per call. Measured on
// coord's shape (stores EX5a: memory/leases.ergo's stepped, an 88-byte
// union, 10^6 turns): flat 330M instructions and no allocation per turn,
// boxed 490M and 0.69. A union a function value returns alone stays
// carried: through tasks.Transform, which collects each result, the flat
// form ran 87 % more instructions (MSB3b probes, dod/msb3b).
const tupleCarry = false

// passCarries reports whether operand j of the call c is carried on by its
// callee (above).
func (l *lowerer) passCarries(c *check.Call, j int) bool {
	switch c.Kind {
	case check.CallFunc:
	case check.CallLen:
		return false
	default:
		return true // an operation, a function value, a conversion
	}
	g := c.Func
	if g == nil {
		return true
	}
	if g.Decl != nil && g.Decl.Extern != nil {
		return false // a Go leaf
	}
	if g.Decl == nil || g.Decl.Body == nil {
		return true
	}
	k := l.carries(g)
	return j >= len(k) || k[j]
}

// carries is f's summary, indexed as f.Sig.Params: whether parameter i
// carries its argument on (above).
func (l *lowerer) carries(f *check.Func) []bool {
	all := func() []bool {
		out := make([]bool, len(f.Sig.Params))
		for i := range out {
			out[i] = true
		}
		return out
	}
	d := f.Decl
	if d == nil || d.Body == nil || d.Extern != nil || f.Pkg == nil || f.Pkg.Info == nil {
		return all()
	}
	if l.carrying == nil {
		l.carrying = map[*check.Func][]bool{}
	}
	if k, ok := l.carrying[f]; ok {
		if k == nil {
			return all() // being decided: recursion carries everything
		}
		return k
	}
	vars := paramVars(f)
	if len(vars) != len(f.Sig.Params) {
		return all()
	}
	l.carrying[f] = nil
	info := f.Pkg.Info
	fl := newFlow(l, info, d)
	out := make([]bool, len(vars))
	for i, v := range vars {
		if v == nil {
			continue
		}
		for _, id := range fl.uses[v] {
			if fl.inLit[id] || l.useCarries(fl, info, id) {
				out[i] = true
				break
			}
		}
	}
	l.carrying[f] = out
	return out
}

// useCarries reports whether the use id of a parameter carries its value
// on (above).
func (l *lowerer) useCarries(fl *flow, info *check.Info, id *syntax.Ident) bool {
	var cur syntax.Node = id
	p := fl.parent[cur]
	for {
		if _, ok := p.(*syntax.ParenExpr); !ok {
			break
		}
		cur, p = p, fl.parent[p]
	}
	switch p := p.(type) {
	case *syntax.ReturnStmt:
		return true
	case *syntax.ExprStmt:
		_, tail := info.Tails[p]
		return tail
	case *syntax.CallExpr:
		c := info.Calls[p]
		if c == nil {
			return true
		}
		if syntax.Node(p.Fun) == cur {
			return false // a call of it: a function value, not a union
		}
		j := operandIndex(p, c, cur)
		if j < 0 {
			return true // a trailing block's or an unlisted operand
		}
		return l.passCarries(c, j)
	case *syntax.AssignStmt:
		// A whole value bound to another name is followed no further: it
		// carries. A store into a field or an element consumes it.
		for i, r := range p.Rhs {
			if syntax.Node(r) == cur && i < len(p.Lhs) {
				_, name := unparen(p.Lhs[i]).(*syntax.Ident)
				return name
			}
		}
	case *syntax.VarSpec:
		return true
	}
	return false
}
