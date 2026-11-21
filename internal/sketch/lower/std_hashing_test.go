package lower

import (
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdHashingLawsProbe restates the prelude's Hashing laws (CONSTRAINTS C5;
// types.md, Maps) as Ergo property tests over the prelude's own provider,
// Structural, at int, string and float64: the member laws through its
// functions. The laws that read a hash (EqualKeysHashEqual,
// Structural*KeysHashEqual) are #162 rows: a test build cannot lower hash
// yet (calls.go, "an unknown call kind" for check.CallHash). A
// where the law states (b K where Equal(a, b)) is the test's implication;
// the test also ties b to a, and for float64 tries 0.0 and -0.0, which are
// == and not identical.
const stdHashingLawsProbe = `let si = Structural[int]
let ss = Structural[string]
let sf = Structural[float64]

func negZero() float64 {
	let z = 0.0
	-z
}

test PreludeHashingEqualIsReflexive(a int, s string, f float64) {
	assert si.Equal(a, a) && ss.Equal(s, s) && (f != f || sf.Equal(f, f))
}

test PreludeHashingEqualIsSymmetric(a int, b int, s string, t string) {
	assert si.Equal(a, b) == si.Equal(b, a) && si.Equal(a, a) == si.Equal(a, a) && ss.Equal(s, t) == ss.Equal(t, s) && sf.Equal(0.0, negZero()) == sf.Equal(negZero(), 0.0)
}

test PreludeHashingEqualIsTransitive(a int, b int, c int) {
	assert (!(si.Equal(a, b) && si.Equal(b, c)) || si.Equal(a, c)) && si.Equal(a, a) && (!(sf.Equal(0.0, negZero()) && sf.Equal(negZero(), 0.0)) || sf.Equal(0.0, 0.0))
}

test PreludeHashingNormalKeyIsEqual(a int, s string) {
	assert (!si.NormalForm() || si.Equal(a, si.NormalKey(a))) && (!ss.NormalForm() || ss.Equal(s, ss.NormalKey(s)))
}

test PreludeHashingEqualKeysNormalEqual(a int, b int) {
	assert (!si.NormalForm() || !si.Equal(a, b) || si.NormalKey(a) == si.NormalKey(b)) && si.NormalKey(a) == si.NormalKey(a) && (!sf.NormalForm() || sf.NormalKey(0.0) == sf.NormalKey(negZero()))
}
`

// stdHashingLawStatements is the statement of each law the probe restates,
// by ledger key; lawledger.Run holds it equal to the ledger.
var stdHashingLawStatements = map[string]string{
	"prelude Hashing.EqualIsReflexive":     `EqualIsReflexive(a K) { Equal(a, a) }`,
	"prelude Hashing.EqualIsSymmetric":     `EqualIsSymmetric(a K, b K) { Equal(a, b) == Equal(b, a) }`,
	"prelude Hashing.EqualIsTransitive":    `EqualIsTransitive(a K, b K where Equal(a, b), c K where Equal(b, c)) { Equal(a, c) }`,
	"prelude Hashing.NormalKeyIsEqual":     `NormalKeyIsEqual(a K) { !NormalForm() || Equal(a, NormalKey(a)) }`,
	"prelude Hashing.EqualKeysNormalEqual": `EqualKeysNormalEqual(a K, b K where Equal(a, b)) { !NormalForm() || NormalKey(a) == NormalKey(b) }`,
}

// TestStdHashingLawsRun runs the Hashing probe's tests with --seed 1;
// lawledger.Run holds that the probe restates exactly the ledger's laws
// whose runner is this test, each with the row's statement, and that each
// passed. It builds Go because Structural's functions run as the lowered
// Map runs them.
func TestStdHashingLawsRun(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stdhashing", stdHashingLawsProbe, stdHashingLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}
