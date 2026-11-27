package lawfix

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
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

// generate draws a package of 2 to 5 files: functions F (subjects), laws
// L and tests T that call some of them directly and through helpers h, each
// declaration in a drawn file. A law, test or helper may call strings.Count
// or mention []time.Weekday, so its file imports strings or time.
func generate(r *rand.Rand) genPackage {
	nf := 2 + r.IntN(4)
	names := []string{"a.ergo", "b.ergo", "c.ergo", "laws.ergo", "tests.ergo"}[:nf]
	decls := map[string][]string{}
	imports, timeImports := map[string]bool{}, map[string]bool{}
	nfun, nhelp, nlaw := 1+r.IntN(4), r.IntN(3), 1+r.IntN(4)
	funFile := make([]string, nfun)
	// A subject is never in laws.ergo or tests.ergo: there an exported
	// function only laws and tests use is read as written for them
	// (Package.LawHelpers).
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
	type checkItem struct {
		name, file string
		direct     []int
		helps      []int
		named      int // the subject the name names, or -1
	}
	var laws []checkItem
	for k := range nlaw {
		l := checkItem{name: fmt.Sprintf("L%d", k), file: names[r.IntN(nf)], named: -1}
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
	// Tests use helpers and subjects too: each helper no law uses, plus extra tests.
	var tests []checkItem
	addTest := func(k int, reqHelp int) {
		tc := checkItem{name: fmt.Sprintf("T%d", k), file: names[r.IntN(nf)], named: -1}
		var parts []string
		for range r.IntN(3) {
			i := r.IntN(nfun)
			tc.direct = append(tc.direct, i)
			parts = append(parts, fmt.Sprintf("F%d(1) == F%d(1)", i, i))
		}
		if reqHelp >= 0 {
			tc.helps = append(tc.helps, reqHelp)
			used[reqHelp] = true
			parts = append(parts, fmt.Sprintf("h%d(1) == h%d(1)", reqHelp, reqHelp))
		} else if nhelp > 0 && (len(tc.direct) == 0 || r.IntN(2) == 0) {
			j := r.IntN(nhelp)
			tc.helps = append(tc.helps, j)
			used[j] = true
			parts = append(parts, fmt.Sprintf("h%d(1) == h%d(1)", j, j))
		}
		if len(parts) == 0 {
			i := r.IntN(nfun)
			tc.direct = append(tc.direct, i)
			parts = append(parts, fmt.Sprintf("F%d(1) == F%d(1)", i, i))
		}
		if r.IntN(3) == 0 {
			parts = append(parts, "strings.Count(\"ab\", \"a\") == 1")
			imports[tc.file] = true
		}
		if r.IntN(3) == 0 {
			parts = append(parts, "len([]time.Weekday{}) == 0")
			timeImports[tc.file] = true
		}
		if len(tc.direct) > 0 && r.IntN(2) == 0 {
			tc.named = tc.direct[r.IntN(len(tc.direct))]
			tc.name = fmt.Sprintf("F%dT%d", tc.named, k)
		}
		decls[tc.file] = append(decls[tc.file], fmt.Sprintf("test %s { assert %s }", tc.name, strings.Join(parts, " && ")))
		tests = append(tests, tc)
	}
	testIdx := 0
	for j := range nhelp {
		if !used[j] || r.IntN(2) == 0 {
			addTest(testIdx, j)
			testIdx++
		}
	}
	for range r.IntN(3) {
		addTest(testIdx, -1)
		testIdx++
	}
	assignHomes := func(items []checkItem) {
		for _, item := range items {
			var subj []string
			for _, i := range item.direct {
				subj = append(subj, funFile[i])
			}
			for _, j := range item.helps {
				subj = append(subj, funFile[helpCalls[j]])
			}
			g.home[item.name] = subj[0]
			if item.named >= 0 {
				g.home[item.name] = funFile[item.named]
			}
			if slices.Contains(subj, item.file) {
				g.home[item.name] = item.file
			}
		}
	}
	assignHomes(laws)
	assignHomes(tests)
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
		for _, j := range tc.helps {
			uses = append(uses, use{tc.file, 1, k*16 + j, tc.name})
		}
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
		return g.home[u.name]
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
		for _, empty := range []string{"laws.ergo", "tests.ergo"} {
			if _, ok := out[empty]; ok {
				fail("%s was not deleted", empty)
			}
		}
		// A second run moves nothing: the integrator reruns ergo fix on a
		// merged head.
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

// TestPlanImportSortClashAndUnalias exercises sorted import transfer, unused
// import pruning when source declarations stay, import-alias clash resolution
// avoiding local variable shadowing, unaliasing an alias that is no longer
// needed in the destination, reusing an existing destination alias, and
// preserving formatting/comments on unformatted source files.
func TestPlanImportSortClashAndUnalias(t *testing.T) {
	srcs := map[string]string{
		// Unformatted spacing around = and body to ensure formatting/comments outside moved decls stay intact.
		"alpha.ergo": "// Alpha package.\n\nimport mymath math\nimport time\n\nfunc math() int { 0 }\n\nfunc Alpha(n int) int {   n + int(time.Unix(0, 0).UnixSeconds()) + int(mymath.Abs(0.0))   }\n",
		"beta.ergo":  "import kv/memory\n\nfunc Beta(n int) int {\n\tlet _ = []memory.Store{}\n\tn\n}\n",
		"keep.ergo":  "import mathpkg math\nimport strings\n\n// KeepStays remains in keep.ergo.\nfunc KeepStays(x float64) float64 { mathpkg.Abs(x) }\n\ntest AlphaUsesStrings {\n\tassert Alpha(strings.Count(\"aa\", \"a\")) == 1\n}\n",
		"tests.ergo": "import mathpkg math\nimport ratelimit/memory\nimport smem session/memory\nimport strconv\n\n// BetaClash moves to beta.ergo and aliases ratelimit/memory past memory and local memory2.\ntest BetaClash {\n\tlet memory2 = 1\n\tlet _ = memory2\n\tlet _ = []memory.Limiter{}\n\tassert Beta(strconv.Itoa(1).Len()) == 1 // keep inline comment\n}\n\n// AlphaReusesAndUnaliases moves to alpha.ergo, reuses mymath, and unaliases session/memory.\ntest AlphaReusesAndUnaliases {\n\tlet _ = []smem.Store{}\n\tassert Alpha(int(mathpkg.Abs(-2.0))) == 2\n} // keep trailing comment\n",
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
	if _, ok := out["tests.ergo"]; ok {
		t.Fatalf("tests.ergo was not deleted:\n%s", dump(out))
	}
	_, after, ds := checkSources(t, out)
	if len(ds) > 0 {
		t.Fatalf("fixed package has diagnostics: %v\n%s", ds, dump(out))
	}
	now := declsByFile(after)
	for decl, want := range map[string]string{
		"KeepStays":               "keep.ergo",
		"AlphaUsesStrings":        "alpha.ergo",
		"BetaClash":               "beta.ergo",
		"AlphaReusesAndUnaliases": "alpha.ergo",
	} {
		if now[decl] != want {
			t.Errorf("%s is in %q, want %s\n%s", decl, now[decl], want, dump(out))
		}
	}
	// Source file keep.ergo pruned unused `import strings` while keeping `import mathpkg math`.
	if strings.Contains(out["keep.ergo"], "import strings") {
		t.Errorf("keep.ergo still imports strings:\n%s", out["keep.ergo"])
	}
	// alpha.ergo received sorted imports (session/memory, strings) without duplicating math.
	if wantSub := "import mymath math\nimport session/memory\nimport strings\nimport time\n"; !strings.Contains(out["alpha.ergo"], wantSub) {
		t.Errorf("alpha.ergo imports not sorted/unaliased as expected:\n%s", out["alpha.ergo"])
	}
	if !strings.Contains(out["alpha.ergo"], "// Alpha package.") || !strings.Contains(out["alpha.ergo"], "   n + ") {
		t.Errorf("alpha.ergo lost existing header comment or unformatted spacing:\n%s", out["alpha.ergo"])
	}
	if !strings.Contains(out["alpha.ergo"], "// AlphaReusesAndUnaliases moves to alpha.ergo") || !strings.Contains(out["alpha.ergo"], "// keep trailing comment") {
		t.Errorf("alpha.ergo lost moved test doc/trailing comments:\n%s", out["alpha.ergo"])
	}
	// beta.ergo sorted imports and preserved inline comment.
	if !strings.Contains(out["beta.ergo"], "import kv/memory\nimport memory3 ratelimit/memory\nimport strconv\n") {
		t.Errorf("beta.ergo imports not sorted/clash-aliased as expected:\n%s", out["beta.ergo"])
	}
	if !strings.Contains(out["beta.ergo"], "// keep inline comment") {
		t.Errorf("beta.ergo lost inline comment:\n%s", out["beta.ergo"])
	}
	// Idempotent on second run.
	pkg2, files2, _ := checkSources(t, out)
	if again, err := Plan(pkg2, files2); err != nil || len(again.Files) > 0 || len(again.Deleted) > 0 {
		t.Errorf("second run not idempotent: files=%v deleted=%v err=%v", again.Files, again.Deleted, err)
	}
}

// TestPlanMultiSubjectAndAmbiguityGuard covers multi-subject tests/laws:
// already placed beside one of their subjects stay put, named subjects win
// earliest occurrence, unnamed multi-subject tests go deterministically to the
// first subject, and tests/laws naming nothing in the package stay unplaced.
func TestPlanMultiSubjectAndAmbiguityGuard(t *testing.T) {
	srcs := map[string]string{
		"alpha.ergo": "func Alpha(n int) int { n + 1 }\n",
		"beta.ergo":  "func Beta(n int) int { n + 2 }\n\ntest MultiStaysInBeta {\n\tassert Alpha(1) + Beta(1) == 5\n}\n",
		"tests.ergo": "test BetaBeforeAlpha {\n\tassert Alpha(1) + Beta(1) == 5\n}\n\ntest NeutralMulti {\n\tassert Alpha(1) + Beta(1) == 5\n}\n\ntest Standalone {\n\tassert 1 + 1 == 2\n}\n",
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
	if !slices.Contains(res.Unplaced, "test Standalone") {
		t.Errorf("res.Unplaced = %v, want test Standalone", res.Unplaced)
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
	if _, ok := out["tests.ergo"]; !ok {
		t.Fatalf("tests.ergo was deleted even though unplaced Standalone remained:\n%s", dump(out))
	}
	_, after, ds := checkSources(t, out)
	if len(ds) > 0 {
		t.Fatalf("fixed package has diagnostics: %v\n%s", ds, dump(out))
	}
	now := declsByFile(after)
	for decl, want := range map[string]string{
		"MultiStaysInBeta": "beta.ergo",
		"BetaBeforeAlpha":  "beta.ergo",
		"NeutralMulti":     "alpha.ergo",
		"Standalone":       "tests.ergo",
	} {
		if now[decl] != want {
			t.Errorf("%s is in %q, want %s\n%s", decl, now[decl], want, dump(out))
		}
	}
	// Second run makes zero moves/deletes (MultiStaysInBeta, BetaBeforeAlpha, NeutralMulti stay, Standalone stays unplaced).
	pkg2, files2, _ := checkSources(t, out)
	if again, err := Plan(pkg2, files2); err != nil || len(again.Files) > 0 || len(again.Deleted) > 0 {
		t.Errorf("second run not idempotent: files=%v deleted=%v err=%v", again.Files, again.Deleted, err)
	}
}

// TestRunRelocatesTestsUnexportsAndDeletes runs lawfix.Run on disk: relocates
// tests, laws, and exclusive helpers (function, type with methods, context),
// unexports an exported helper in tests.ergo, deletes empty tests.ergo and
// *_test.ergo files, reports moves/deletes accurately, and is idempotent.
func TestRunRelocatesTestsUnexportsAndDeletes(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("ergo.mod", "module example.com/pkg\n")
	write("alpha.ergo", "type Reader {\n\tRead() int,\n}\n\nfunc Alpha(n int) int { n + 1 }\n")
	write("beta.ergo", "func Beta(n int) int { n * 2 }\n")
	write("tests.ergo", "import strings\n\ntype stubReader { val int } implements Reader\n\nfunc Read(s stubReader) int { s.val }\n\nfunc MakeInput() int { Alpha(strings.Count(\"aa\", \"a\")) }\n\nlet onStub = context { Reader: stubReader{val: 7} }\n\ntest AlphaWithHelper {\n\twith onStub {\n\t\tassert MakeInput() == 3 && Reader.Read() == 7\n\t}\n}\n\nlaw AlphaMonotone(n int) {\n\tAlpha(n) < Alpha(n+1)\n}\n")
	write("beta_test.ergo", "test BetaDoubles {\n\tassert Beta(3) == 6\n}\n")

	rep, err := Run(Options{Paths: []string{dir}, Write: true})
	if err != nil {
		t.Fatal(err)
	}
	text := rep.Text()
	for _, want := range []string{
		"unexported MakeInput -> makeInput",
		"moved test AlphaWithHelper from",
		"moved law AlphaMonotone from",
		"moved test BetaDoubles from",
		"deleted " + filepath.Join(dir, "tests.ergo") + ": no declaration is left in it",
		"deleted " + filepath.Join(dir, "beta_test.ergo") + ": no declaration is left in it",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report missing %q:\n%s", want, text)
		}
	}
	for _, gone := range []string{"tests.ergo", "beta_test.ergo"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s still exists on disk (err=%v)", gone, err)
		}
	}
	// Reload and verify placement + zero diagnostics.
	files, err := readDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, ds := check.CheckOptions("example.com/pkg", files, nil, check.Options{PlaceLaws: true})
	if len(ds) > 0 {
		t.Fatalf("after Run, package has diagnostics: %v", ds)
	}
	now := declsByFile(files)
	for decl, want := range map[string]string{
		"AlphaWithHelper": "alpha.ergo",
		"AlphaMonotone":   "alpha.ergo",
		"makeInput":       "alpha.ergo",
		"onStub":          "alpha.ergo",
		"stubReader":      "alpha.ergo",
		"BetaDoubles":     "beta.ergo",
	} {
		if now[decl] != want {
			t.Errorf("%s is in %q, want %s", decl, now[decl], want)
		}
	}
	// Second run is a no-op.
	rep2, err := Run(Options{Paths: []string{dir}, Write: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Text() != "" {
		t.Errorf("second Run was not idempotent:\n%s", rep2.Text())
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
