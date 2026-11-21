package main

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/stage"
	"ergo.local/prototype/internal/testinputs/genprogram"
)

// affectedSeeds is TestAffectedIsWhatChanges's number of seeds;
// ERGO_AFFECTED_SEED=N replays one.
const affectedSeeds = 1

// affectedEdit is one edit script: it rewrites the head's files, given
// the program and a random source. empty marks an edit that changes no
// reach (comment, reformat, move): its affected set is empty.
type affectedEdit struct {
	name  string
	empty bool
	apply func(r *rand.Rand, files map[string]string, p genprogram.Program)
}

// pinned are the tests the property adds to each package at BASE: one
// whose refutation prints Total's value, so a body edit changes a
// verdict, and one that reaches Text0, which the signature and deletion
// edits touch.
const pinned = "\ntest Pinned {\n\tassert Total(3) == -1\n}\n\ntest Texts {\n\tassert Text0(Make0(2)) != \"\"\n}\n"

// pkgFile is the main source of package i.
func pkgFile(i int) string { return genprogram.Name(i) + "/" + genprogram.Name(i) + ".ergo" }

// funcSpan is the start and end (past "\n}\n") of the declaration of
// func name in src, with its doc comment.
func funcSpan(src, name string) (int, int) {
	start := strings.Index(src, "func "+name+"(")
	if doc := strings.LastIndex(src[:start], "\n\n"); doc >= 0 {
		start = doc + 2
	}
	end := start + strings.Index(src[start:], "\n}\n") + 3
	return start, end
}

// imported is a package that another package of p imports.
func imported(r *rand.Rand, p genprogram.Program) int {
	var out []int
	for i := range len(p.Packages) - 1 {
		for name, src := range p.Files {
			if strings.HasSuffix(name, ".ergo") && strings.Contains(src, "import "+p.Module+"/"+genprogram.Name(i)+"\n") && !strings.HasPrefix(name, "cmd/") {
				out = append(out, i)
				break
			}
		}
	}
	return out[r.IntN(len(out))]
}

var affectedEdits = []affectedEdit{
	{name: "body", apply: func(r *rand.Rand, f map[string]string, p genprogram.Program) {
		i := r.IntN(len(p.Packages) - 1)
		f[pkgFile(i)] = strings.Replace(f[pkgFile(i)], "\t\treturn Rect{W: it.Weight, H: ", "\t\treturn Rect{W: it.Weight + 1, H: ", 1)
	}},
	{name: "signature", apply: func(r *rand.Rand, f map[string]string, p genprogram.Program) {
		i := r.IntN(len(p.Packages) - 1)
		src := f[pkgFile(i)]
		s, e := funcSpan(src, "Text0")
		f[pkgFile(i)] = src[:s] + strings.ReplaceAll(src[s:e], "items", "rows") + src[e:]
	}},
	{name: "new test", apply: func(r *rand.Rand, f map[string]string, p genprogram.Program) {
		name := genprogram.Name(r.IntN(len(p.Packages)-1)) + "/tests.ergo"
		f[name] += "\ntest Added {\n\tassert Sum0(Make0(2)) >= 0\n}\n"
	}},
	{name: "deleted function", apply: func(r *rand.Rand, f map[string]string, p genprogram.Program) {
		i := r.IntN(len(p.Packages) - 1)
		src := f[pkgFile(i)]
		s, e := funcSpan(src, "Text0")
		src = src[:s] + src[e:]
		if !strings.Contains(src, "strings.") {
			src = strings.Replace(src, "import strings\n", "", 1)
		}
		f[pkgFile(i)] = src
		tests := genprogram.Name(i) + "/tests.ergo"
		f[tests] = strings.Replace(f[tests], "\ntest Texts {\n\tassert Text0(Make0(2)) != \"\"\n}\n", "", 1)
	}},
	{name: "comment", empty: true, apply: func(r *rand.Rand, f map[string]string, p genprogram.Program) {
		i := r.IntN(len(p.Packages) - 1)
		f[pkgFile(i)] = strings.Replace(f[pkgFile(i)], "func Area(", "// A note the edit adds.\nfunc Area(", 1)
	}},
	{name: "reformat", empty: true, apply: func(r *rand.Rand, f map[string]string, p genprogram.Program) {
		i := r.IntN(len(p.Packages) - 1)
		src := strings.Replace(f[pkgFile(i)], "3 * c.R * c.R", "3*c.R*c.R", 1)
		f[pkgFile(i)] = strings.ReplaceAll(src, "\tlet mut total = 0\n", "\tlet mut total = 0\n\n")
	}},
	{name: "moved declaration", empty: true, apply: func(r *rand.Rand, f map[string]string, p genprogram.Program) {
		i := r.IntN(len(p.Packages) - 1)
		src := f[pkgFile(i)]
		s, e := funcSpan(src, "Area")
		f[pkgFile(i)] = src[:s] + src[e:] + "\n" + src[s:e]
	}},
	{name: "imported package", apply: func(r *rand.Rand, f map[string]string, p genprogram.Program) {
		i := imported(r, p)
		f[pkgFile(i)] = strings.Replace(f[pkgFile(i)], "\t\treturn Rect{W: it.Weight, H: ", "\t\treturn Rect{W: it.Weight + 2, H: ", 1)
	}},
	{name: "ergo.mod", apply: func(r *rand.Rand, f map[string]string, p genprogram.Program) {
		f["ergo.mod"] += "\n"
	}},
}

func affectedGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"--no-optional-locks", "-C", dir, "-c", "user.name=Ergo fixture", "-c", "user.email=fixture@invalid", "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// affectedRepo is a Git repository whose one commit holds base, its
// working tree head.
func affectedRepo(t *testing.T, base, head map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, base)
	affectedGit(t, dir, "init", "-q")
	affectedGit(t, dir, "add", ".")
	affectedGit(t, dir, "commit", "-qm", "base")
	for name := range base {
		if _, ok := head[name]; !ok {
			if err := os.Remove(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeFiles(t, dir, head)
	return dir
}

// checkedObligations are the digests of each package's obligations in the
// module on disk at dir, and each one's evaluator result (status, reason
// and cases), by "package: label".
func checkedObligations(t *testing.T, dir string, p genprogram.Program) (digests, results map[string]string) {
	t.Helper()
	platform, err := platformOf("", false, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	digests, results = map[string]string{}, map[string]string{}
	for _, path := range p.Packages {
		a, err := sketchfront.CheckDirForPlatform(filepath.Join(dir, strings.TrimPrefix(path, p.Module+"/")), platform)
		if err != nil || len(a.Diagnostics) > 0 || a.Root().Package == nil {
			t.Fatalf("%s does not check: %v %v", path, err, a.Diagnostics)
		}
		for label, o := range obligationsOf(a.Root().Package) {
			digests[path+": "+label] = o.digest
		}
		rep, err := a.Evaluate(eval.DefaultBudget())
		if err != nil {
			t.Fatal(err)
		}
		for _, res := range rep.Results {
			label := res.Name
			if !strings.HasPrefix(label, "law ") {
				label = "test " + label
			}
			results[path+": "+label] = fmt.Sprintf("%v %s %d", res.Status, res.Reason, res.Cases)
		}
	}
	return digests, results
}

// TestAffectedIsWhatChanges is the property of `ergo test --affected`
// (CI-SELECT CS-AFFECTED): over generated modules and edit scripts, (a)
// every obligation whose evaluator result differs between BASE and the
// working tree is affected, (b) the affected set is the obligations whose
// reach digest differs or that are new (every obligation, when ergo.mod
// changed), with BASE read from a disk copy here and from Git objects by
// the command, (c) a comment, a reformat or a moved declaration affects
// nothing, and (d) --list prints the set and runs nothing. Mutants
// affected-ignores-imported-package, affected-uses-file-bytes,
// affected-base-is-worktree and affected-list-runs.
func TestAffectedIsWhatChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on PATH")
	}
	seeds := []uint64{}
	for s := range uint64(affectedSeeds) {
		seeds = append(seeds, s+1)
	}
	if v := os.Getenv("ERGO_AFFECTED_SEED"); v != "" {
		s, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{s}
	}
	for _, seed := range seeds {
		p := genprogram.GenerateProgram(seed, 2*genprogram.Width/2+2, genprogram.Options{Groups: 2})
		base := map[string]string{}
		for name, src := range p.Files {
			base[name] = src
			if strings.HasSuffix(name, "/tests.ergo") {
				base[name] += pinned
			}
		}
		baseDir := t.TempDir()
		writeFiles(t, baseDir, base)
		baseDigests, baseResults := checkedObligations(t, baseDir, p)
		for _, edit := range affectedEdits {
			r := rand.New(rand.NewPCG(seed, uint64(len(edit.name))))
			head := map[string]string{}
			for name, src := range base {
				head[name] = src
			}
			edit.apply(r, head, p)
			dir := affectedRepo(t, base, head)
			headDigests, headResults := checkedObligations(t, dir, p)
			fail := func(format string, args ...any) {
				t.Helper()
				var diff strings.Builder
				for name := range head {
					if head[name] != base[name] {
						fmt.Fprintf(&diff, "--- %s at BASE\n%s\n--- %s in the working tree\n%s\n", name, base[name], name, head[name])
					}
				}
				t.Fatalf("seed %d, edit %s: %s\n%sreplay: ERGO_AFFECTED_SEED=%d go test -count=1 -run '^TestAffectedIsWhatChanges$' ./cmd/ergo", seed, edit.name, fmt.Sprintf(format, args...), diff.String(), seed)
			}
			opts := fileOptions{command: "test", target: report.Target, file: filepath.Join(dir, "...")}
			opts.affected = affectedOptions{on: true, base: "HEAD", list: true}
			stage.Reset()
			var out bytes.Buffer
			ran := 0
			err := affectedTestCommand(opts, &out, func(fileOptions, loaded) error { ran++; return nil })
			if err != nil {
				fail("--affected: %v", err)
			}
			var got []string
			for line := range strings.Lines(out.String()) {
				got = append(got, strings.TrimSuffix(line, "\n"))
			}
			sort.Strings(got)
			// (b): the digests that differ, or every obligation.
			var want []string
			for key, d := range headDigests {
				if then, had := baseDigests[key]; !had || then != d || edit.name == "ergo.mod" {
					want = append(want, key)
				}
			}
			sort.Strings(want)
			if !slices.Equal(got, want) {
				fail("affected set\n got %q\nwant %q", got, want)
			}
			// (c)
			if edit.empty && len(got) != 0 {
				fail("an edit of no reach affects %q", got)
			}
			// (a)
			for key, res := range headResults {
				if then, had := baseResults[key]; (!had || then != res) && !slices.Contains(got, key) {
					fail("%s is not affected, but its result changed: %q at BASE, %q now", key, baseResults[key], res)
				}
			}
			// (d)
			counts := stage.Snapshot()
			if ran != 0 || counts["go_commands"] != 0 || counts["go_compiles"] != 0 || counts["links"] != 0 || counts["processes"] != 0 {
				fail("--list ran %d packages; stage report %v", ran, counts)
			}
			if counts["obligations_affected"] != int64(len(want)) {
				fail("obligations_affected %d, want %d", counts["obligations_affected"], len(want))
			}
		}
	}
}

// TestAffectedWhyNamesTheDeclaration is the reason line's text and place
// (a diagnostic-text row): an edit of a function's body names the
// function and its line, relative to the module root, for the test that
// reaches it through another package; an unchanged test says none, and a
// new one says new.
func TestAffectedWhyNamesTheDeclaration(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on PATH")
	}
	base := map[string]string{
		"ergo.mod":       "module m\n",
		"lib/tally.ergo": "// Package lib counts.\n\n// Tally is n doubled.\nfunc Tally(n int) int {\n\tn + n\n}\n",
		"app/app.ergo":   "import m/lib\n\nfunc twice(n int) int {\n\tlib.Tally(n)\n}\n\nfunc one() int {\n\t1\n}\n",
		"app/tests.ergo": "test Doubles {\n\tassert twice(2) == 4\n}\n\ntest One {\n\tassert one() == 1\n}\n",
	}
	head := map[string]string{}
	for name, src := range base {
		head[name] = src
	}
	head["lib/tally.ergo"] = strings.Replace(base["lib/tally.ergo"], "\tn + n\n", "\t2 * n\n", 1)
	head["app/tests.ergo"] += "\ntest Fresh {\n\tassert one() > 0\n}\n"
	dir := affectedRepo(t, base, head)
	opts := fileOptions{command: "test", target: report.Target, file: filepath.Join(dir, "app")}
	opts.affected = affectedOptions{on: true, base: "HEAD", list: true, why: true}
	var out bytes.Buffer
	if err := affectedTestCommand(opts, &out, func(fileOptions, loaded) error { return nil }); err != nil {
		t.Fatal(err)
	}
	want := "m/app: test Doubles: reaches func lib.Tally (lib/tally.ergo:4), changed\n" +
		"m/app: test Fresh: new\n" +
		"m/app: test One: none: nothing it reaches changed\n"
	if out.String() != want {
		t.Errorf("--why:\n%s\nwant:\n%s", out.String(), want)
	}
}
