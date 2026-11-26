package lint

import "ergo.local/prototype/internal/sketch/syntax"

// repeatedTraversal is the speed hint for walking one collection twice
// (catalog PF6, "one pass, not a second walk"): two for-in loops,
// statements of one block, over the same variable or field of one, with
// no write to that variable in the first loop or between them. The second
// loop is reported. A loop over a call's result, or over a variable a
// statement between reassigns, is another collection. Out of scope, by
// the 2026-11-18 read: a first loop that can leave early (a search, or a
// check of every element before any work), and a second loop that reads
// or writes a variable the first writes (it needs all of the first's
// work, as a width found by the first loop and used by the second), or
// writes through a pointer the first writes through (output in order);
// and a statement between them that calls or reads what the first loop
// wrote (an action or a decision the second loop must follow).
var repeatedTraversal = &Rule{
	Name:    "repeated-traversal",
	Code:    "L145",
	Doc:     "Two loops in a row over the same unchanged collection walk it twice; one loop can do the work of both.",
	Instead: "do the second loop's work in the first, in one pass",
	Default: Hint,
	Check:   checkRepeatedTraversal,
}

func checkRepeatedTraversal(p *Pass) {
	for _, b := range p.fileIndex().blocks {
		for i, s := range b.List {
			first, ok := s.(*syntax.RangeStmt)
			if !ok {
				continue
			}
			v := rootVar(p.Info, first.X)
			if v == nil {
				continue
			}
			hasSecond := false
			for _, t := range b.List[i+1:] {
				if second, isRange := t.(*syntax.RangeStmt); isRange && rootVar(p.Info, second.X) == v {
					hasSecond = true
					break
				}
			}
			if !hasSecond {
				continue
			}
			if v == nil || writes(p.Info, first.Body, v) || leavesEarly(first.Body) {
				continue
			}
			shape := shapeOf(p.Info, first.X)
			after := written(p.Info, first.Body)
			for v := range pointers(p.Info, first.Body) {
				after[v] = true
			}
			for _, t := range b.List[i+1:] {
				if second, isRange := t.(*syntax.RangeStmt); isRange && rootVar(p.Info, second.X) == v && shapeOf(p.Info, second.X) == shape && !usesAny(p.Info, second.Body, after) {
					p.Report(second.For, second.Body.Lbrace, "this loop walks the collection the loop before it walked, unchanged")
					break
				}
				if writes(p.Info, t, v) || calls(t) || usesAny(p.Info, t, after) {
					break
				}
			}
		}
	}
}
