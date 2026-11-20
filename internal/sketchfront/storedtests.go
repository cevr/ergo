package sketchfront

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lawcache"
	"ergo.local/prototype/internal/sketch/lower"
)

// Stored test verdicts and route notes (TESTSPEED FT2). They share the law
// verdicts' store (lawcache), so one bounded store holds every verdict of
// both tiers. A test's key is its reach digest (lower.TestDigest), its
// name and the seed: an edit of a declaration it reaches makes a new key,
// and an edit of anything else, a comment or a declaration it does not
// reach, keeps it. Only a pass is stored, as for laws.
//
// A route note is kept for an obligation the evaluator left incomplete
// (its budget ran out): the next `ergo test` of the package whose
// obligation still has that key sends the package to the test binary at
// once, with no evaluation, instead of spending the budget again to learn
// the same thing. A note is never a verdict: it lives under a key of its
// own and says only which tier answers.

// testKey is the store's key of test t of pkg's verdict at seed.
func testKey(pkg *check.Package, t *check.Test, seed uint64, keys lower.ForeignKeys) string {
	return lawcache.Key("test", t.Name, lower.TestDigestWith(pkg, t, keys), strconv.FormatUint(seed, 10))
}

// routeKey is the key of the route note of the obligation whose verdict
// key is key, for an evaluation of steps.
func routeKey(key string, steps int64) string {
	return lawcache.Key("route", key, strconv.FormatInt(steps, 10))
}

// routeNote is a route note's text: never a verdict's encoding.
const routeNote = "route\tincomplete on the evaluator"

// testVerdicts is the store's view of a package's tests at a seed for the
// evaluator (eval.TestVerdicts).
type testVerdicts struct {
	store *lawcache.Store
	pkg   *check.Package
	seed  uint64
	keys  map[*check.Test]string
	// foreign are the program's foreign keys (testKey).
	foreign lower.ForeignKeys
}

func (v *testVerdicts) key(t *check.Test) string {
	k, ok := v.keys[t]
	if !ok {
		k = testKey(v.pkg, t, v.seed, v.foreign)
		v.keys[t] = k
	}
	return k
}

func (v *testVerdicts) Cached(t *check.Test) (eval.Verdict, bool) {
	text, ok := v.store.Get(v.key(t))
	if !ok {
		return eval.Verdict{}, false
	}
	// The format's version is in its tag: a test verdict of the older
	// "test" one says nothing of its reads, so its test runs once.
	parts := strings.Split(text, "\t")
	if len(parts) != 5 || parts[0] != "test2" {
		return eval.Verdict{}, false
	}
	reads, err := strconv.Unquote(parts[4])
	if err != nil {
		return eval.Verdict{}, false
	}
	cases, err1 := strconv.Atoi(parts[2])
	total, err2 := strconv.Atoi(parts[3])
	if err1 != nil || err2 != nil {
		return eval.Verdict{}, false
	}
	switch parts[1] {
	case "evaluated", "proved", "tested":
	default:
		return eval.Verdict{}, false
	}
	out := eval.Verdict{Evidence: parts[1], Cases: cases, TotalCases: total}
	if reads != "" {
		out.Reads = strings.Split(reads, "\n")
	}
	return out, true
}

func (v *testVerdicts) Held(t *check.Test, held eval.Verdict) {
	reads := strconv.Quote(strings.Join(held.Reads, "\n"))
	v.store.Put(v.key(t), "test2\t"+held.Evidence+"\t"+strconv.Itoa(held.Cases)+"\t"+strconv.Itoa(held.TotalCases)+"\t"+reads)
}

// routes are the route-note keys of a package's obligations at a seed and
// budget, by obligation name, as TestLines names them.
func routes(pkg *check.Package, seed uint64, states int, steps int64, keys lower.ForeignKeys) map[string]string {
	out := map[string]string{}
	for _, t := range pkg.Tests {
		out[t.Name] = routeKey(testKey(pkg, t, seed, keys), steps)
	}
	for _, t := range lower.RunTargets(pkg) {
		out[t.Label] = routeKey(targetKey(pkg, t, seed, states, runnerEval, keys), steps)
	}
	return out
}
