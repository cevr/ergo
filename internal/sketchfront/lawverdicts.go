package sketchfront

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lawcache"
	"ergo.local/prototype/internal/sketch/lower"
)

// Law verdicts (lawcache; v1 audit 3, F7: "an unchanged law runs up to
// three times"). Check-time evaluation (Analysis.Evaluate), ergo test's
// build (TestVerdicts) and the ledger's check runs (UpdateLedger,
// FreshStdLedger) share one store: a law run keyed by its label, its
// TargetDigest, the seed and the budget of states runs once, in whichever
// comes first, and the others print its verdict "(cached)".

// defaultStates is the test build's --states default and the evaluator's.
const defaultStates = 1_000_000

// targetKey is the store's key of the run t of pkg's test build at seed
// and states.
func targetKey(pkg *check.Package, t lower.RunTarget, seed uint64, states int) string {
	if states == 0 {
		states = defaultStates
	}
	return lawcache.Key("law run", t.Label, lower.TargetDigest(pkg, t.Law, t.Binding), strconv.FormatUint(seed, 10), strconv.Itoa(states))
}

// rootPackage is the analysed package whose tests and laws run.
func (a Analysis) rootPackage() *check.Package {
	pkgs := a.Checked()
	if len(pkgs) == 0 || len(a.Diagnostics) > 0 {
		return nil
	}
	return pkgs[len(pkgs)-1]
}

// EvalVerdicts is the store's view for the evaluation of a's laws at seed
// and states (eval.Budget.Verdicts); nil when store is nil.
func (a Analysis) EvalVerdicts(store *lawcache.Store, seed uint64, states int) eval.Verdicts {
	pkg := a.rootPackage()
	if store == nil || pkg == nil {
		return nil
	}
	v := &evalVerdicts{store: store, pkg: pkg, seed: seed, states: states, targets: lower.RunTargets(pkg)}
	v.keys = make([]string, len(v.targets))
	return v
}

type evalVerdicts struct {
	store   *lawcache.Store
	pkg     *check.Package
	seed    uint64
	states  int
	targets []lower.RunTarget
	// keys memoizes each target's key, "" until it is asked for.
	keys []string
}

// key is the key of the run of law with binding, when it is a target.
func (v *evalVerdicts) key(law *check.Law, binding []check.LawProvider) (string, bool) {
	for i, t := range v.targets {
		if t.Law != law || len(t.Binding) != len(binding) || (len(binding) > 0 && &t.Binding[0] != &binding[0]) {
			continue
		}
		if v.keys[i] == "" {
			v.keys[i] = targetKey(v.pkg, t, v.seed, v.states)
		}
		return v.keys[i], true
	}
	return "", false
}

func (v *evalVerdicts) Cached(law *check.Law, binding []check.LawProvider) (string, int, bool) {
	k, ok := v.key(law, binding)
	if !ok {
		return "", 0, false
	}
	text, ok := v.store.Get(k)
	if !ok {
		return "", 0, false
	}
	verdict, ok := lawcache.Decode(text)
	return verdict.Evidence, verdict.Cases, ok
}

func (v *evalVerdicts) Held(law *check.Law, binding []check.LawProvider, evidence string, cases int) {
	if k, ok := v.key(law, binding); ok {
		v.store.Put(k, lawcache.Evaluated(evidence, cases).Encode())
	}
}

// TestVerdicts is the store's view for one run of a's test build at seed
// and states: the runs whose verdict the store holds (CachedFile, the
// file the build's --cached reads), and the verdicts of the runs that
// held, which Record stores from the build's output.
type TestVerdicts struct {
	store   *lawcache.Store
	targets []lower.RunTarget
	keys    []string
}

// TestVerdicts is nil when store is nil.
func (a Analysis) TestVerdicts(store *lawcache.Store, seed uint64, states int) *TestVerdicts {
	pkg := a.rootPackage()
	if store == nil || pkg == nil {
		return nil
	}
	tv := &TestVerdicts{store: store}
	for _, t := range lower.RunTargets(pkg) {
		tv.targets = append(tv.targets, t)
		tv.keys = append(tv.keys, targetKey(pkg, t, seed, states))
	}
	return tv
}

// CachedFile is the text of the --cached file: a "LABEL\tTEXT" line for
// each run the store holds a verdict of.
func (tv *TestVerdicts) CachedFile() string {
	if tv == nil {
		return ""
	}
	var b strings.Builder
	for i, t := range tv.targets {
		text, ok := tv.store.Get(tv.keys[i])
		if !ok {
			continue
		}
		if v, ok := lawcache.Decode(text); ok {
			b.WriteString(t.Label + "\t" + v.Text + "\n")
		}
	}
	return b.String()
}

// Record stores the verdict of each run whose ok line out holds: a run
// that printed no ok line (it failed, was skipped or did not run) or a
// cached one stores nothing.
func (tv *TestVerdicts) Record(out string) {
	if tv == nil {
		return
	}
	at := map[string]int{}
	for i, t := range tv.targets {
		at[t.Label] = i
	}
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(line, "ok ")
		if !ok {
			continue
		}
		label, _, _ := strings.Cut(rest, " (")
		i, ok := at[label]
		if !ok {
			continue
		}
		if v, ok := lawcache.FromLine(label, line); ok {
			tv.store.Put(tv.keys[i], v.Encode())
		}
	}
}
