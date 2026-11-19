package testplan

import "ergo.local/prototype/internal/sketch/check"

// The text a refuted case gives a generated value, one rule for both
// runners: the Go build's literal (lower/literal.go) and the evaluator's
// argument (eval/arguments.go). TestCasePlansAgreeWithGo compares them.

// ByConstructor reports whether the text of a value of n is the call of
// its constructor rather than its record: n is built by its constructor
// (check/tests.go rule 3), and the constructor takes no argument, which
// replays the value, or a field of n holds a value with no text, such as
// an operation type's. Else the record shows the value, as `time.Time{sec:
// 1, nsec: 0, mono: 0, hasMono: false}`, which only n's package may write:
// the seed replays the case.
func ByConstructor(n *check.Named) bool {
	ctor := Constructor(n)
	if ctor == nil {
		return false
	}
	if len(ctor.Sig.Params) == 0 {
		return true
	}
	seen := map[*check.Named]bool{}
	var text func(t check.Type) bool
	text = func(t check.Type) bool {
		switch u := t.(type) {
		case *check.Basic:
			return true
		case *check.Slice:
			return text(u.Elem)
		case *check.Map:
			return text(u.Key) && text(u.Elem)
		case *check.Tuple:
			for _, e := range u.Types {
				if !text(e) {
					return false
				}
			}
			return true
		case *check.Named:
			// A value built by its own constructor has a text, its record
			// or its call.
			if seen[u] || (u != n && Constructor(u) != nil) {
				return true
			}
			seen[u] = true
			switch under := u.Underlying().(type) {
			case *check.Struct:
				for _, f := range under.Fields {
					if !text(f.Type) {
						return false
					}
				}
				return true
			case *check.Union:
				for _, v := range under.Variants {
					if !text(v) {
						return false
					}
				}
				return true
			case *check.Basic, *check.Slice, *check.Map, *check.Tuple:
				return text(under)
			}
		}
		return false
	}
	return !text(n)
}
