package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"ergo.local/prototype/internal/proclimit"
)

// TestHandoverGrowsLinearly is the counted growth guard of OWN1's
// hand-over (internal/sketch/lower/handover.go): a program that grows one
// value step by step does work linear in its size, as hand Go's append
// does, never quadratic. Three programs, each at sizes n, 2n and 4n:
//
//   - examples/mdhtml on a document of n paragraphs: Read folds the
//     emitted blocks into doc with doc = slices.Concat(doc, closed)
//     (own-consume);
//   - examples/router on n messages: each Told step appends to the Idle
//     entry's log, Idle{..i, Log: i.Log.Append(d)} (own-lineage), and the
//     memory journal appends each signal (Ref.Update, own-lineage);
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
//     the result with no copy (own-fresh-call).
//
// The guard is a ratio of counts, never of time: with d1 = I(2n) - I(n)
// and d2 = I(4n) - I(2n) the user-space instructions of the whole process,
// linear work gives d2/d1 near 2 and quadratic work near 4; the bound is
// 3.0, on the medians of 3 rounds. The collector's marking of a larger
// heap moved router's linear ratio between 1.7 and 2.4 across runs, and
// the copying reference's lowest ratio is 3.41. It builds Go because the claim is about
// the machine work of the built program, which no in-process check of the
// lowered Go shows. Each program runs bounded by proclimit, and the test
// waits for its exit. A host with no counter of user-space instructions
// skips it and says why.
//
// Red on the reference: OWN_GROWTH_CLI=<an ergo binary> runs the guard on
// that binary's builds; an ergo built with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.valueLowering=copy
// (the copying lowering, which takes no hand-over) gives d2/d1 5.34
// (mdhtml), 4.75 (router) and 3.41 (ownring), and the head 2.03, 1.96 and
// 2.08 (2026-11-13). The mutant it alone catches: a hand-over that keeps
// values right but copies anyway (own-lineage or own-consume never taken,
// fieldOwned never owning), which no value property sees. It builds with
// -report copies and checks the growing site's line, so a silent copy
// report fails too (mutant own-copy-report-silent).
func TestHandoverGrowsLinearly(t *testing.T) {
	needsBuild(t)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if fd, err := brcOpenCounter(); err != nil {
		t.Skipf("no hardware counter of user-space instructions: %v", err)
	} else {
		syscall.Close(fd)
	}
	cli := os.Getenv("OWN_GROWTH_CLI")
	if cli == "" {
		cli = buildTestCLI(t)
	}
	ws := smokeWorkspace(t, "examples/mdhtml", "examples/router", "cmd/ergo/testdata/ownring", "cmd/ergo/testdata/ownvt", "cmd/ergo/testdata/ownedit")
	dir := t.TempDir()
	type program struct {
		name string
		path string
		base int
		// site is a line the copy report must print: the growing site.
		site string
		// run is the command and the stdin of the program at size n.
		run func(bin string, n int) (*exec.Cmd, string)
	}
	programs := []program{
		{"mdhtml", filepath.Join(ws, "examples", "mdhtml", "cmd", "mdhtml"), 4000, "markdown/blocks.ergo:155:10 in place", func(bin string, n int) (*exec.Cmd, string) {
			var b strings.Builder
			for i := range n {
				fmt.Fprintf(&b, "Paragraph %d with *em* and `code`.\n\n", i)
			}
			return exec.Command(bin), b.String()
		}},
		{"router", filepath.Join(ws, "examples", "router", "cmd", "router"), 2000, "route/machine.ergo:289:38 one extender", func(bin string, n int) (*exec.Cmd, string) {
			lines := []string{"route 1 logs disk", "route 2 metrics socket", "route 3 alerts stdout"}
			topics := []string{"logs", "metrics", "alerts"}
			for i := range n {
				lines = append(lines, "send "+topics[i%3]+" hello")
			}
			return exec.Command(bin), strings.Join(lines, "\n") + "\n"
		}},
		{"ownring", filepath.Join(ws, "cmd", "ergo", "testdata", "ownring", "ring.ergo"), 1000, "", func(bin string, n int) (*exec.Cmd, string) {
			return exec.Command(bin, strconv.Itoa(n), strconv.Itoa(20*n)), ""
		}},
		{"ownvt", filepath.Join(ws, "cmd", "ergo", "testdata", "ownvt", "vt.ergo"), 20000, "", func(bin string, n int) (*exec.Cmd, string) {
			return exec.Command(bin, strconv.Itoa(n)), ""
		}},
		{"ownedit", filepath.Join(ws, "cmd", "ergo", "testdata", "ownedit", "edit.ergo"), 40000, "", func(bin string, n int) (*exec.Cmd, string) {
			return exec.Command(bin, strconv.Itoa(n)), ""
		}},
	}
	for _, p := range programs {
		bin := filepath.Join(dir, p.name)
		build := exec.Command(cli, "build", "-report", "copies", "-o", bin, p.path)
		var out bytes.Buffer
		build.Stdout, build.Stderr = &out, &out
		if err := proclimit.Run(build, 4<<30); err != nil {
			t.Fatalf("ergo build %s: %v\n%s", p.path, err, out.Bytes())
		}
		// The copy report names the growing site's form (a report that
		// lists no site, or the wrong form, fails here).
		if p.site != "" && !strings.Contains(out.String(), p.site) {
			t.Errorf("ergo build -report copies %s: no line with %q in\n%s", p.name, p.site, out.Bytes())
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

// ownCount runs cmd with stdin under the instructions counter, with
// GOMAXPROCS=1, and returns its user-space instructions; the run must
// exit 0.
func ownCount(t *testing.T, cmd *exec.Cmd, stdin string) uint64 {
	t.Helper()
	fd, err := brcOpenCounter()
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	cmd.Env = append(os.Environ(), "GOMAXPROCS=1")
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	brcIoctl(fd, brcPerfEnable)
	err = proclimit.Run(cmd, 2<<30)
	brcIoctl(fd, brcPerfDisable)
	if err != nil {
		t.Fatalf("%v: %v\n%.2000s", cmd.Args, err, out.Bytes())
	}
	var b [8]byte
	if n, err := syscall.Read(fd, b[:]); err != nil || n != 8 {
		t.Fatalf("read the counter: %d bytes, %v", n, err)
	}
	return binary.NativeEndian.Uint64(b[:])
}
