package irspike

import "testing"

// TestIROwnedSitesMatchSource is the seeded property over owned Ref sites
// (FLIP F7 FORK-OWNLIT, GenerateOwnLit): a Ref's Update and sync.Modify
// given a literal that takes the held value over call the owned form of
// their runtime body, RefUpdateOwned or RefModifyOwned, with the copy
// function of the held type and the value walk's fresh flag. Each program
// prints the same under both lowerings, and put, take, alias and grow have
// IR bodies. Mutants f7o-copy-none (the copy function returns its operand)
// and f7o-fresh-always (the flag is always true) fail it: a later write in
// place reaches a value Get or an aliasing Modify gave back. A failing seed
// prints the Ergo source, both outcomes and its replay command.
//
// It builds Go because the copy and the flag act only inside the runtime's
// cell when the compiled program runs: no in-process check sees a write
// in place reach a value given back earlier.
func TestIROwnedSitesMatchSource(t *testing.T) {
	f7Property(t, "TestIROwnedSitesMatchSource", GenerateOwnLit, "put", "take", "alias", "grow")
}
