package lower

import (
	"sort"

	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Call-site ids (types.md, Call sites 6; MAP-INT rule 7). The CallSite a
// call passes for a final parameter it leaves out carries Id, a number the
// compiler gives the call's position: the program's positions where a call
// starts, of its sources and of the std files it reaches, in order of path,
// line and column, from 1. So two calls at one position (a call whose
// callee is a call, f(x)(y)) share their Id as they share their File, Line
// and Column, two positions never share one, and the numbers are the same
// in every lowering of the program, whatever order it asks in (the flat
// unions' second lowerer, the IR build). A CallSite the program writes
// keeps the Id it writes, 0 by convention: no compiler-passed site has 0.
//
// Prior art: Compose's compiler keys a group by an integer it derives from
// the call's source position (a hash of the file and offset), so the
// runtime's slot table compares ints; React keys by the hook's call order
// within a component instead, which needs no compiler.

// siteKey is a call's position: its file's path, line and column.
type siteKey struct {
	path         string
	line, column int
}

// siteID is the Id of the CallSite the call x passes.
func (l *lowerer) siteID(x *syntax.CallExpr) int {
	if l.siteIDs == nil {
		l.siteIDs = l.numberSites()
	}
	path, line, column := l.locate(x, x.Pos())
	id, ok := l.siteIDs[siteKey{path, line, column}]
	if !ok {
		ice(x, "a call site the numbering did not reach")
	}
	return id
}

// numberSites numbers every position where a call starts, in the
// program's sources and the std files it reaches.
func (l *lowerer) numberSites() map[siteKey]int {
	seen := map[siteKey]bool{}
	var keys []siteKey
	add := func(k siteKey) {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for _, s := range l.srcs {
		syntax.Inspect(s.file, func(n syntax.Node) bool {
			if x, ok := n.(*syntax.CallExpr); ok {
				path, line, column := l.locate(x, x.Pos())
				add(siteKey{path, line, column})
			}
			return true
		})
	}
	// The std files are walked here, once and only when a call passes a
	// site: stdFileOf finds one node's file lazily.
	for _, p := range l.libraries() {
		if !p.Std() {
			continue
		}
		for _, f := range p.Files() {
			path := "std/" + p.Path + "/" + f.Name
			if p.Path == stdlib.PreludePath {
				path = "std/" + f.Name
			}
			syntax.Inspect(f, func(n syntax.Node) bool {
				if x, ok := n.(*syntax.CallExpr); ok {
					line, column := f.Position(x.Pos())
					add(siteKey{path, line, column})
				}
				return true
			})
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.path != b.path {
			return a.path < b.path
		}
		if a.line != b.line {
			return a.line < b.line
		}
		return a.column < b.column
	})
	ids := make(map[siteKey]int, len(keys))
	for i, k := range keys {
		ids[k] = i + 1
	}
	return ids
}
