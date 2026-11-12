package eval

import "strconv"

// The run of a rewrite law (types.md, Laws 14), which the test build makes
// (lower/rewritelaw.go) and check and inspect report. A run takes
// RewriteCases drawn cases, each from a prefix of 0 to RewritePrefix-1
// operations, and RewriteInstanceCases more at each site instance. Its
// cases are drawn sequences, an unbounded value, so it is tested, never
// proved, and the build applies proved rewrite laws only (owner q115).
const (
	RewriteCases         = 400
	RewritePrefix        = 32
	RewriteInstanceCases = 40
)

// RewriteVerdict is what check-time evaluation and ergo inspect say of a
// rewrite law: its label, its run's bounds, and the build's decision. The
// deepest prefix a run reached is the test build's (ergo test's ok line,
// the receipt's deepest).
func RewriteVerdict() string {
	return "tested, not proved: ergo test runs a rewrite law over " + strconv.Itoa(RewriteCases) +
		" cases, each from a prefix of 0 to " + strconv.Itoa(RewritePrefix-1) + " operations, plus " +
		strconv.Itoa(RewriteInstanceCases) + " at each site instance, on fresh providers; the build applies proved rewrite laws only, so it keeps the reference lowering"
}
