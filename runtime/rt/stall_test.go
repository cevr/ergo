package rt

import "testing"

// TestStalledSeesThroughEmptiedDomains: a domain made below d through a
// domain that has since emptied is still below d (stall.go: "every domain
// a member of d's tree made since"). a's member made b's, b's member made
// c's, and b emptied. While c's member can run, a is not stalled; once it
// parks, a is. Mutant (stall-below-parent-only): below follows only the
// nearest domain above, so c is lost with b and a reads as stalled while
// c's member runs.
func TestStalledSeesThroughEmptiedDomains(t *testing.T) {
	a := &Virtual{domain: true, live: 1, parked: 1}
	b := &Virtual{domain: true, live: 1}
	c := &Virtual{domain: true, live: 1}
	domainMade(a, nil)
	domainMade(b, a)
	domainMade(c, b)
	defer domainEmpty(a)
	defer domainEmpty(c)
	b.live = 0
	domainEmpty(b)
	if Stalled(a) {
		t.Fatal("a reads as stalled while a member of c, made below it through b, can run")
	}
	c.parked = 1
	if !Stalled(a) {
		t.Fatal("a does not read as stalled once every member below it parked")
	}
}
