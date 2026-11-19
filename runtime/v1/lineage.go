package v1

import "sync"

// Lineage is the one-extender token of one Append site (OWN1;
// lower/handover.go): the array and the length of the slice the site made
// last. A value is a slice that every holder reads only up to its length;
// the site appends in place only when its receiver is exactly the slice it
// made last, whose spare capacity it gave nobody, so the slot it writes is
// past the length of every slice of that array that any binding holds:
//
//   - the site made every slice of the array: it allocated the array (a
//     clipped append makes a new one), and nothing else writes it past a
//     length, since the lowering never counts a Lineage site's result as
//     fresh, so no let mut root or owned form takes it uncopied;
//   - each slot past the token's length is written at most once: after an
//     in-place append the token is one longer, and after a copy it names
//     the new array, so no later receiver of the old array matches it.
//
// So a value that two holders extend (an actor's state that a reader
// kept, a journal replay that steps a snapshot again, a sub-slice x[:k])
// is copied, as the clipped Append copies it, and a value one holder
// extends again and again (an actor's log, a journal's signals) is
// extended in place, in amortized constant time, as hand Go's append is.
//
// The token keeps the array alive (one per site) and compares its first
// element's address as an interface value, so two instances of a generic
// site (two element types) never match each other. The mutex makes the
// compare and the write one step when tasks share the site.
//
// Prior art: Erlang's binary append reuses a binary's spare room when the
// appended-to binary is the last one made from it (the "writable binary"
// of BEAM's binary_append); Go's design 74299 (runtime.freegc and an
// alias analysis for appends), whose conservative analysis Ergo replaces
// with its value rule and this check.
type Lineage struct {
	mu   sync.Mutex
	base any // the address of the first element of the last slice made, or nil
	n    int // its length
}

// match reports whether xs is the slice the site made last and has spare
// capacity; the caller holds l.mu.
func lineageMatch[T any](l *Lineage, xs []T) bool {
	return len(xs) < cap(xs) && l.base != nil && l.n == len(xs) && l.base == any(&xs[:1][0])
}

// mark makes out the site's last slice; the caller holds l.mu.
func lineageMark[T any](l *Lineage, out []T) {
	l.base, l.n = any(&out[:1][0]), len(out)
}

// LineageAppend is Append at a site with the token l: x appended in place
// when xs is the slice the site made last (Lineage), else to a copy, as
// the clipped Append makes.
func LineageAppend[T any](l *Lineage, xs []T, x T) []T {
	l.mu.Lock()
	var out []T
	if lineageMatch(l, xs) {
		out = append(xs, x)
	} else {
		out = append(xs[:len(xs):len(xs)], x)
	}
	lineageMark(l, out)
	l.mu.Unlock()
	return out
}

// LineageAppendCheck is LineageAppend's checking form, which a test build
// calls (lower/handover.go): it always appends to a copy, and where the
// run build would append in place it first clears xs's spare capacity,
// which the proof says no binding's length covers. A binding the proof
// missed, one that extended the array, reads zero values, so the
// program's own tests see it.
func LineageAppendCheck[T any](l *Lineage, xs []T, x T) []T {
	l.mu.Lock()
	if lineageMatch(l, xs) {
		clear(xs[len(xs):cap(xs)])
	}
	out := append(xs[:len(xs):len(xs)], x)
	lineageMark(l, out)
	l.mu.Unlock()
	return out
}
