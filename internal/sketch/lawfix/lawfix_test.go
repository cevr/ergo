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
	// helperOf is, by helper that only laws use, the law that must hold it
	// beside itself: the first law, in source order, that uses it.
	helperOf map[string]string
}

// generate draws a package of 2 to 4 files: functions F (subjects), laws
// L that call some of them directly and through helpers h, and tests that
// use some helpers too, each declaration in a drawn file. A law or helper
// may call strings.Count, so its file imports strings.
func generate(r *rand.Rand) genPackage {
	nf := 2 + r.IntN(3)
	names := []string{"a.ergo", "b.ergo", "c.ergo", "laws.ergo"}[:nf]
	decls := map[string][]string{}
	imports := map[string]bool{}
	nfun, nhelp, nlaw := 1+r.IntN(4), r.IntN(3), 1+r.IntN(4)
	funFile := make([]string, nfun)
	for i := range nfun {
		funFile[i] = names[r.IntN(nf)]
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
	g := genPackage{files: map[string]string{}, home: map[string]string{}, helperOf: map[string]string{}}
	used := make([]bool, nhelp)
	type lawInfo struct {
		name, file string
		direct     []int
		helps      []int
	}
	var laws []lawInfo
	for k := range nlaw {
		l := lawInfo{name: fmt.Sprintf("L%d", k), file: names[r.IntN(nf)]}
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
		decls[l.file] = append(decls[l.file], fmt.Sprintf("// %s holds.\nlaw %s(n int) { %s }", l.name, l.name, strings.Join(parts, " && ")))
		laws = append(laws, l)
	}
	// A test uses a helper half the time: that helper then stays put.
	tested := make([]bool, nhelp)
	for j := range nhelp {
		if !used[j] || r.IntN(2) == 0 {
			tested[j] = true
			f := names[r.IntN(nf)]
			decls[f] = append(decls[f], fmt.Sprintf("test T%d { assert h%d(1) == h%d(1) }", j, j, j))
		}
	}
	// Package order: by file name, then by place in the file.
	sort.SliceStable(laws, func(a, b int) bool { return laws[a].file < laws[b].file })
	for _, l := range laws {
		var subj []string
		for _, i := range l.direct {
			subj = append(subj, funFile[i])
		}
		for _, j := range l.helps {
			subj = append(subj, funFile[helpCalls[j]])
		}
		g.home[l.name] = subj[0]
		if slices.Contains(subj, l.file) {
			g.home[l.name] = l.file
		}
		for _, j := range l.helps {
			h := fmt.Sprintf("h%d", j)
			if _, ok := g.helperOf[h]; !ok && !tested[j] {
				g.helperOf[h] = l.name
			}
		}
	}
	for _, n := range names {
		if len(decls[n]) == 0 {
			continue
		}
		var b strings.Builder
		if imports[n] {
			b.WriteString("import strings\n\n")
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
// and each helper only laws use beside the first law that uses it.
// LAWLOC_SEED=n replays one seed. Mutants (testdata of check,
// mutants.json): lawloc-fix-drops-helper (a helper stays behind) and
// lawloc-fix-no-import (a moved law loses its import).
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
		was, now := declsByFile(files), declsByFile(after)
		if len(was) != len(now) {
			fail("declarations %d before, %d after", len(was), len(now))
		}
		for law, home := range g.home {
			if now[law] != home {
				fail("law %s is in %s, its home is %s", law, now[law], home)
			}
		}
		for h, law := range g.helperOf {
			if now[h] != now[law] {
				fail("helper %s is in %s, apart from %s in %s", h, now[h], law, now[law])
			}
		}
	}
}
