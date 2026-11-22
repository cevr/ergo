package main

import (
	"bytes"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/fingerprint"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/stage"
	"ergo.local/prototype/internal/testinputs/genprogram"
)

// treeSeeds is TestCheckTreeAnswersByTokens's number of seeds;
// ERGO_TREE_SEED=N replays one.
const treeSeeds = 2

// treeEdit is one edit script of the property, applied to the head's
// files; dep is the package the sub-folder request's package imports.
type treeEdit struct {
	name  string
	apply func(r *rand.Rand, f map[string]string, p genprogram.Program, dep int)
}

var treeEdits = []treeEdit{
	{name: "none", apply: func(*rand.Rand, map[string]string, genprogram.Program, int) {}},
	{name: "layout", apply: func(r *rand.Rand, f map[string]string, p genprogram.Program, dep int) {
		// Lines and spaces only: every site below the edit moves.
		i := r.IntN(len(p.Packages) - 1)
		src := strings.Replace(f[pkgFile(i)], "3 * c.R * c.R", "3*c.R*c.R", 1)
		src = strings.Replace(src, "\nimport strings\n", "\nimport strings\n\n\n", 1)
		f[pkgFile(i)] = strings.ReplaceAll(src, "\tlet mut total = 0\n", "\tlet mut total = 0\n\n")
		tests := genprogram.Name(i) + "/tests.ergo"
		f[tests] = "\n\n" + f[tests]
	}},
	{name: "layout of the request's package", apply: func(r *rand.Rand, f map[string]string, p genprogram.Program, dep int) {
		// Every site of the sub-folder request's package moves, the
		// site its reasons name too (an even seed's).
		i := genprogram.Width
		f[pkgFile(i)] = strings.Replace(f[pkgFile(i)], "\nimport strings\n", "\nimport strings\n\n", 1)
	}},
	{name: "comment", apply: func(r *rand.Rand, f map[string]string, p genprogram.Program, dep int) {
		i := r.IntN(len(p.Packages) - 1)
		f[pkgFile(i)] = strings.Replace(f[pkgFile(i)], "// Area is the area of s.", "// Area is the area of s, as the edit says.", 1)
	}},
	{name: "body", apply: func(r *rand.Rand, f map[string]string, p genprogram.Program, dep int) {
		i := r.IntN(len(p.Packages) - 1)
		f[pkgFile(i)] = strings.Replace(f[pkgFile(i)], "\t\treturn Rect{W: it.Weight, H: ", "\t\treturn Rect{W: it.Weight + 1, H: ", 1)
	}},
	{name: "signature", apply: func(r *rand.Rand, f map[string]string, p genprogram.Program, dep int) {
		i := r.IntN(len(p.Packages) - 1)
		src := f[pkgFile(i)]
		s, e := funcSpan(src, "Text0")
		f[pkgFile(i)] = src[:s] + strings.ReplaceAll(src[s:e], "items", "rows") + src[e:]
	}},
	{name: "new file", apply: func(r *rand.Rand, f map[string]string, p genprogram.Program, dep int) {
		f[genprogram.Name(r.IntN(len(p.Packages)-1))+"/more.ergo"] = "func More() int { 1 }\n"
	}},
	{name: "imported package", apply: func(r *rand.Rand, f map[string]string, p genprogram.Program, dep int) {
		// Outside the sub-folder request, which reaches it.
		f[pkgFile(dep)] = strings.Replace(f[pkgFile(dep)], "\t\treturn Rect{W: it.Weight, H: ", "\t\treturn Rect{W: it.Weight + 2, H: ", 1)
	}},
	{name: "unread import", apply: func(r *rand.Rand, f map[string]string, p genprogram.Program, dep int) {
		// The body of the package the last one imports and no test of it
		// enters (TestCheckTreeAnswersByTokens's unread.ergo): the last
		// package is kept by its verdicts' reads (WS0c).
		u := treeReached(f, "unread.ergo", 0)
		f[pkgFile(u)] = strings.Replace(f[pkgFile(u)], "\t\treturn Rect{W: it.Weight, H: ", "\t\treturn Rect{W: it.Weight + 3, H: ", 1)
	}},
	{name: "interface of the unread import", apply: func(r *rand.Rand, f map[string]string, p genprogram.Program, dep int) {
		// quiet, which holds no obligation, imports it: only the cut
		// key's interfaces evaluate it again (ws0b-key-omits-interface).
		u := treeReached(f, "unread.ergo", 0)
		f[pkgFile(u)] += "\n// Extra widens the interface.\nfunc Extra() int {\n\t0\n}\n"
	}},
	{name: "body read by a call", apply: func(r *rand.Rand, f map[string]string, p genprogram.Program, dep int) {
		// The last package's ReadsByACall now fails: its verdict read
		// the package only through the call (ws0c-plan-unrecorded).
		u := treeReached(f, "reach.ergo", 0)
		f[pkgFile(u)] = strings.Replace(f[pkgFile(u)], "func Probe() int {\n\tlen(Make0(0))\n", "func Probe() int {\n\tlen(Make0(1))\n", 1)
	}},
	{name: "let read by a test", apply: func(r *rand.Rand, f map[string]string, p genprogram.Program, dep int) {
		// The last package's ReadsByALet now fails: its verdict read the
		// package only through the initializers' world
		// (ws0c-init-unrecorded, ws0c-world-dropped).
		u := treeReached(f, "reach.ergo", 1)
		f[pkgFile(u)] = strings.Replace(f[pkgFile(u)], "let Seed = []int{}\n", "let Seed = []int{7}\n", 1)
	}},
	{name: "defect", apply: func(r *rand.Rand, f map[string]string, p genprogram.Program, dep int) {
		// A body every verdict that enters it sees: Sum0 divides by
		// zero, so each test whose run reaches it is a defect, and a
		// verdict replayed past it (a read the cutoff did not record)
		// prints passed where the normal path prints the defect.
		i := r.IntN(len(p.Packages) - 1)
		f[pkgFile(i)] = strings.Replace(f[pkgFile(i)], "func Sum0(items []Item) int {\n\tlet mut total = 0\n", "func Sum0(items []Item) int {\n\tlet mut total = 1 / (len(items) - len(items))\n", 1)
	}},
	{name: "test", apply: func(r *rand.Rand, f map[string]string, p genprogram.Program, dep int) {
		name := genprogram.Name(r.IntN(len(p.Packages)-1)) + "/tests.ergo"
		f[name] += "\ntest Added {\n\tassert Sum0(Make0(2)) >= 0\n}\n"
	}},
}

// treeReached is the index of the k-th package the last package's file
// name (TestCheckTreeAnswersByTokens's unread.ergo or reach.ergo) imports.
func treeReached(f map[string]string, name string, k int) int {
	m := regexp.MustCompile(`/p(\d+)\n`).FindAllStringSubmatch(f[genprogram.Name(genprogram.Width+1)+"/"+name], -1)
	i, _ := strconv.Atoi(m[k][1])
	return i
}

// treeRun is one `ergo check DIR/...` in this process: what it printed
// and its stage counters.
type treeRun struct {
	out    []byte
	err    error
	counts map[string]int64
}

func runCheckTree(t *testing.T, pattern string, reference bool) treeRun {
	t.Helper()
	if reference {
		// The normal path: no warm answer read or written.
		os.Setenv("ERGO_BUILD", "full")
		defer os.Unsetenv("ERGO_BUILD")
	}
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	saved := os.Stdout
	os.Stdout = f
	stage.Reset()
	opts := fileOptions{command: "check", target: report.Target, file: pattern}
	err = recursiveCheckCommand(opts)
	os.Stdout = saved
	counts := stage.Snapshot()
	out, rerr := os.ReadFile(f.Name())
	if rerr != nil {
		t.Fatal(rerr)
	}
	return treeRun{out: out, err: err, counts: counts}
}

// treeText is what the property holds a check may read of a file: its
// tokens, and each comment's text with the line breaks before the next
// code (a binding's `// ergo:` line is read as the line above a
// declaration). It is read apart from syntax.CheckText, so a key that
// leaves a part out is caught. The generated sources have no `//` in a
// string.
var treeComment = regexp.MustCompile(`//[^\n]*([ \t\n]*)`)

func treeText(src string) string {
	var b strings.Builder
	b.WriteString(syntax.TokenText(src))
	for _, m := range treeComment.FindAllStringSubmatch(src, -1) {
		fmt.Fprintf(&b, "\x00%d %s", strings.Count(m[1], "\n"), m[0][:len(m[0])-len(m[1])])
	}
	return b.String()
}

// TestCheckTreeAnswersByTokens is the property of the warm answer of
// `ergo check DIR/...` by tokens (MONOREPO WS0, checktree.go): over
// generated modules and edit scripts (none, layout only, a comment, a
// body, a signature, a new file, a test, a package outside the request
// that it imports, the body of an import no test enters), for the whole module and for a sub-folder, the answer
// is byte-equal to the normal path's (ERGO_BUILD=full) after every edit,
// and, for a kept base, files_parsed, obligations_evaluated and
// eval_steps are 0 exactly when nothing the request reaches changed in
// its tokens, comments or comments' line breaks. The packages the cutoff
// evaluates again (WS0b, checkcut.go) are exactly those the reference
// loads' fingerprints name. An even seed's base has a reason that names a
// site, and is never kept. Mutants ws0-key-by-bytes, ws0-key-omits-imports,
// ws0-key-omits-comments, ws0-site-replayed, ws0-revision-replayed,
// ws0-keeps-site-reasons, ws0b-impl-not-verified, ws0b-keeps-after-puts,
// ws0b-key-omits-interface, ws0b-replays-with-diagnostics,
// ws0c-reads-not-verified, ws0c-plan-unrecorded, ws0c-init-unrecorded,
// ws0c-world-dropped and ws0c-cached-reads-dropped.
func TestCheckTreeAnswersByTokens(t *testing.T) {
	seeds := []uint64{}
	for s := range uint64(treeSeeds) {
		seeds = append(seeds, s+1)
	}
	if v := os.Getenv("ERGO_TREE_SEED"); v != "" {
		s, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{s}
	}
	var unkept []string
	treeUnkept = func(why string) { unkept = append(unkept, why) }
	defer func() { treeUnkept = nil }()
	for _, seed := range seeds {
		t.Setenv("ERGO_CACHE", t.TempDir())
		p := genprogram.GenerateProgram(seed, genprogram.Width+2, genprogram.Options{Groups: 2})
		// The sub-folder request: a package of the second layer, which
		// imports one of the first.
		sub := genprogram.Width
		deps := regexp.MustCompile(`import `+p.Module+`/p(\d+)\n`).FindAllStringSubmatch(p.Files[pkgFile(sub)], -1)
		if len(deps) == 0 {
			t.Fatalf("seed %d: package %d imports nothing", seed, sub)
		}
		dep, _ := strconv.Atoi(deps[0][1])
		// An import no obligation enters (WS0c): the last package also
		// imports a first-layer package it does not otherwise import, in a
		// function no test calls, so an edit of that package's body
		// changes an import's implementation that its verdicts did not
		// read (the "unread import" edit; its reads keep it).
		//
		// Two more first-layer packages it did not import are read by one
		// way each (reach.ergo): a call (the plan hook) and a package let
		// (the initializers' world). An edit of either flips a verdict of
		// the last package, so a read the cutoff did not record replays a
		// verdict the normal path does not print.
		//
		// A package with no test (quiet) imports the unread one: an edit
		// of that package's interface must evaluate it again, which only
		// the cut key's interfaces say (it has no obligation to check).
		last := genprogram.Width + 1
		var fresh []int
		for u := range genprogram.Width {
			if !strings.Contains(p.Files[pkgFile(last)], "import "+p.Module+"/"+genprogram.Name(u)+"\n") {
				fresh = append(fresh, u)
			}
		}
		if len(fresh) < 3 {
			t.Fatalf("seed %d: package %d imports all but %d first-layer packages", seed, last, len(fresh))
		}
		unread, byCall, byLet := genprogram.Name(fresh[0]), genprogram.Name(fresh[1]), genprogram.Name(fresh[2])
		p.Files[genprogram.Name(last)+"/unread.ergo"] = fmt.Sprintf("import %s/%s\n\n// unread is called by no test: no verdict of %s reads %s.\nfunc unread() int {\n\tlen(%s.Make0(0))\n}\n",
			p.Module, unread, genprogram.Name(last), unread, unread)
		p.Files[pkgFile(fresh[1])] += "\n// Probe is read by a call of " + genprogram.Name(last) + "'s tests.\nfunc Probe() int {\n\tlen(Make0(0))\n}\n"
		p.Files[pkgFile(fresh[2])] += "\n// Seed is read by a let of " + genprogram.Name(last) + "'s tests.\nlet Seed = []int{}\n"
		p.Files[genprogram.Name(last)+"/reach.ergo"] = fmt.Sprintf("import %[1]s/%[2]s\nimport %[1]s/%[3]s\n\ntest ReadsByACall {\n\tassert %[2]s.Probe() == 0\n}\n\ntest ReadsByALet {\n\tassert len(%[3]s.Seed) == 0\n}\n", p.Module, byCall, byLet)
		p.Files["quiet/quiet.ergo"] = fmt.Sprintf("import %[1]s/%[2]s\n\n// Q has no test: its evaluation holds no obligation.\nfunc Q() int {\n\tlen(%[2]s.Make0(0))\n}\n", p.Module, unread)
		if seed%2 == 0 {
			// A package let whose initializer calls Go: check time skips
			// the package's obligations with a reason that names the let's
			// site (eval's siteText).
			src := p.Files[pkgFile(sub)]
			p.Files[pkgFile(sub)] = strings.Replace(src, "\nimport strings\n", "\nimport strings\nimport ansi\n", 1) + "\n// Reverse is reverse video.\nlet Reverse = ansi.SGR(ansi.Style{..ansi.Plain, Reverse: true})\n"
		}
		dir := t.TempDir()
		writeFiles(t, dir, p.Files)
		patterns := []string{filepath.Join(dir, "..."), filepath.Join(dir, genprogram.Name(sub), "...")}
		hits, misses, narrowed := 0, 0, 0
		for _, edit := range treeEdits {
			for _, pattern := range patterns {
				// The base, kept: a run that stores verdicts keeps nothing,
				// so the second run keeps the answer.
				writeFiles(t, dir, p.Files)
				removeExtra(t, dir, p.Files)
				// A store of its own, so the base's second run keeps every
				// package's evaluation and the cutoff's account below starts
				// from the base alone.
				t.Setenv("ERGO_CACHE", t.TempDir())
				// Kept by either run: the first keeps the packages whose
				// evaluation stored no verdict.
				cutKept := map[string]bool{}
				for range 2 {
					unkept = nil
					treeCutKept = func(path string, obligations bool) { cutKept[path] = obligations }
					r := runCheckTree(t, pattern, false)
					treeCutKept = nil
					if r.err != nil {
						t.Fatalf("seed %d: the base does not check: %v\n%s", seed, r.err, r.out)
					}
				}
				before := treeFingerprints(t, pattern)
				// An even seed's base has a reason that names a site, which
				// a layout edit moves: it is never kept.
				kept := len(unkept) == 0
				if kept == (seed%2 == 0) {
					t.Fatalf("seed %d, %s: the base kept %v; reasons %q", seed, pattern, kept, unkept)
				}
				head := map[string]string{}
				for name, src := range p.Files {
					head[name] = src
				}
				edit.apply(rand.New(rand.NewPCG(seed, uint64(len(edit.name)))), head, p, dep)
				writeFiles(t, dir, head)
				// Only the folders the request reaches count: its own and
				// those they import.
				reached := treeReach(p, head, filepath.Dir(pattern), dir)
				same := true
				for name, src := range head {
					if !reached[filepath.Dir(name)] {
						continue
					}
					if old, ok := p.Files[name]; !ok || strings.HasSuffix(name, ".ergo") && treeText(old) != treeText(src) {
						same = false
					}
				}
				// The normal path twice, with the store emptied (q14: the
				// cutoff gives the verdicts a run that keeps nothing gives):
				// its first run stores every verdict, so its second prints
				// what every later run prints ("cached").
				store := os.Getenv("ERGO_CACHE")
				t.Setenv("ERGO_CACHE", t.TempDir())
				runCheckTree(t, pattern, true)
				want := runCheckTree(t, pattern, true)
				t.Setenv("ERGO_CACHE", store)
				after := treeFingerprints(t, pattern)
				reran := map[string]bool{}
				var replayed []string
				cutHit := map[string]bool{}
				treeRerun = func(path string) { reran[path] = true }
				treeCutReplayed = func(path string, narrowed, reads bool) {
					cutHit[path] = true
					replayed = append(replayed, fmt.Sprintf("%s (narrowed %v, reads %v)", path, narrowed, reads))
				}
				got := runCheckTree(t, pattern, false)
				treeRerun, treeCutReplayed = nil, nil
				fail := func(format string, args ...any) {
					t.Helper()
					var diff strings.Builder
					for name := range head {
						if head[name] != p.Files[name] {
							fmt.Fprintf(&diff, "--- %s at the base\n%s\n--- %s after the edit\n%s\n", name, p.Files[name], name, head[name])
						}
					}
					t.Fatalf("seed %d, edit %s, %s: %s\n%sthe base was not kept because: %q\nreplay: ERGO_TREE_SEED=%d go test -count=1 -run '^TestCheckTreeAnswersByTokens$' ./cmd/ergo", seed, edit.name, pattern, fmt.Sprintf(format, args...), diff.String(), unkept, seed)
				}
				if !bytes.Equal(got.out, want.out) || (got.err == nil) != (want.err == nil) {
					fail("the answer differs from the normal path's\n got %v\n%s\nwant %v\n%s", got.err, got.out, want.err, want.out)
				}
				// The run after it: a run that stored verdicts keeps
				// nothing, so this one prints what the emptied store's
				// second run prints, never the first's
				// (ws0b-keeps-after-puts).
				if again := runCheckTree(t, pattern, false); !bytes.Equal(again.out, want.out) || (again.err == nil) != (want.err == nil) {
					fail("the run after the edited one differs from the normal path's\n got %v\n%s\nwant %v\n%s", again.err, again.out, want.err, want.out)
				}
				work := got.counts["files_parsed"] + got.counts["obligations_evaluated"] + got.counts["eval_steps"]
				if kept && same != (work == 0) || !kept && work == 0 {
					fail("no token or comment changed: %v, but the run parsed %d files and evaluated %d obligations in %d steps", same, got.counts["files_parsed"], got.counts["obligations_evaluated"], got.counts["eval_steps"])
				}
				// The cutoff (WS0b, WS0c; checkcut.go): a package is
				// evaluated again when its base was not kept, it has a
				// diagnostic, its source changed or an interface of the
				// program changed (must); and only when that holds or it has
				// an obligation and an import's implementation changed (may):
				// within that, its verdicts' reads decide, and the answer
				// above being the emptied store's is what proves them. The
				// fingerprints are the reference loads' (CS5a,
				// TestIncrementalCheckDecides).
				if must, may, ok := treeWantRerun(before, after, cutKept); !ok {
					fail("the reference loads do not share a load")
				} else if kept && same {
					if len(reran) > 0 {
						fail("nothing changed, but the run evaluated %v", slices.Sorted(maps.Keys(reran)))
					}
				} else {
					// A defect stops the run at its package, as it stops the
					// normal path's (the answers above are equal): a package
					// after it was neither evaluated nor replayed, and the
					// cutoff decided nothing of it.
					if got.err != nil {
						for path := range after {
							if !reran[path] && !cutHit[path] {
								delete(must, path)
								delete(may, path)
							}
						}
					}
					for path, why := range must {
						if !reran[path] {
							fail("the cutoff kept %s, which it must evaluate (%s; evaluated %v; replayed %q; kept at the base: %v)", path, why, slices.Sorted(maps.Keys(reran)), replayed, cutKept)
						}
					}
					for path := range reran {
						if !may[path] {
							fail("the cutoff evaluated %s, which nothing it reads changed (kept at the base: %v)", path, cutKept)
						}
						delete(may, path)
					}
					// What the implementation rule would run and the reads
					// kept: the read set at work, as the run counts it.
					if n := got.counts["packages_kept_by_reads"]; n != int64(len(may)) {
						fail("the run counts %d packages kept by their reads, want %d: %v (replayed: %q; kept at the base: %v)", n, len(may), slices.Sorted(maps.Keys(may)), replayed, cutKept)
					}
					narrowed += len(may)
				}
				if len(reran) < len(after) {
					hits++
				}
				if len(reran) > 0 {
					misses++
				}
			}
		}
		// A seed whose edits took no answer from the store, or evaluated
		// nothing again, proved nothing of the store (WS0's seed 2 did not
		// assert a hit, and hid a layout edit that missed).
		if hits == 0 || misses == 0 {
			t.Fatalf("seed %d is vacuous: %d runs took a kept evaluation, %d evaluated again", seed, hits, misses)
		}
		// A seed where every changed implementation was read proved
		// nothing of the read set (WS0c).
		if narrowed == 0 {
			t.Fatalf("seed %d is vacuous: no package with an obligation was kept across an edit of an import it does not read", seed)
		}
	}
}

// treeFP is one selected package's fingerprints in a reference load, with
// its local imports' interfaces and implementations.
type treeFP struct {
	source string
	diags  bool
	ifaces map[string]string
	impls  map[string]string
}

// treeFingerprints are the selected packages' fingerprints of one
// shared load of pattern (loadShared, check only), by import path; nil
// when the packages do not share a load.
func treeFingerprints(t *testing.T, pattern string) map[string]treeFP {
	t.Helper()
	_, loads, ok := loadShared(fileOptions{command: "check", target: report.Target, file: pattern}, gobind.Platform{})
	if !ok {
		return nil
	}
	out := map[string]treeFP{}
	for _, l := range loads {
		fps := l.analysis.Fingerprints()
		by := map[string]fingerprint.Package{}
		for _, fp := range fps {
			by[fp.Path] = fp
		}
		root := fps[len(fps)-1]
		e := treeFP{source: root.Source, diags: len(l.analysis.Diagnostics) > 0, ifaces: map[string]string{}, impls: map[string]string{}}
		for _, fp := range fps[:len(fps)-1] {
			e.ifaces[fp.Path] = fp.Interface
		}
		for _, imp := range root.Imports {
			e.impls[imp] = by[imp].Implementation
		}
		out[l.analysis.Root().Path] = e
	}
	return out
}

// treeWantRerun are the packages the cutoff must evaluate after the base
// (before, with cutKept, the packages whose evaluation the base kept and
// whether each holds an obligation) is edited to after, and those it may:
// the must, and each with an obligation whose import's implementation
// changed (its verdicts' reads decide which; WS0c).
func treeWantRerun(before, after map[string]treeFP, cutKept map[string]bool) (must map[string]string, may map[string]bool, ok bool) {
	if before == nil || after == nil {
		return nil, nil, false
	}
	must, may = map[string]string{}, map[string]bool{}
	for path, a := range after {
		b, ok := before[path]
		obligations, kept := cutKept[path]
		why := ""
		switch {
		case !ok:
			why = "new"
		case !kept:
			why = "not kept at the base"
		case a.diags:
			why = "a diagnostic"
		case a.source != b.source:
			why = "its source changed"
		case !maps.Equal(a.ifaces, b.ifaces):
			for p := range a.ifaces {
				if a.ifaces[p] != b.ifaces[p] {
					why += " the interface of " + p
				}
			}
			why = "changed:" + why
		}
		if why != "" {
			must[path], may[path] = why, true
		} else if obligations && !maps.Equal(a.impls, b.impls) {
			may[path] = true
		}
	}
	return must, may, true
}

// treeReach are the folders (relative, slash-separated) a request
// walking from reaches in files: those below from and every folder they
// import, transitively, read from the import lines of the base's and the
// head's files.
func treeReach(p genprogram.Program, files map[string]string, from, dir string) map[string]bool {
	rel, _ := filepath.Rel(dir, from)
	rel = filepath.ToSlash(rel)
	imp := regexp.MustCompile(`(?m)^import ` + regexp.QuoteMeta(p.Module) + `/(\S+)$`)
	reached := map[string]bool{}
	var queue []string
	for name := range files {
		if d := filepath.Dir(name); d != "." && (rel == "." || d == rel || strings.HasPrefix(d, rel+"/")) && !reached[d] {
			reached[d] = true
			queue = append(queue, d)
		}
	}
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		for name, src := range files {
			if filepath.Dir(name) != d {
				continue
			}
			for _, m := range append(imp.FindAllStringSubmatch(src, -1), imp.FindAllStringSubmatch(p.Files[name], -1)...) {
				if !reached[m[1]] {
					reached[m[1]] = true
					queue = append(queue, m[1])
				}
			}
		}
	}
	return reached
}

// removeExtra removes the files under dir that an edit added and files
// does not hold.
func removeExtra(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	var extra []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if _, ok := files[filepath.ToSlash(rel)]; err == nil && !ok {
			extra = append(extra, path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(extra)
	for _, path := range extra {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
}
