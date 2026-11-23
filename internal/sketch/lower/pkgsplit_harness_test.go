package lower

import (
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestBodyEditKeepsTheHarness is FT6's guard (test-speed plan §6 row 6),
// in process on the IR test build's split (lowerIRSplit): the test
// harness is main, and the program's root Ergo package is its own Go
// package (rootDir) below it. For generated programs of several packages,
// whose units name their sources under a Root as a real load does:
//
//   - every test and law function is declared in main.go, and none in the
//     root package;
//   - a one-line body edit of the root (a call moves within its line) or
//     of a library function changes the files of that one Go package, and
//     never main.go, so the direct path compiles that package alone (Go
//     reuses main while the package's export data holds).
//
// The behaviour of the split programs is TestSplitLoweringAgrees'. Mutants
// only it kills (testdata/mutants.json): ft-harness-starts-in-root,
// ft-init-pinned-in-test and ft-split-ignores-emitted-names. A failing
// seed prints the edit and the files that changed; replay with
// ERGO_HARNESS_SEED=n.
func TestBodyEditKeepsTheHarness(t *testing.T) {
	seeds := []uint64{1, 2, 3}
	if s := os.Getenv("ERGO_HARNESS_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{n}
	}
	scale := regexp.MustCompile(`func scale\(n int\) int \{ n \* (\d+) \+ 1 \}`)
	for _, seed := range seeds {
		g := genSplitProgram(seed)
		lower := func(p splitProgram) (map[string]string, map[string]bool) {
			t.Helper()
			u := p.check(t)
			u.Root = "/split"
			for i := range u.Imports {
				u.Imports[i].Root = "/split"
			}
			files, _, rep, err := lowerIRSplit(u, true, programModule+"/p/h", nil)
			if err != nil {
				t.Fatalf("seed %d: %v", seed, err)
			}
			if rep.Reason != "" {
				t.Fatalf("seed %d: the test build did not split: %s", seed, rep.Reason)
			}
			out := map[string]string{}
			for _, f := range files {
				out[f.Path] = string(f.Data)
			}
			l := newBuildLowerer(u, &testLowering{gens: map[pkgKey]bool{}, lits: map[pkgKey]bool{}})
			if _, err := l.program(); err != nil {
				t.Fatal(err)
			}
			return out, l.harnessNames()
		}
		before, harness := lower(g)
		rootGo := rootDir + "/" + rootDir + ".go"
		if _, ok := before[rootGo]; !ok {
			t.Fatalf("seed %d: the test build has no root package %s; files %v", seed, rootGo, slices.Sorted(maps.Keys(before)))
		}
		if len(harness) == 0 {
			t.Fatalf("seed %d: the program has no test or law function", seed)
		}
		for name := range harness {
			if !strings.HasPrefix(name, goHelper("ergoTest")) && !strings.HasPrefix(name, goHelper("ergoLaw")) {
				continue
			}
			decl := regexp.MustCompile(`(?m)^(func X?|type )` + regexp.QuoteMeta(name) + `[( ]`)
			if !decl.MatchString(before["main.go"]) || decl.MatchString(before[rootGo]) {
				t.Fatalf("seed %d: %s is not main's alone (main.go %v, %s %v)", seed, name, decl.MatchString(before["main.go"]), rootGo, decl.MatchString(before[rootGo]))
			}
		}
		rootEdit := g
		rootEdit.root = strings.Replace(g.root, "func line() string {\n\t\"\\(Total)", "func line() string {\n\t\"x\\(Total)", 1)
		libEdit := g
		libEdit.libs = slices.Clone(g.libs)
		libEdit.libs[0].text = scale.ReplaceAllString(g.libs[0].text, "func scale(n int) int { n * ${1} + 2 }")
		for _, e := range []struct {
			name string
			p    splitProgram
			dir  string
		}{{"a call of the root's line() moves", rootEdit, rootDir + "/"}, {"the body of gen/base's scale", libEdit, splitDir + "/gen/base/"}} {
			if e.p.root == g.root && slices.Equal(e.p.libs, g.libs) {
				t.Fatalf("seed %d: the edit %q changed no source", seed, e.name)
			}
			after, _ := lower(e.p)
			var changed []string
			for _, path := range slices.Sorted(maps.Keys(after)) {
				if before[path] != after[path] {
					changed = append(changed, path)
				}
			}
			if len(changed) == 0 {
				t.Fatalf("seed %d: after %s no file changed", seed, e.name)
			}
			for _, path := range changed {
				if !strings.HasPrefix(path, e.dir) {
					t.Fatalf("seed %d: after %s %s changed (all changed: %v), want only %s*\nreplay: ERGO_HARNESS_SEED=%d go test -run '^TestBodyEditKeepsTheHarness$' ./internal/sketch/lower", seed, e.name, path, changed, e.dir, seed)
				}
			}
		}
	}
}
