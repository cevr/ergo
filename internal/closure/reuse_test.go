package closure

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"ergo.local/prototype/internal/testinputs"
)

// The sweep's reuse of recorded runs (HEAVY-COST-2). A test process run
// with testinputs.RecordVariable records itself: an observed test log, the
// observer's access-time identity, a Go build cache of its package's own,
// and probes of the guarantees the sweep's own runs have. With reuseVariable
// naming that directory, the sweep classifies a package's recorded runs
// instead of running its tests again, when every guarantee holds; otherwise
// it runs them, and its log line names the guarantee that failed. With
// reuseModeVariable=compare it does both and fails a package whose two
// verdicts differ (the reuse's acceptance check).
const (
	reuseVariable     = "ERGO_CLOSURE_REUSE"
	reuseModeVariable = "ERGO_CLOSURE_REUSE_MODE"
	reuseCompare      = "compare"
)

// recordScratch are the variables whose values are a run's own scratch,
// and the record's and the observer's own: they differ between a recorded
// run and a swept one by construction, and the recorded run's places are
// in its record (Writable, Scratch, Cache). ERGO_CACHE is replaced in every
// test process before the tests run (testinputs, withPrivateCache).
var recordScratch = []string{"TMPDIR", "GOTMPDIR", "XDG_CACHE_HOME", "GOCACHE", "PWD", "ERGO_CACHE", testinputs.RecordVariable, testinputs.IdentityVariable}

// reuseRefusal says why the package's recorded runs cannot stand for the
// sweep's runs of it, or "" when they can. pkgDir is the package's
// directory, names its tests (scanTests), env the environment a swept run
// receives (sweepEnvironment, with the sweep's extra).
func reuseRefusal(recs []testinputs.Record, dirs []string, pkg, pkgDir string, names, env []string) string {
	if len(recs) == 0 {
		return "no recorded run"
	}
	if _, ok := unobservable[pkg]; ok {
		return "the package is unobservable"
	}
	covered := map[string]bool{}
	for i, rec := range recs {
		run := fmt.Sprintf("recorded run %s", filepath.Base(dirs[i]))
		switch {
		case rec.Package != pkg || rec.Dir != pkgDir:
			return fmt.Sprintf("%s is of %s at %s, not %s", run, rec.Package, rec.Dir, pkgDir)
		case rec.Exit != 0:
			return fmt.Sprintf("%s exited %d", run, rec.Exit)
		case !rec.TreeReadOnly:
			return fmt.Sprintf("%s saw the checkout writable (%s): the sweep's runs see it read-only", run, rec.TreeProbe)
		case !strings.HasPrefix(rec.UserNamespaces, testinputs.UserNamespacesRefused):
			return fmt.Sprintf("%s could make user namespaces (%s): the sweep's runs cannot", run, rec.UserNamespaces)
		case !ownCache(rec, dirs[i]):
			return fmt.Sprintf("%s built in %s, not a cache of its package's own", run, rec.Cache)
		}
		if diff := envDifference(rec.Env, env); diff != "" {
			return fmt.Sprintf("%s had another environment than a swept run: %s", run, diff)
		}
		data, err := os.ReadFile(filepath.Join(dirs[i], testinputs.RecordLog))
		if err != nil {
			return fmt.Sprintf("%s has no test log: %v", run, err)
		}
		if len(data) == 0 || data[len(data)-1] != '\n' {
			return fmt.Sprintf("%s was not observed: %q", run, data[strings.LastIndexByte(string(data), '\n')+1:])
		}
		if _, err := os.Stat(filepath.Join(dirs[i], testinputs.RecordIdentity)); err != nil {
			return fmt.Sprintf("%s has no access-time identity: %v", run, err)
		}
		selected, why := selectedTests(rec.Args, names)
		if why != "" {
			return fmt.Sprintf("%s: %s", run, why)
		}
		for _, n := range selected {
			covered[n] = true
		}
	}
	var missing []string
	for _, n := range names {
		if !covered[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return fmt.Sprintf("no recorded run ran %d of the package's tests, %s first", len(missing), missing[0])
	}
	return ""
}

// ownCache says whether rec's cache is its package's own under the record
// directory (testinputs: <base>/cache/<package>, beside <base>/runs).
func ownCache(rec testinputs.Record, dir string) bool {
	base := filepath.Dir(filepath.Dir(filepath.Dir(dir))) // <base>/runs/<pkg>/<run>
	return rec.Cache == filepath.Join(base, "cache", strings.ReplaceAll(rec.Package, "/", "_"))
}

// envDifference names the first variable whose value differs between a
// recorded run's environment and a swept run's, outside recordScratch, or
// returns "".
func envDifference(recorded, swept []string) string {
	toMap := func(env []string) map[string]string {
		m := map[string]string{}
		for _, kv := range env {
			name, value, _ := strings.Cut(kv, "=")
			if !slices.Contains(recordScratch, name) {
				m[name] = value
			}
		}
		return m
	}
	a, b := toMap(recorded), toMap(swept)
	names := slices.Sorted(func(yield func(string) bool) {
		for k := range a {
			if !yield(k) {
				return
			}
		}
		for k := range b {
			if _, ok := a[k]; !ok && !yield(k) {
				return
			}
		}
	})
	for _, k := range names {
		va, ina := a[k]
		vb, inb := b[k]
		switch {
		case ina && !inb:
			return fmt.Sprintf("%s is set in the recorded run only", k)
		case inb && !ina:
			return fmt.Sprintf("%s is set in a swept run only", k)
		case va != vb:
			return fmt.Sprintf("%s=%q in the recorded run, %q in a swept run", k, va, vb)
		}
	}
	return ""
}

// selectedTests returns the tests of names that a test binary run with
// args runs, or why the sweep cannot tell: a run of only part of a test
// (a subtest pattern), a short run, or a listing.
func selectedTests(args, names []string) ([]string, string) {
	var run, skip string
	for i := 0; i < len(args); i++ {
		name, value, hasValue := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		takeValue := func() string {
			if hasValue {
				return value
			}
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch name {
		case "test.run":
			run = takeValue()
		case "test.skip":
			skip = takeValue()
		case "test.short":
			if !hasValue || value == "true" {
				return nil, "it ran -short, which skips the tests that build Go"
			}
		case "test.list":
			return nil, "it listed tests instead of running them"
		}
	}
	for _, p := range []string{run, skip} {
		if strings.Contains(p, "/") {
			return nil, fmt.Sprintf("its pattern %q selects subtests, part of a test", p)
		}
	}
	matches := func(pattern string) (func(string) bool, error) {
		if pattern == "" {
			return nil, nil
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, err
		}
		return re.MatchString, nil
	}
	runs, err := matches(run)
	if err != nil {
		return nil, err.Error()
	}
	skips, err := matches(skip)
	if err != nil {
		return nil, err.Error()
	}
	var out []string
	for _, n := range names {
		if (runs == nil || runs(n)) && (skips == nil || !skips(n)) {
			out = append(out, n)
		}
	}
	return out, ""
}

// recordedEntries classifies the recorded runs' logs as runTestsWith
// classifies a swept run's, each with its own access-time identity, its own
// writable places and the package's declared kernel writes, and returns
// their union.
func recordedEntries(recs []testinputs.Record, dirs []string, roots []string, pkg string) ([]entry, error) {
	var entries []entry
	for i, rec := range recs {
		data, err := os.ReadFile(filepath.Join(dirs[i], testinputs.RecordLog))
		if err != nil {
			return nil, err
		}
		id, err := testinputs.ReadIdentity(filepath.Join(dirs[i], testinputs.RecordIdentity))
		if err != nil {
			return nil, fmt.Errorf("recorded run %s: %w", dirs[i], err)
		}
		writable := slices.Clone(rec.Writable)
		for _, k := range declaredKernelWrites[pkg] {
			writable = append(writable, k.point)
		}
		run, err := readLog(data, rec.Dir, roots, rec.Scratch, newAccessIdentity(id, writable))
		if err != nil {
			return nil, fmt.Errorf("recorded run %s: %w", dirs[i], err)
		}
		entries = append(entries, run...)
	}
	slices.SortFunc(entries, func(a, b entry) int { return cmp.Or(strings.Compare(a.path, b.path), strings.Compare(a.op, b.op)) })
	return slices.Compact(entries), nil
}

// reusePackage is the sweep's reuse of the package dir: its entries from
// the recorded runs, or why it runs the tests itself.
func reusePackage(base string, roots []string, dir, pkgDir string) ([]entry, string) {
	recs, dirs, err := testinputs.ReadRecords(base, dir)
	if err != nil {
		return nil, err.Error()
	}
	names, err := scanTests(pkgDir)
	if err != nil {
		return nil, err.Error()
	}
	if why := reuseRefusal(recs, dirs, dir, pkgDir, names, sweepEnvironment()); why != "" {
		return nil, why
	}
	entries, err := recordedEntries(recs, dirs, roots, dir)
	if err != nil {
		return nil, err.Error()
	}
	return entries, ""
}

// sameVerdict compares the closure's verdicts on a package's swept and
// reused entries: the inputs outside its closure and the declared entries
// no input uses. It returns "" when they agree.
func sameVerdict(c closure, dir string, swept, reused []entry) string {
	verdict := func(entries []entry) (outside []string, unused []string) {
		used := map[string]bool{}
		for _, e := range c.check(entries, used) {
			outside = append(outside, e.op+" "+e.path)
		}
		for _, a := range declared[dir] {
			if !used[a.key()] {
				unused = append(unused, a.key())
			}
		}
		return outside, unused
	}
	so, su := verdict(swept)
	ro, ru := verdict(reused)
	if !slices.Equal(so, ro) {
		return fmt.Sprintf("outside the closure: swept %v, reused %v", so, ro)
	}
	if !slices.Equal(su, ru) {
		return fmt.Sprintf("declared and unused: swept %v, reused %v", su, ru)
	}
	return ""
}
