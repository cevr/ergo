package lower

import (
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdURLLawsProbe restates std url's law (STD-SURVEY SM2) as an Ergo test
// over the package. url's other guarantees are Go's answers, which
// TestStdURLMatchesGo holds; EncodeQuery of url's own pairs is outside
// that property, so its round trip is a law.
const stdURLLawsProbe = `import url

test UrlQueryRoundTrips(names []string, values []string) {
	let mut pairs = []url.Pair{}
	for i, n in names {
		pairs = pairs.Append(url.Pair{Name: n, Value: if i < len(values) { values[i] } else { "" }})
	}
	assert url.ParseQuery(url.EncodeQuery(pairs)) == pairs
}
`

// stdURLLawStatements are the url laws' statements, as the ledger writes
// them.
var stdURLLawStatements = map[string]string{
	"url QueryRoundTrips": `QueryRoundTrips(names []string, values []string) { let mut pairs = []Pair{} for i, n in names { pairs = pairs.Append(Pair{Name: n, Value: if i < len(values) { values[i] } else { "" }}) } ParseQuery(EncodeQuery(pairs)) == pairs }`,
}

// TestStdURLLawsRun runs the url probe's tests with --seed 1;
// lawledger.Run holds that the probe restates exactly the ledger's url
// laws, each with the row's statement, and that each passed.
//
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs.
func TestStdURLLawsRun(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stdurl", stdURLLawsProbe, stdURLLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}
