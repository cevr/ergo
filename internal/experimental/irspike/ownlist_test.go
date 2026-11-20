package irspike

import "testing"

// TestIROwnedListMatchSource is the seeded property over frames that open
// a scope only for their owned values and keep them in the list form
// (OWNED-LOCAL, GenerateOwnList): for each seed the IR path and the
// source path print the same opens, uses, hand-offs and releases (each
// with the field written in place), in the same order, a defect at a
// release included, and taker and ol0..ol2 (every seed writes at least
// three) have IR bodies. A failing seed prints the Ergo source, both
// outcomes and its replay command.
//
// It builds Go because the question is what the built program releases,
// with which writes, in which order, on which exit, which only a run
// shows.
//
// Mutants: ownlist-owning-at-dropped (listLet writes no v1.OwningAt, so a
// written local is released with its old fields), ownlist-param-dropped
// (openScope registers no taken parameter, so taker's r is never
// released), ownlist-plain-off (irFrame keeps a plain list frame's source:
// the olN have no IR body).
func TestIROwnedListMatchSource(t *testing.T) {
	f7Property(t, "TestIROwnedListMatchSource", GenerateOwnList, append([]string{"ol0", "ol1", "ol2"}, ownListFuncs...)...)
}
