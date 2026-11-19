package check

import (
	"strconv"

	"ergo.local/prototype/internal/sketch/syntax"
)

// An extern's capability row covers its callbacks' row parameters
// (rows.md, Capability rows 8; CK-EXTROW). A function with a body charges
// what it calls (Capability rows 2); an extern's body is Go, which the
// checker does not read (D-E5), so a callback the host may call charges
// the extern nothing by itself. When a callback's capability row names a
// row parameter of the extern, the host's call of it needs that row. So
// the extern's own capability row lists the parameter, or a result's type
// holds it (the host hands the callback back, and whoever calls it later
// is charged), or another parameter's type holds it outside a row (a value
// the caller holds carries it). An extern that drops it is S102 at the
// parameter: its callers would not hold the capabilities the callback
// needs, and no provider would be in scope when it runs.
//
// Decided: error rows are not checked, by the host's contract: the host
// may absorb a callback's failure (actor's start makes a failed restart a
// defect; tasks.Go hands it to the set), while it has no way to provide a
// capability the caller does not hold. Reverse by checking failures too,
// with a written marker on a parameter whose failures the host absorbs.
//
// A function the callback receives from the host (`send` in `run
// func(send func(e E))`) is the host's, not the program's: its rows are
// not the callback's. A concrete capability in a callback's row
// (`needs Streaming`) is one the host provides; only row parameters are
// checked, as only they can be dropped with no wrong type in Go.
//
// Prior art: ReScript's external and Gleam's @external trust the written
// type entirely; Koka's extern declarations write their effect row, and a
// higher-order one carries its argument's effect variable, as here.

// externRows checks d, an extern declaration with signature sig.
func (c *checker) externRows(d *syntax.FuncDecl, sig *Signature) {
	if d.Type == nil || d.Type.Params == nil {
		return
	}
	params := d.Type.Params.List
	k := 0
	for _, field := range params {
		n := max(len(field.Names), 1)
		for range n {
			if k >= len(sig.Params) {
				return
			}
			for _, q := range callbackRowParams(sig.Params[k].Type, sig.TParams) {
				if rowNames(sig.Uses, q) || resultsHold(sig, q) || paramsHold(sig, k, q) {
					continue
				}
				c.errorf(field.Type, "S102", "the host may call %s, whose capability row has %s, which %s does not list: write needs %s (rows.md, Capability rows 8)", callbackName(sig.Params[k], k), q.Name, d.Name.Name, q.Name)
			}
			k++
		}
	}
}

func callbackName(v *Var, i int) string {
	if v.Name != "" && v.Name != "_" {
		return v.Name
	}
	return "parameter " + strconv.Itoa(i+1)
}

// callbackRowParams are the row parameters of tparams that a function the
// host may call names in its rows, in t: a function-typed t itself, its
// results, and the elements of a slice, Map, pointer, tuple or record
// type; not the functions it receives.
func callbackRowParams(t Type, tparams []*TypeParam) []*TypeParam {
	own := map[*TypeParam]bool{}
	for _, tp := range tparams {
		if tp.Kind == KindUses {
			own[tp] = true
		}
	}
	var out []*TypeParam
	seen := map[*TypeParam]bool{}
	add := func(r *Row) {
		for _, e := range r.Elems() {
			if q, ok := e.(*TypeParam); ok && own[q] && !seen[q] {
				seen[q] = true
				out = append(out, q)
			}
		}
	}
	var walk func(t Type, depth int)
	walk = func(t Type, depth int) {
		if depth > 8 {
			return
		}
		switch t := t.(type) {
		case *Signature:
			add(t.Uses)
			for _, r := range t.Results {
				walk(r, depth+1)
			}
		case *Slice:
			walk(t.Elem, depth+1)
		case *Array:
			walk(t.Elem, depth+1)
		case *Map:
			walk(t.Elem, depth+1)
		case *Pointer:
			walk(t.Elem, depth+1)
		case *Tuple:
			for _, e := range t.Types {
				walk(e, depth+1)
			}
		case *Struct:
			for _, f := range t.Fields {
				walk(f.Type, depth+1)
			}
		}
	}
	walk(t, 0)
	return out
}

func rowNames(r *Row, q *TypeParam) bool {
	for _, e := range r.Elems() {
		if e == Type(q) {
			return true
		}
	}
	return false
}

// paramsHold reports whether a parameter of sig other than the k-th holds
// q outside the rows of the functions in its type.
func paramsHold(sig *Signature, k int, q *TypeParam) bool {
	for i, p := range sig.Params {
		if i != k && holdsTyped(q, p.Type, 0) {
			return true
		}
	}
	return false
}

// holdsTyped reports whether q occurs in t as a type, not as a row
// element of a function type in t.
func holdsTyped(q *TypeParam, t Type, depth int) bool {
	if depth > 16 {
		return false
	}
	switch t := t.(type) {
	case *TypeParam:
		return t == q
	case *Signature:
		for _, p := range t.Params {
			if holdsTyped(q, p.Type, depth+1) {
				return true
			}
		}
		for _, r := range t.Results {
			if holdsTyped(q, r, depth+1) {
				return true
			}
		}
	case *Named:
		for _, a := range t.TArgs {
			if holdsTyped(q, a, depth+1) {
				return true
			}
		}
	case *Slice:
		return holdsTyped(q, t.Elem, depth+1)
	case *Array:
		return holdsTyped(q, t.Elem, depth+1)
	case *Map:
		return holdsTyped(q, t.Key, depth+1) || holdsTyped(q, t.Elem, depth+1)
	case *Pointer:
		return holdsTyped(q, t.Elem, depth+1)
	case *Tuple:
		for _, e := range t.Types {
			if holdsTyped(q, e, depth+1) {
				return true
			}
		}
	case *Struct:
		for _, f := range t.Fields {
			if holdsTyped(q, f.Type, depth+1) {
				return true
			}
		}
	case *RowArg:
		return rowNames(t.Row, q)
	}
	return false
}

// resultsHold reports whether a result of sig holds q, in a row too.
func resultsHold(sig *Signature, q *TypeParam) bool {
	for _, r := range sig.Results {
		if holdsParam(q, r, 0) {
			return true
		}
	}
	return false
}

func holdsParam(q *TypeParam, t Type, depth int) bool {
	if depth > 16 {
		return false
	}
	if occurs(q, t) {
		return true
	}
	switch t := t.(type) {
	case *Signature:
		if rowNames(t.Uses, q) {
			return true
		}
		for _, p := range t.Params {
			if holdsParam(q, p.Type, depth+1) {
				return true
			}
		}
		for _, r := range t.Results {
			if holdsParam(q, r, depth+1) {
				return true
			}
		}
	case *Named:
		for _, a := range t.TArgs {
			if holdsParam(q, a, depth+1) {
				return true
			}
		}
	case *Slice:
		return holdsParam(q, t.Elem, depth+1)
	case *Map:
		return holdsParam(q, t.Elem, depth+1)
	case *Pointer:
		return holdsParam(q, t.Elem, depth+1)
	case *Tuple:
		for _, e := range t.Types {
			if holdsParam(q, e, depth+1) {
				return true
			}
		}
	case *Struct:
		for _, f := range t.Fields {
			if holdsParam(q, f.Type, depth+1) {
				return true
			}
		}
	case *RowArg:
		return rowNames(t.Row, q)
	}
	return false
}
