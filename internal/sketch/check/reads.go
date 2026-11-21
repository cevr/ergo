package check

import (
	"sort"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Read sets (NS2b; stdlib.md, UI 13). A function's read set of a record
// parameter is the top-level fields its body can read: p.f reads f (and
// p.f.g reads f too); passing p whole to a function of the program reads
// what that function reads of the matching parameter; any other use of p
// (a spread, a match, a dot call, an assignment, a call of a function whose
// body this package cannot see) reads all of it. Function literals in the
// body count as the body, since they see the same p. A parameter that is
// not a record reads all of itself. Recursive functions join to the least
// fixpoint: a set only grows, by at most its record's field count, so the
// work is linear in the bodies, the call edges and the fields.
//
// The lowering reads the fact to compare only the fields a component
// reads when it decides whether a call is written again (uigroup.go). A
// missed read there would show a stale screen, so the set over-reads
// wherever it cannot see.

// ReadSet is what a function's body reads of one parameter: All, or the
// top-level Fields of a record, sorted.
type ReadSet struct {
	All    bool
	Fields []string
}

// readKey is one parameter of one function.
type readKey struct {
	decl *syntax.FuncDecl
	i    int
}

// readNode is a parameter's set while the fixpoint runs.
type readNode struct {
	all    bool
	fields map[string]bool
	// callers are the parameters that pass this one's argument whole.
	callers []readKey
}

// join adds o's set to n's and reports a change.
func (n *readNode) join(all bool, fields map[string]bool) bool {
	if n.all {
		return false
	}
	if all {
		n.all, n.fields = true, nil
		return true
	}
	changed := false
	for f := range fields {
		if !n.fields[f] {
			n.fields[f] = true
			changed = true
		}
	}
	return changed
}

// readRecord is the record a parameter of type t is, or nil. An
// instance of a generic type is nil (read all): asking for its underlying
// type would instantiate its body, n × k instances over n parameters of a
// k-variant union (TestGenericDefaultSwitchLinear), and the lowering
// compares no generic record by fields.
func readRecord(t Type) *Struct {
	n, ok := t.(*Named)
	if !ok || len(n.TArgs) != 0 {
		return nil
	}
	s, _ := n.Underlying().(*Struct)
	return s
}

// readSets records Info.Reads once every body is final.
func (c *checker) readSets() {
	info := c.pkg.Info
	nodes := map[readKey]*readNode{}
	vars := map[*Var]readKey{}
	var decls []*syntax.FuncDecl
	for _, f := range c.funcs {
		if f.Decl == nil || f.Decl.Extern != nil || f.Decl.Body == nil {
			continue
		}
		decls = append(decls, f.Decl)
		for i, v := range declParams(info, f.Decl) {
			k := readKey{f.Decl, i}
			n := &readNode{fields: map[string]bool{}}
			nodes[k] = n
			if v == nil || readRecord(v.Type) == nil {
				n.all = true
				continue
			}
			vars[v] = k
		}
	}
	tracked := func(x syntax.Expr) (readKey, bool) {
		id, ok := unparen(x).(*syntax.Ident)
		if !ok {
			return readKey{}, false
		}
		v, ok := info.Uses[id].(*Var)
		if !ok {
			return readKey{}, false
		}
		k, ok := vars[v]
		return k, ok
	}
	for _, d := range decls {
		seen := map[*syntax.Ident]bool{}
		syntax.Inspect(d.Body, func(n syntax.Node) bool {
			switch n := n.(type) {
			case *syntax.SelectorExpr:
				k, ok := tracked(n.X)
				if !ok || k.decl != d {
					break
				}
				v := info.Uses[unparen(n.X).(*syntax.Ident)].(*Var)
				if f, _ := readRecord(v.Type).field(n.Sel.Name); f != nil {
					nodes[k].join(false, map[string]bool{f.Name: true})
					seen[unparen(n.X).(*syntax.Ident)] = true
				}
			case *syntax.CallExpr:
				call := info.Calls[n]
				if call == nil || call.Kind != CallFunc || call.Recv != nil || call.Func == nil || call.Func.Sig == nil {
					break
				}
				for j, a := range n.Args {
					k, ok := tracked(a)
					if !ok || k.decl != d || j >= len(call.Func.Sig.Params) {
						continue
					}
					seen[unparen(a).(*syntax.Ident)] = true
					callee := call.Func.Decl
					if to, ok := nodes[readKey{callee, j}]; ok && callee != nil {
						to.callers = append(to.callers, k)
						continue
					}
					// Another package's function: its published set, or all.
					set, ok := ReadSet{All: true}, false
					if callee != nil && call.Func.Pkg != nil && call.Func.Pkg.Info != nil {
						var sets []ReadSet
						if sets, ok = call.Func.Pkg.Info.Reads[callee]; ok && j < len(sets) {
							set = sets[j]
						}
					}
					fields := map[string]bool{}
					for _, f := range set.Fields {
						fields[f] = true
					}
					nodes[k].join(set.All, fields)
				}
			}
			return true
		})
		syntax.Inspect(d.Body, func(n syntax.Node) bool {
			if id, ok := n.(*syntax.Ident); ok && !seen[id] {
				if k, ok := tracked(id); ok && k.decl == d {
					nodes[k].join(true, nil)
				}
			}
			return true
		})
	}
	// The fixpoint: a changed set joins every caller that passes it on.
	var work []readKey
	for k := range nodes {
		work = append(work, k)
	}
	for len(work) > 0 {
		k := work[len(work)-1]
		work = work[:len(work)-1]
		n := nodes[k]
		for _, ck := range n.callers {
			if nodes[ck].join(n.all, n.fields) {
				work = append(work, ck)
			}
		}
	}
	for _, d := range decls {
		params := declParams(info, d)
		sets := make([]ReadSet, len(params))
		for i := range params {
			n := nodes[readKey{d, i}]
			if n.all {
				sets[i] = ReadSet{All: true}
				continue
			}
			fields := make([]string, 0, len(n.fields))
			for f := range n.fields {
				fields = append(fields, f)
			}
			sort.Strings(fields)
			sets[i] = ReadSet{Fields: fields}
		}
		info.Reads[d] = sets
	}
}
