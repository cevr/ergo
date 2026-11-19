package lawfix

import (
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// genPackage is a generated package: its files by name, and for each law
// the file its first subject is in (the reference model of Plan).
type genPackage struct {
	files map[string]string
	// home is, by law, the file the law must end in: its own file when one
	// of its subjects is there, else its first subject's file.
	home map[string]string
	// helperAt is, by helper, the file it must end in: its own file when a
	// law or test that uses it stays or arrives there, else the file of the
	// first one, in package order, that uses it.
	helperAt map[string]string
}

// generate draws a package of 2 to 4 files: functions F (subjects), laws
// L that call some of them directly and through helpers h, and tests that
// use some helpers too, each declaration in a drawn file. A law or helper
// may call strings.Count, so its file imports strings.
func generate(r *rand.Rand) genPackage {
	nf := 2 + r.IntN(3)
	names := []string{"a.ergo", "b.ergo", "c.ergo", "laws.ergo"}[:nf]
	decls := map[string][]string{}
	imports, timeImports := map[string]bool{}, map[string]bool{}
	nfun, nhelp, nlaw := 1+r.IntN(4), r.IntN(3), 1+r.IntN(4)
	funFile := make([]string, nfun)
	// A subject is never in laws.ergo: there an exported function only laws
	// use is read as written for them (Package.LawHelpers).
	for i := range nfun {
		funFile[i] = names[r.IntN(min(nf, 3))]
		decls[funFile[i]] = append(decls[funFile[i]], fmt.Sprintf("func F%d(n int) int { n + %d }", i, i))
	}
	// A helper calls one subject, and maybe strings.
	helpCalls := make([]int, nhelp)
	helpFile := make([]string, nhelp)
	helpStrings := make([]bool, nhelp)
	for j := range nhelp {
		helpCalls[j] = r.IntN(nfun)
		helpFile[j] = names[r.IntN(nf)]
		helpStrings[j] = r.IntN(2) == 0
		body := fmt.Sprintf("F%d(n)", helpCalls[j])
		if helpStrings[j] {
			body += " + strings.Count(\"a\", \"a\")"
			imports[helpFile[j]] = true
		}
		decls[helpFile[j]] = append(decls[helpFile[j]], fmt.Sprintf("// h%d helps.\nfunc h%d(n int) int { %s }", j, j, body))
	}
	g := genPackage{files: map[string]string{}, home: map[string]string{}, helperAt: map[string]string{}}
	used := make([]bool, nhelp)
	type lawInfo struct {
		name, file string
		direct     []int
		helps      []int
		named      int // the subject the name names, or -1
	}
	var laws []lawInfo
	for k := range nlaw {
		l := lawInfo{name: fmt.Sprintf("L%d", k), file: names[r.IntN(nf)], named: -1}
		var parts []string
		for range r.IntN(3) {
			i := r.IntN(nfun)
			l.direct = append(l.direct, i)
			parts = append(parts, fmt.Sprintf("F%d(n) == F%d(n)", i, i))
		}
		if nhelp > 0 && (len(l.direct) == 0 || r.IntN(2) == 0) {
			j := r.IntN(nhelp)
			l.helps = append(l.helps, j)
			used[j] = true
			parts = append(parts, fmt.Sprintf("h%d(n) == h%d(n)", j, j))
		}
		if len(parts) == 0 {
			i := r.IntN(nfun)
			l.direct = append(l.direct, i)
			parts = append(parts, fmt.Sprintf("F%d(n) == F%d(n)", i, i))
		}
		if r.IntN(3) == 0 {
			parts = append(parts, "strings.Count(\"ab\", \"a\") == 1")
			imports[l.file] = true
		}
		// An import named only in a type: the package name there has no
		// Uses entry.
		if r.IntN(3) == 0 {
			parts = append(parts, "len([]time.Weekday{}) == 0")
			timeImports[l.file] = true
		}
		// Half the laws are named for a subject they call, as SortPermutes
		// is for Sort: that one is the law's home.
		if len(l.direct) > 0 && r.IntN(2) == 0 {
			l.named = l.direct[r.IntN(len(l.direct))]
			l.name = fmt.Sprintf("F%dL%d", l.named, k)
		}
		decls[l.file] = append(decls[l.file], fmt.Sprintf("// %s holds.\nlaw %s(n int) { %s }", l.name, l.name, strings.Join(parts, " && ")))
		laws = append(laws, l)
	}
	// Tests use helpers too: each helper no law uses, and others at times.
	type testInfo struct {
		name, file string
		help       int
	}
	var tests []testInfo
	for j := range nhelp {
		if !used[j] || r.IntN(2) == 0 {
			tc := testInfo{fmt.Sprintf("T%d", j), names[r.IntN(nf)], j}
			decls[tc.file] = append(decls[tc.file], fmt.Sprintf("test %s { assert h%d(1) == h%d(1) }", tc.name, j, j))
			tests = append(tests, tc)
		}
	}
	for _, l := range laws {
		var subj []string
		for _, i := range l.direct {
			subj = append(subj, funFile[i])
		}
		for _, j := range l.helps {
			subj = append(subj, funFile[helpCalls[j]])
		}
		g.home[l.name] = subj[0]
		if l.named >= 0 {
			g.home[l.name] = funFile[l.named]
		}
		if slices.Contains(subj, l.file) {
			g.home[l.name] = l.file
		}
	}
	// A helper's first user, in package order (by file name, then laws
	// before tests in a file, each in the order drawn), keeps it beside
	// itself.
	type use struct {
		file string
		kind int // 0 a law, 1 a test
		at   int
		name string
	}
	var uses []use
	for k, l := range laws {
		for _, j := range l.helps {
			uses = append(uses, use{l.file, 0, k*16 + j, l.name})
		}
	}
	for k, tc := range tests {
		uses = append(uses, use{tc.file, 1, k*16 + tc.help, tc.name})
	}
	sort.SliceStable(uses, func(a, b int) bool {
		if uses[a].file != uses[b].file {
			return uses[a].file < uses[b].file
		}
		if uses[a].kind != uses[b].kind {
			return uses[a].kind < uses[b].kind
		}
		return uses[a].at/16 < uses[b].at/16
	})
	end := func(u use) string {
		if u.kind == 0 {
			return g.home[u.name]
		}
		return u.file
	}
	for _, u := range uses {
		h := fmt.Sprintf("h%d", u.at%16)
		if _, ok := g.helperAt[h]; !ok {
			g.helperAt[h] = end(u)
		}
		if end(u) == helpFile[u.at%16] {
			g.helperAt[h] = end(u)
		}
	}
	for _, n := range names {
		if len(decls[n]) == 0 {
			continue
		}
		var b strings.Builder
		if imports[n] {
			b.WriteString("import strings\n")
		}
		if timeImports[n] {
			b.WriteString("import time\n")
		}
		if imports[n] || timeImports[n] {
			b.WriteString("\n")
		}
		b.WriteString(strings.Join(decls[n], "\n\n"))
		b.WriteString("\n")
		g.files[n] = b.String()
	}
	return g
}

func checkSources(t *testing.T, srcs map[string]string) (*check.Package, []*syntax.File, []check.Diagnostic) {
	t.Helper()
	var names []string
	for n := range srcs {
		names = append(names, n)
	}
	sort.Strings(names)
	var files []*syntax.File
	for _, n := range names {
		f, err := syntax.Parse(srcs[n])
		if err != nil {
			t.Fatalf("%s does not parse: %v\n%s", n, err, srcs[n])
		}
		f.Name = n
		files = append(files, f)
	}
	pkg, ds := check.Check("main", files, nil)
	return pkg, files, ds
}

func dump(srcs map[string]string) string {
	var names []string
	for n := range srcs {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "--- %s\n%s", n, srcs[n])
	}
	return b.String()
}

// declsByFile lists the declaration names of each file.
func declsByFile(files []*syntax.File) map[string]string {
	out := map[string]string{}
	for _, f := range files {
		for _, d := range f.Decls {
			for _, n := range declNames(d) {
				out[n] = f.Name
			}
		}
	}
	return out
}

// TestPlanPlacesEveryLaw: for a drawn package, Plan's edit checks with no
// diagnostic, keeps every declaration, puts each law in its reference
// home (its own file when that holds a subject, else its first subject's)
// and each helper where a law or test that uses it stays or arrives, else
// beside the first one; a second Plan of the result edits nothing.
// LAWLOC_SEED=n replays one seed. Mutants (mutants.json):
// lawloc-fix-drops-helper (a helper stays behind), lawloc-fix-no-import
// (a moved law loses its import) and lawloc-fix-helper-churns (a helper
// follows its first user only, so a second run moves it again).
func TestPlanPlacesEveryLaw(t *testing.T) {
	seeds := 200
	first := uint64(1)
	if s := os.Getenv("LAWLOC_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, seeds = n, 1
	}
	for seed := first; seed < first+uint64(seeds); seed++ {
		g := generate(rand.New(rand.NewPCG(seed, 7)))
		replay := fmt.Sprintf("LAWLOC_SEED=%d go test ./internal/sketch/lawfix -run TestPlanPlacesEveryLaw", seed)
		pkg, files, ds := checkSources(t, g.files)
		for _, d := range ds {
			if d.Code != check.CodeLawPlace {
				t.Fatalf("seed %d: the drawn package does not check: %s\n%s\nreplay: %s", seed, d, dump(g.files), replay)
			}
		}
		res, err := Plan(pkg, files)
		if err != nil {
			t.Fatalf("seed %d: %v\n%s\nreplay: %s", seed, err, dump(g.files), replay)
		}
		out := map[string]string{}
		for n, s := range g.files {
			out[n] = s
		}
		for n, s := range res.Files {
			out[n] = s
		}
		for _, n := range res.Deleted {
			delete(out, n)
		}
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d: %s\nbefore:\n%s\nafter:\n%s\nreplay: %s", seed, fmt.Sprintf(format, args...), dump(g.files), dump(out), replay)
		}
		_, after, ds := checkSources(t, out)
		if len(ds) > 0 {
			fail("the fixed package has diagnostics: %v", ds)
		}
		// A file of laws left holding a helper or a test is renamed.
		ren := map[string]string{}
		for _, rn := range res.Renamed {
			from, to, _ := strings.Cut(rn, " -> ")
			ren[from] = to
		}
		named := func(f string) string {
			if to, ok := ren[f]; ok {
				return to
			}
			return f
		}
		was, now := declsByFile(files), declsByFile(after)
		if len(was) != len(now) {
			fail("declarations %d before, %d after", len(was), len(now))
		}
		for law, home := range g.home {
			if now[law] != named(home) {
				fail("law %s is in %s, its home is %s", law, now[law], home)
			}
		}
		for h, at := range g.helperAt {
			if now[h] != named(at) {
				fail("helper %s is in %s, want %s", h, now[h], at)
			}
		}
		// A second run moves nothing: the integrator reruns ergo fix on a
		// merged head (LAWLOC r2).
		pkg2, files2, _ := checkSources(t, out)
		again, err := Plan(pkg2, files2)
		if err != nil {
			fail("the second run: %v", err)
		}
		if len(again.Files) > 0 || len(again.Deleted) > 0 {
			fail("a second run edits %d files and deletes %v", len(again.Files), again.Deleted)
		}
	}
}

// TestPlanKeepsProvidersAndNamesFiles is the smallest repro of two hand
// steps the first LAWLOC migration needed (examples/shards, idle-timer):
// a provider the member laws of an operation type run against, written in
// a file of laws, goes beside the operation type, and a file of laws left
// holding other declarations is named by its concept. Mutants (check and
// lawfix mutants.json): lawloc-fix-drops-provider (nothing names the
// provider, so it stayed behind) and lawloc-fix-keeps-laws-file (the file
// keeps its name).
func TestPlanKeepsProvidersAndNamesFiles(t *testing.T) {
	srcs := map[string]string{
		"targets.ergo":    "type Targets {\n\tSize(n int) int,\n\n\tlaw SizeIsPositive(n int) {\n\t\tSize(n) >= 0\n\t},\n}\n\nfunc F(n int) int { n + 1 }\n",
		"laws.ergo":       "type refusing {} implements Targets\n\nfunc Size(_ refusing, n int) int { 0 }\n\nlaw FKeepsOrder(a int, b int) {\n\t(a <= b) == (F(a) <= F(b))\n}\n",
		"clock_laws.ergo": "type Clock { N int }\n\nlaw ClockKeepsN(n int) {\n\tClock{N: n}.N == n\n}\n",
	}
	pkg, files, ds := checkSources(t, srcs)
	for _, d := range ds {
		if d.Code != check.CodeLawPlace {
			t.Fatalf("the package does not check: %v\n%s", ds, dump(srcs))
		}
	}
	res, err := Plan(pkg, files)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for n, s := range srcs {
		out[n] = s
	}
	for _, n := range res.Deleted {
		delete(out, n)
	}
	for n, s := range res.Files {
		out[n] = s
	}
	_, after, ds := checkSources(t, out)
	if len(ds) > 0 {
		t.Fatalf("the fixed package has diagnostics: %v\n%s", ds, dump(out))
	}
	now := declsByFile(after)
	for name, want := range map[string]string{"refusing": "targets.ergo", "FKeepsOrder": "targets.ergo", "Clock": "clock.ergo", "ClockKeepsN": "clock.ergo"} {
		if now[name] != want {
			t.Errorf("%s is in %q, want %s\n%s", name, now[name], want, dump(out))
		}
	}
}
