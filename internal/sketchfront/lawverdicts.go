package sketchfront

import (
	"encoding/json"
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
// digest (lower.RunTarget.Digest: TargetDigest, or ContextDigest for a run
// under a context), the seed and the budget of states runs once, in
// whichever comes first, and the others print its verdict "(cached)". A
// machine's reachability law is keyed per runner: check-time evaluation
// reports its search (eval.MachineSearch) and the test build its own
// text, so each runner reuses only its own verdict of it.

// defaultStates is the test build's --states default and the evaluator's.
const defaultStates = 1_000_000

// The runners of a machine's law, which key its verdict apart.
const (
	runnerEval   = "eval"
	runnerNative = "native"
)

// targetKey is the store's key of the run t of pkg's test build at seed
// and states, run by runner (runnerEval or runnerNative).
func targetKey(pkg *check.Package, t lower.RunTarget, seed uint64, states int, runner string, keys lower.ForeignKeys) string {
	if states == 0 {
		states = defaultStates
	}
	parts := []string{"law run", t.Label, t.DigestWith(pkg, keys), strconv.FormatUint(seed, 10), strconv.Itoa(states)}
	if t.Machine {
		parts = append(parts, "machine", runner)
	}
	return lawcache.Key(parts...)
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
// and states (eval.Budget.Verdicts); nil when store is nil. native is the
// toolchain key part (TestVerdicts) of the Go that a test binary ergo
// test would build now is linked by, the pinned release's, or "": a law
// verdict a test binary linked by that toolchain wrote is read too, since
// a native run under it is exactly what the key names (FT2 r3: no double
// work). A verdict of another toolchain is never read.
func (a Analysis) EvalVerdicts(store *lawcache.Store, seed uint64, states int, native string) eval.Verdicts {
	pkg := a.rootPackage()
	if store == nil || pkg == nil {
		return nil
	}
	v := &evalVerdicts{store: store, pkg: pkg, seed: seed, states: states, native: native, foreign: foreignKeys(a, nil)}
	// The store keys no check-time run under a context: the evaluator
	// runs one every time (eval/lawcontext.go), and ergo test keys its own.
	for _, t := range lower.RunTargets(pkg) {
		if t.Context == nil {
			v.targets = append(v.targets, t)
		}
	}
	v.keys = make([]string, len(v.targets))
	return v
}

type evalVerdicts struct {
	store  *lawcache.Store
	pkg    *check.Package
	seed   uint64
	states int
	native string // the toolchain whose test binary's verdicts are read
	// foreign are the program's foreign keys (targetKey).
	foreign lower.ForeignKeys
	targets []lower.RunTarget
	// keys memoizes each target's key, "" until it is asked for.
	keys []string
}

// target is the target of the run of law with binding, and its key.
func (v *evalVerdicts) target(law *check.Law, binding []check.LawProvider) (lower.RunTarget, string, bool) {
	for i, t := range v.targets {
		if t.Law != law || len(t.Binding) != len(binding) || (len(binding) > 0 && &t.Binding[0] != &binding[0]) {
			continue
		}
		if v.keys[i] == "" {
			v.keys[i] = targetKey(v.pkg, t, v.seed, v.states, runnerEval, v.foreign)
		}
		return t, v.keys[i], true
	}
	return lower.RunTarget{}, "", false
}

func (v *evalVerdicts) Cached(law *check.Law, binding []check.LawProvider) (eval.Verdict, bool) {
	t, k, ok := v.target(law, binding)
	if !ok {
		return eval.Verdict{}, false
	}
	text, ok := v.store.Get(k)
	if !ok {
		// A search's verdict the binary wrote is not check time's form.
		if t.Machine || v.native == "" {
			return eval.Verdict{}, false
		}
		if text, ok = v.store.Get(lawcache.Key(k, "toolchain", v.native)); !ok {
			return eval.Verdict{}, false
		}
	}
	stored, ok := lawcache.Decode(text)
	if !ok {
		return eval.Verdict{}, false
	}
	if t.Machine {
		// A search's verdict is check time's own: its reason and search.
		var out eval.Verdict
		if json.Unmarshal([]byte(stored.Text), &out) != nil || out.Evidence != stored.Evidence {
			return eval.Verdict{}, false
		}
		out.Reads, out.ReadsUnknown = stored.Reads, !stored.ReadsKnown
		return out, true
	}
	return eval.Verdict{Evidence: stored.Evidence, Cases: stored.Cases, TotalCases: stored.Cases, Reads: stored.Reads, ReadsUnknown: !stored.ReadsKnown}, true
}

func (v *evalVerdicts) Held(law *check.Law, binding []check.LawProvider, held eval.Verdict) {
	t, k, ok := v.target(law, binding)
	if !ok {
		return
	}
	reads := held.Reads
	if t.Machine {
		data, err := json.Marshal(held)
		if err == nil {
			v.store.Put(k, lawcache.Verdict{Evidence: held.Evidence, Cases: held.Cases, Text: string(data), Reads: reads, ReadsKnown: true}.Encode())
		}
		return
	}
	out := lawcache.Evaluated(held.Evidence, held.Cases)
	out.Reads, out.ReadsKnown = reads, true
	v.store.Put(k, out.Encode())
}

// TestVerdicts is the store's view for one run of a's test build at seed
// and states: the runs whose verdict the store holds (CachedFile, the
// file the build's --cached reads), and the verdicts of the runs that
// held, which Record stores from the build's output.
//
// A verdict the test binary wrote also depends on the Go toolchain that
// linked it, so it is stored under its key with toolchain (own): another
// toolchain, and the evaluator, which shares only toolchain-free keys,
// never read it. The build still reads a verdict under the shared key,
// which only the evaluator writes and no toolchain is an input of (FT2).
type TestVerdicts struct {
	store   *lawcache.Store
	targets []lower.RunTarget
	keys    []string // shared: the evaluator's
	own     []string // this toolchain's
}

// TestVerdicts is nil when store is nil. toolchain is the identity of
// the Go toolchain that links the build ("" keys by the shared key alone).
func (a Analysis) TestVerdicts(store *lawcache.Store, seed uint64, states int, toolchain string) *TestVerdicts {
	pkg := a.rootPackage()
	if store == nil || pkg == nil {
		return nil
	}
	tv := &TestVerdicts{store: store}
	foreign := foreignKeys(a, nil)
	for _, t := range lower.RunTargets(pkg) {
		tv.targets = append(tv.targets, t)
		k := targetKey(pkg, t, seed, states, runnerNative, foreign)
		tv.keys = append(tv.keys, k)
		if toolchain != "" {
			k = lawcache.Key(k, "toolchain", toolchain)
		}
		tv.own = append(tv.own, k)
	}
	return tv
}

// CachedFile is the text of the --cached file: a "LABEL\tTEXT" line for
// each run the store holds a verdict of, TEXT a Go quoted string.
func (tv *TestVerdicts) CachedFile() string {
	if tv == nil {
		return ""
	}
	var b strings.Builder
	for i, t := range tv.targets {
		text, ok := tv.store.Get(tv.own[i])
		if !ok {
			if text, ok = tv.store.Get(tv.keys[i]); !ok {
				continue
			}
		}
		if v, ok := lawcache.Decode(text); ok {
			b.WriteString(t.Label + "\t" + strconv.Quote(v.Text) + "\n")
		}
	}
	return b.String()
}

// Record stores the verdict of each run whose ok line out holds, with the
// lines that continue it: a run that printed no ok line (it failed, was
// skipped or did not run) or a cached one stores nothing.
func (tv *TestVerdicts) Record(out string) {
	if tv == nil {
		return
	}
	at := map[string]int{}
	for i, t := range tv.targets {
		at[t.Label] = i
	}
	lines := strings.Split(out, "\n")
	for n := 0; n < len(lines); n++ {
		rest, ok := strings.CutPrefix(lines[n], "ok ")
		if !ok {
			continue
		}
		label, _, _ := strings.Cut(rest, " (")
		i, ok := at[label]
		if !ok {
			continue
		}
		if v, read, ok := lawcache.FromLines(label, lines[n:]); ok {
			tv.store.Put(tv.own[i], v.Encode())
			n += read - 1
		}
	}
}
