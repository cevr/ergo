package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/opcount"
	"ergo.local/prototype/internal/proclimit"
)

// TestHandoverGrowsLinearly is the counted growth guard of OWN1's
// hand-over (internal/sketch/lower/handover.go): a program that grows one
// value step by step does work linear in its size, as hand Go's append
// does, never quadratic. Three programs, each at sizes n, 2n and 4n:
//
//   - examples/mdhtml on a document of n paragraphs: the block parser
//     appends each block, p.blocks = p.blocks.Append(block{…}) (in place,
//     V3);
//   - examples/router on n messages: each answer is appended to the
//     board, Board{..b, Answers: b.Answers.Append(…)} (in place: the
//     source is a field of a local dead after the call);
//   - testdata/ownring, GP's ring-copy repro (wf10 corpus GP) with n
//     slots and 20n pushes: r = push(r, i), whose push takes its
//     parameter over through its fields (fieldOwned) and writes one slot;
//   - testdata/ownvt, lane G's DT1 repro (examples/vt) on 3n bytes: a
//     []Event threaded through a per-byte step, ev = p.step(b, ev), whose
//     step owns its parameter (V5), so neither side copies the list (main
//     e02194103 cloned it at both);
//   - testdata/ownedit, GP2b's editor document on n edits: Make owns its
//     Doc through its fields (fieldOwned, with `return d`), threads
//     b = apply(b, e) through a match (V7, freshAll), and its caller takes
//     the result with no copy (own-fresh-call);
//   - testdata/ownwindow, lane PA's DF1 (flattree's scratch stack) on n
//     steps: the stack grows to n, and each step closes a two-item window
//     of it, kept and read, so the elements copied per step are the
//     window's, independent of the stack's length (gobody.go, goBodyView);
//   - testdata/hostsearch, LAWFFI LF1's host-search on n: n rank queries
//     sort.SearchInts(xs, i) over a sorted index of n keys, which Go only
//     reads (a checked reads fact, host-reads), so a query passes the
//     index with no copy: n log n work, where a copy per query is n².
//     At 16000, 32000, 64000 the head gives d2/d1 2.12 and an ergo whose
//     readsWhy refuses every site (mutant ffi-reads-off) 3.62 (2026-11-16);
//   - testdata/ownshrink, the self-layout plan's SL3 (the tree builder of
//     toktree-ergo-naive) on n groups: a scratch stack cut back to a mark,
//     stack = stack[0:mark], then appended to (own-reslice), which main
//     e02194103 cloned at each cut;
//   - testdata/reuse, the REUSE plan's shapes (§1) on one user type
//     Set { xs []int } whose Insert owns its argument, each a = f(a, …)
//     n times: g1 (Insert, in place since V5), g4 (an early return of the
//     whole parameter), f2 and f3 (a = InsertCapped(a, …) catch { … }, handed over
//     across the handled failure since RU2's own-catch), r1
//     and r2 (a part moved into a literal and an update), r3 (the prelude's
//     Append in an update), u1 (a union's payload), fv (Insert through a
//     function value); st (examples/jobs' settle in place, ST's gx9 item
//     1), whose copy of the operation's result is linear and which the
//     report must name; uc (std ui's old mirror, UC's g8): a Get on a Map of
//     records that hold a slice, through a pointer, which main e02194103
//     copied whole at each read (ergoCloneMap(m.nodes)[id]), quadratic in
//     n, and which MAPCOPY's entry rule (V8) reads in place; x1 (OWN1
//     st-x1's X1b): a plain Get of a let mut Map written later, which
//     main e02194103 also copied whole and V8 copies as the element read;
//     kc (OWN1 arms round 1, keepCard's Bump): an entry of a Map behind a
//     borrow replaced by keepCard(v.Value, k) in Some's arm, which RU8's
//     arm form of the entry move-out (moveout.go) hands the entry with no
//     copy; gm and gs (LWR-ELEM-APPEND, D1's naive GroupBy): n values into
//     16 groups grown in place, in a Map's entry (the arm form) and in an
//     element of a [][]int (sameElem).
//
// A row marked red names the unit that turns it green: the test skips its
// count (the build and the report's site still run) until that unit
// deletes the mark; OWN_GROWTH_KNOWN_RED=1 counts it too, so a unit shows
// its rows red at its base. Measured at RU0's head (2026-11-15): g4, f2,
// f3, r1, r2, r3, u1 and fv copy the Set at each step. A unit that turns
// a row green also gives it 200,000 steps: linear, a step costs a few
// instructions, and at 2,000 or 20,000 steps the runtime's own work (a
// collection, a heap growth) is as large as the difference counted.
//
// SL3's mutant mapcopy-sl3-reslice-never (the reslice shortcut never taken) is
// caught here as a growth and in TestHandoverAgrees by trim's copy.
//
// The guard is a ratio of counts, never of time: with d1 = I(2n) - I(n)
// and d2 = I(4n) - I(2n) the user-space instructions of the whole process,
// linear work gives d2/d1 near 2 and quadratic work near 4; the bound is
// 3.0, on the medians of 3 rounds. The programs run with the collector
// off (ownCount), because a collector's cycles step in number between
// sizes and moved the head's ratios between 0.56 and 5.47; with it off
// every program gives 1.96 to 2.03 (2026-11-17). It builds Go because the claim is about
// the machine work of the built program, which no in-process check of the
// lowered Go shows. Each program runs bounded by proclimit, and the test
// waits for its exit. A host with no counter of user-space instructions
// skips it and says why.
//
// Red on the reference: OWN_GROWTH_CLI=<an ergo binary> runs the guard on
// that binary's builds; an ergo built with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.valueLowering=copy
// (the copying lowering, which takes no hand-over) fails: with the
// collector off, ownring's copies exhaust its 2 GiB bound ("runtime: out
// of memory"), while the head's ownring stays near a megabyte
// (2026-11-17). With the collector on (GOGC=100 or 200) the reference gave
// router 4.97 to 5.95, ownring 3.45 to 3.73 and ownvt 3.69 to 3.84, but
// mdhtml 1.55 to 2.66, no different from the head. The mutant it alone catches: a hand-over that keeps
// values right but copies anyway (own-lineage or own-consume never taken,
// fieldOwned never owning), which no value property sees. It builds with
// -report copies and checks the growing site's line, so a silent copy
// report fails too (mutant own-copy-report-silent).
func TestHandoverGrowsLinearly(t *testing.T) {
	needsBuild(t)
	if err := opcount.Available(); err != nil {
		t.Skipf("no hardware counter of user-space instructions: %v", err)
	}
	cli := os.Getenv("OWN_GROWTH_CLI")
	if cli == "" {
		cli = buildTestCLI(t)
	}
	ws := smokeWorkspace(t, "examples/mdhtml", "examples/router", "cmd/ergo/testdata/ownring", "cmd/ergo/testdata/ownvt", "cmd/ergo/testdata/ownedit", "cmd/ergo/testdata/ownshrink", "cmd/ergo/testdata/ownwindow", "cmd/ergo/testdata/reuse", "cmd/ergo/testdata/hostsearch")
	dir := t.TempDir()
	type program struct {
		name string
		path string
		base int
		// site is the growing site the copy report must print, as
		// FILE:«SOURCE TEXT» FORM (siteReported).
		site string
		// run is the command and the stdin of the program at size n.
		run func(bin string, n int) (*exec.Cmd, string)
		// red, when set, is the unit that turns a known-red row green.
		red string
	}
	reuse := filepath.Join(ws, "cmd", "ergo", "testdata", "reuse", "reuse.ergo")
	shape := func(name string) func(bin string, n int) (*exec.Cmd, string) {
		return func(bin string, n int) (*exec.Cmd, string) {
			return exec.Command(bin, name, strconv.Itoa(n)), ""
		}
	}
	programs := []program{
		{"mdhtml", filepath.Join(ws, "examples", "mdhtml", "cmd", "mdhtml"), 4000, "markdown/blocks.ergo:«p.blocks = p.blocks.Append(block{» in place", func(bin string, n int) (*exec.Cmd, string) {
			var b strings.Builder
			for i := range n {
				fmt.Fprintf(&b, "Paragraph %d with *em* and `code`.\n\n", i)
			}
			return exec.Command(bin), b.String()
		}, ""},
		{"router", filepath.Join(ws, "examples", "router", "cmd", "router"), 2000, "route/machine.ergo:«Board{..b, Answers: b.Answers.Append(» in place", func(bin string, n int) (*exec.Cmd, string) {
			lines := []string{"route 1 logs disk", "route 2 metrics socket", "route 3 alerts stdout"}
			topics := []string{"logs", "metrics", "alerts"}
			for i := range n {
				lines = append(lines, "send "+topics[i%3]+" hello")
			}
			return exec.Command(bin), strings.Join(lines, "\n") + "\n"
		}, ""},
		{"ownring", filepath.Join(ws, "cmd", "ergo", "testdata", "ownring", "ring.ergo"), 1000, "", func(bin string, n int) (*exec.Cmd, string) {
			return exec.Command(bin, strconv.Itoa(n), strconv.Itoa(20*n)), ""
		}, ""},
		{"ownvt", filepath.Join(ws, "cmd", "ergo", "testdata", "ownvt", "vt.ergo"), 20000, "", func(bin string, n int) (*exec.Cmd, string) {
			return exec.Command(bin, strconv.Itoa(n)), ""
		}, ""},
		{"ownedit", filepath.Join(ws, "cmd", "ergo", "testdata", "ownedit", "edit.ergo"), 40000, "", func(bin string, n int) (*exec.Cmd, string) {
			return exec.Command(bin, strconv.Itoa(n)), ""
		}, ""},
		{"ownshrink", filepath.Join(ws, "cmd", "ergo", "testdata", "ownshrink", "shrink.ergo"), 10000, "", func(bin string, n int) (*exec.Cmd, string) {
			return exec.Command(bin, strconv.Itoa(n)), ""
		}, ""},
		{"ownwindow", filepath.Join(ws, "cmd", "ergo", "testdata", "ownwindow", "window.ergo"), 20000, "", func(bin string, n int) (*exec.Cmd, string) {
			return exec.Command(bin, strconv.Itoa(n)), ""
		}, ""},
		{"reuse-g1", reuse, 20000, "reuse.ergo:«for i in 0..n { a = Insert(a, i) }» owned call: calls Insert's owned body: s handed over", shape("g1"), ""},
		{"reuse-g4", reuse, 200000, "", shape("g4"), ""},
		{"reuse-f2", reuse, 200000, "", shape("f2"), ""},
		{"reuse-f3", reuse, 200000, "", shape("f3"), ""},
		{"reuse-r1", reuse, 200000, "", shape("r1"), ""},
		{"reuse-r2", reuse, 200000, "", shape("r2"), ""},
		{"reuse-r3", reuse, 200000, "", shape("r3"), ""},
		{"reuse-u1", reuse, 2000, "", shape("u1"), "RU3"},
		{"reuse-fv", reuse, 2000, "", shape("fv"), "RU4 (a known callback takes the owning form; plan §2, Indirect calls)"},
		{"reuse-st", reuse, 20000, "reuse.ergo:«let mut all = Store.All()» copy slice: ", shape("st"), ""},
		{"reuse-uc", reuse, 5000, "", shape("uc"), ""},
		{"reuse-x1", reuse, 5000, "reuse.ergo:«let mut k = v.Value» copy deep: ", shape("x1"), ""},
		{"reuse-kc", reuse, 200000, "", shape("kc"), ""},
		{"reuse-gm", reuse, 200000, "", shape("gm"), ""},
		{"reuse-gs", reuse, 200000, "", shape("gs"), ""},
		{"host-search", filepath.Join(ws, "cmd", "ergo", "testdata", "hostsearch", "search.ergo"), 16000, "search.ergo:13:31 no copy", func(bin string, n int) (*exec.Cmd, string) {
			return exec.Command(bin, strconv.Itoa(n)), ""
		}, ""},
	}
	knownRed := os.Getenv("OWN_GROWTH_KNOWN_RED") == "1"
	built := map[string]string{}
	reports := map[string]string{}
	for _, p := range programs {
		bin, ok := built[p.path]
		if !ok {
			bin = filepath.Join(dir, p.name)
			build := exec.Command(cli, "build", "-report", "copies", "-o", bin, p.path)
			var out bytes.Buffer
			build.Stdout, build.Stderr = &out, &out
			if err := proclimit.Run(build, 4<<30); err != nil {
				t.Fatalf("ergo build %s: %v\n%s", p.path, err, out.Bytes())
			}
			built[p.path], reports[p.path] = bin, out.String()
		}
		out := bytes.NewBufferString(reports[p.path])
		// The copy report names the growing site's form (a report that
		// lists no site, or the wrong form, fails here).
		if p.site != "" && !siteReported(t, p.path, p.site, out.String()) {
			t.Errorf("ergo build -report copies %s: no line for %q in\n%s", p.name, p.site, out.Bytes())
		}
		if p.red != "" && !knownRed {
			t.Logf("%s: known red until %s (OWN_GROWTH_KNOWN_RED=1 counts it)", p.name, p.red)
			continue
		}
		const rounds = 3
		var d1s, d2s []float64
		for range rounds {
			var counts [3]uint64
			for k, n := range []int{p.base, 2 * p.base, 4 * p.base} {
				cmd, stdin := p.run(bin, n)
				counts[k] = ownCount(t, cmd, stdin)
			}
			d1s = append(d1s, float64(counts[1])-float64(counts[0]))
			d2s = append(d2s, float64(counts[2])-float64(counts[1]))
		}
		d1, d2 := brcMedian(d1s), brcMedian(d2s)
		ratio := d2 / d1
		t.Logf("%s at %d, %d, %d: d1 %.0f, d2 %.0f instructions; d2/d1 %.2f", p.name, p.base, 2*p.base, 4*p.base, d1, d2, ratio)
		if d1 <= 0 || ratio > 3.0 {
			t.Errorf("%s grows faster than linearly: d2/d1 = %.2f (d1 %.0f, d2 %.0f), want at most 3.0", p.name, ratio, d1, d2)
		}
	}
}

// ownCount runs cmd with stdin under the instructions counter
// (internal/opcount), with GOMAXPROCS=1, and returns its user-space
// instructions; a refused or failed run fails the test.
func ownCount(t *testing.T, cmd *exec.Cmd, stdin string) uint64 {
	t.Helper()
	// The count is the mutator's: the collector is off (GOGC=off, no
	// GOMEMLIMIT), whatever the caller's environment. With it on, a run's
	// few whole-heap cycles step in number between sizes and move the ratio
	// either way: the gate's GOGC=400 and GOMEMLIMIT (keyedEnvironment) gave
	// mdhtml 4.44 and ownedit 0.74 (merge tier at fca9f4f96), and rt.Pace's
	// 64 MiB floor gave ownedit 3.85 to 5.05 (2026-11-17), where the
	// mutator's work is linear. A copying lowering still does quadratic work,
	// or exceeds the run's 2 GiB bound, and fails either way.
	cmd.Env = append(slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains([]string{"GOGC", "GOMEMLIMIT", "GODEBUG", "GOMAXPROCS"}, name)
	}), "GOMAXPROCS=1", "GOGC=off")
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	r, err := opcount.Count(cmd, 2<<30)
	if err != nil {
		t.Fatal(err)
	}
	if refusal, why := r.Refusal(); refusal != "" {
		t.Fatalf("%v: %s: %s\n%.2000s", cmd.Args, refusal, why, out.Bytes())
	}
	return r.Instructions
}

// siteReported says whether report, an ergo build -report copies of the
// program at path, has a line for site, written FILE:«TEXT» FORM: FILE is
// the source file as the report names it (relative to the program's
// module, or to the file's folder for a one-file program), TEXT the one
// line of it that holds the site, and FORM the report's words after the
// position. The site is found by its text, not pinned by its line, so an
// edit elsewhere in the file does not fail the guard (I22 Q10: mdhtml's
// rewrite moved the pinned line); a report that names no line of TEXT in
// FORM fails it (own-copy-report-silent).
func siteReported(t *testing.T, path, site, report string) bool {
	t.Helper()
	file, rest, ok := strings.Cut(site, ":«")
	text, form, ok2 := strings.Cut(rest, "»")
	if !ok || !ok2 {
		t.Fatalf("site %q is not FILE:«TEXT» FORM", site)
	}
	root := path
	if strings.HasSuffix(path, ".ergo") {
		root = filepath.Dir(path)
	} else {
		for {
			if _, err := os.Stat(filepath.Join(root, "ergo.mod")); err == nil {
				break
			}
			parent := filepath.Dir(root)
			if parent == root {
				t.Fatalf("no ergo.mod above %s", path)
			}
			root = parent
		}
	}
	source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if err != nil {
		t.Fatal(err)
	}
	line := 0
	for i, l := range strings.Split(string(source), "\n") {
		if strings.Contains(l, text) {
			if line != 0 {
				t.Fatalf("%s: %q is on lines %d and %d; name the site by text on one line", file, text, line, i+1)
			}
			line = i + 1
		}
	}
	if line == 0 {
		t.Fatalf("%s: no line holds %q", file, text)
	}
	prefix := fmt.Sprintf("%s:%d:", file, line)
	for _, l := range strings.Split(report, "\n") {
		_, after, found := strings.Cut(l, prefix)
		if !found {
			continue
		}
		if _, words, ok := strings.Cut(after, " "); ok && strings.HasPrefix(" "+words, form) {
			return true
		}
	}
	return false
}
