package fix_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/fix"
	"ergo.local/prototype/internal/sketch/format"
	"ergo.local/prototype/internal/sketchfront"
)

// TestFixMigratesModel (EXC1 Property 2; audit CSX §4.10) is a seeded
// property of ergo fix. Each seed writes a module of two packages that the
// old method-set rule accepts: ops declares one to three operation types,
// each with one operation, some including an earlier one and some with a
// member law; impl declares types whose functions fit some of them, or
// match only an operation's name (another result type), converts some of
// them to an operation type they fit, and declares its own operation type
// that a conversion uses as one of ops'. No type writes a clause. Then:
// the module checks clean under the old rule; ergo fix writes it; the
// result checks clean under the declared relation, with the same law runs
// (a pair that only a law run used is written too); a second fix writes
// nothing; and format leaves every fixed file as the fix wrote it.
// ERGO_SEED=<n> replays one seed; a failure prints the module.
//
// Mutants it kills that no other test does (ledger: internal/sketch/fix):
// fix writes a clause where only the names match; fix drops a pair that
// only a law run used; fix is not idempotent on an include.
func TestFixMigratesModel(t *testing.T) {
	var seeds []uint64
	if s := os.Getenv("ERGO_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_SEED: %v", err)
		}
		seeds = []uint64{n}
	} else {
		for i := range uint64(40) {
			seeds = append(seeds, i+1)
		}
	}
	for _, seed := range seeds {
		dir := t.TempDir()
		files := fixModule(seed)
		for name, text := range files {
			if !strings.HasSuffix(name, ".ergo") {
				writeFile(t, filepath.Join(dir, name), text)
				continue
			}
			canon, err := format.Source(text)
			if err != nil {
				t.Fatalf("seed %d: %s does not format: %v\n%s", seed, name, err, text)
			}
			writeFile(t, filepath.Join(dir, name), canon)
		}
		module := func() string {
			var b strings.Builder
			for _, name := range []string{"ops/ops.ergo", "impl/impl.ergo"} {
				src, _ := os.ReadFile(filepath.Join(dir, name))
				fmt.Fprintf(&b, "--- %s\n%s", name, src)
			}
			return b.String()
		}
		fail := func(format string, args ...any) {
			t.Fatalf("seed %d (replay: ERGO_SEED=%d go test -run TestFixMigratesModel ./internal/sketch/fix): %s\n%s", seed, seed, fmt.Sprintf(format, args...), module())
		}

		restore := check.RecordLegacy(func(check.LegacyPair) {})
		before, diags := lawRuns(dir)
		restore()
		if len(diags) > 0 {
			fail("the old rule refuses the module: %v", diags)
		}
		rep, err := fix.Run(fix.Options{Paths: []string{dir}, Write: true})
		if err != nil {
			fail("fix: %v", err)
		}
		if len(rep.Unwritten) > 0 {
			fail("fix left pairs unwritten: %v", rep.Unwritten)
		}
		after, diags := lawRuns(dir)
		if len(diags) > 0 {
			fail("the declared relation refuses the fixed module: %v", diags)
		}
		if !slices.Equal(before, after) {
			fail("law runs before %v, after %v", before, after)
		}
		again, err := fix.Run(fix.Options{Paths: []string{dir}})
		if err != nil || len(again.Written) > 0 || len(again.Unwritten) > 0 {
			fail("a second fix is not empty: %v %v %v", err, again.Written, again.Unwritten)
		}
		for _, name := range []string{"ops/ops.ergo", "impl/impl.ergo"} {
			src, _ := os.ReadFile(filepath.Join(dir, name))
			if canon, err := format.Source(string(src)); err != nil || canon != string(src) {
				fail("format changes the fixed %s (%v):\n%s", name, err, canon)
			}
		}
	}
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// lawRuns checks each package of the module in dir and returns its law
// runs, "pkg type law", sorted, with every diagnostic.
func lawRuns(dir string) ([]string, []string) {
	var runs, diags []string
	targets, err := sketchfront.DiscoverPackages(dir)
	if err != nil {
		return nil, []string{err.Error()}
	}
	seen := map[string]bool{}
	for _, tgt := range targets {
		a, err := sketchfront.CheckDir(tgt.Dir)
		if err != nil {
			diags = append(diags, err.Error())
			continue
		}
		for _, d := range a.Diagnostics {
			diags = append(diags, fmt.Sprintf("%s %s %s", d.File, d.Code, d.Message))
		}
		for _, p := range a.Checked() {
			if seen[p.Path] {
				continue
			}
			seen[p.Path] = true
			for _, r := range p.LawRuns {
				for _, b := range r.Bindings {
					for _, prov := range b {
						runs = append(runs, p.Path+" "+prov.Type.Obj.Name+" "+r.Law.Name)
					}
				}
			}
		}
	}
	slices.Sort(runs)
	slices.Sort(diags)
	return slices.Compact(runs), slices.Compact(diags)
}

// fixModule writes the module of one seed: ergo.mod, ops and impl.
func fixModule(seed uint64) map[string]string {
	r := rand.New(rand.NewPCG(seed, 0xf1c5))
	n := 1 + r.IntN(3)
	inc := make([]int, n)
	var ops strings.Builder
	for k := range n {
		inc[k] = -1
		include := ""
		if k > 0 && r.IntN(2) == 0 {
			inc[k] = r.IntN(k)
			include = fmt.Sprintf("..Op%d, ", inc[k])
		}
		law := ""
		if r.IntN(2) == 0 {
			law = fmt.Sprintf(", law L%d(v int) { v == v }", k)
		}
		fmt.Fprintf(&ops, "type Op%d { %sA%d() int%s }\n", k, include, k, law)
	}
	// names[k] are the operations Opk asks for.
	names := make([][]int, n)
	for k := range n {
		for j := k; j >= 0; j = inc[j] {
			names[k] = append(names[k], j)
		}
	}
	var impl strings.Builder
	impl.WriteString("import m/ops\nfunc keep(o ops.Op0) {}\n")
	for i := range 1 + r.IntN(3) {
		name := fmt.Sprintf("T%d", i)
		if r.IntN(2) == 0 {
			fmt.Fprintf(&impl, "type %s { v int }\n", name)
		} else {
			fmt.Fprintf(&impl, "type %s int\n", name)
		}
		has := map[int]bool{}
		for j := range n {
			switch r.IntN(3) {
			case 0: // fits
				has[j] = true
				fmt.Fprintf(&impl, "func A%d(x %s) int { 0 }\n", j, name)
			case 1: // the name only: another result type
				fmt.Fprintf(&impl, "func A%d(x %s) string { \"\" }\n", j, name)
			}
		}
		val := name + "{v: 1}"
		if !strings.Contains(impl.String(), "type "+name+" {") {
			val = name + "(1)"
		}
		for k := range n {
			fits := true
			for _, j := range names[k] {
				fits = fits && has[j]
			}
			if fits && r.IntN(2) == 0 {
				fmt.Fprintf(&impl, "func to%s%d() ops.Op%d { %s }\n", name, k, k, val)
			}
		}
	}
	// An operation type of impl with the operations of one of ops', used
	// as it: the fix writes an include.
	if k := r.IntN(n + 1); k < n {
		var members []string
		for _, j := range names[k] {
			members = append(members, fmt.Sprintf("A%d() int", j))
		}
		fmt.Fprintf(&impl, "type Local { %s }\n", strings.Join(members, ", "))
		fmt.Fprintf(&impl, "func asOps(l Local) ops.Op%d { l }\n", k)
	}
	return map[string]string{
		"ergo.mod":       "module m\n",
		"ops/ops.ergo":   ops.String(),
		"impl/impl.ergo": impl.String(),
	}
}
