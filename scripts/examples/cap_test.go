package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sizePairs are (stdout, stderr) sizes in bytes around the cap N: each edge
// of N on each stream, then seeded picks from the boundary set {0, 1, N-1,
// N, N+1, 2N+7} and from [0, 2N].
func sizePairs(seed uint64, count int) [][2]int {
	const n = outputCap
	random := rand.New(rand.NewPCG(seed, 3))
	boundary := []int{0, 1, n - 1, n, n + 1, 2*n + 7}
	pick := func() int {
		if random.IntN(2) == 0 {
			return boundary[random.IntN(len(boundary))]
		}
		return random.IntN(2*n + 1)
	}
	pairs := [][2]int{{n, n}, {n + 1, 0}, {0, n + 1}, {n - 1, n + 1}, {n + 1, n + 1}, {2*n + 7, 1}}
	for len(pairs) < count {
		pairs = append(pairs, [2]int{pick(), pick()})
	}
	return pairs
}

// writer is a shell program that writes $1 bytes to stdout, then $2 bytes to
// stderr.
const writer = "#!/bin/sh\nhead -c \"$1\" /dev/zero\nhead -c \"$2\" /dev/zero >&2\n"

// wantOver is the verdict of writer for these sizes: "" when both are within
// the cap; else the stream that went over first, and only it, since the
// runner stops the group at that byte, so writer never reaches stderr once
// stdout is over.
func wantOver(sizes [2]int) string {
	switch {
	case sizes[0] > outputCap:
		return fmt.Sprintf("stdout over %d bytes", outputCap)
	case sizes[1] > outputCap:
		return fmt.Sprintf("stderr over %d bytes", outputCap)
	}
	return ""
}

// Each stream of a command holds at most outputCap bytes. A case, a recorded
// original and an ergo test that write within the cap on both streams behave
// as before; one that writes more fails with a line of its own that names
// each stream over the cap and only those, prints no diff and no output,
// and records no golden.
func TestOutputCap(t *testing.T) {
	t.Parallel()
	zeros := func(size int) string { return string(make([]byte, size)) }
	label := func(i int) string { return fmt.Sprintf("c%02d", i) }
	// The printed output stays small: no diff and no dump of a stream.
	small := func(t *testing.T, what, out string) {
		t.Helper()
		if len(out) > 64<<10 {
			t.Errorf("%s printed %d bytes; an over-cap command prints no dump\n%.2000s", what, len(out), out)
		}
	}
	// Two streams that both went over before the stop (writers in a race)
	// are both named, stdout first.
	both := outcome{stdoutOver: true, stderrOver: true}.overCap()
	if want := fmt.Sprintf("stdout over %d bytes; stderr over %d bytes", outputCap, outputCap); both != want {
		t.Errorf("both streams over: %q, want %q", both, want)
	}

	t.Run("case", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		pairs := sizePairs(20261104, 16)
		cases := map[string]map[string]string{}
		for i, sizes := range pairs {
			files := map[string]string{"args": fmt.Sprintf("%d\n%d\n", sizes[0], sizes[1]), "expected.out": ""}
			if wantOver(sizes) == "" {
				files["expected.out"] = zeros(sizes[0])
				if sizes[1] > 0 {
					files["expected.err"] = zeros(sizes[1])
				}
			}
			cases[label(i)] = files
		}
		f.program("sizer", writer, "slow\n", cases, nil)
		code, out := f.run("-timeout", "30s", "sizer")
		small(t, "the case leg", out)
		oks, fails := verdicts(out)
		if code != 1 {
			t.Errorf("exit %d, want 1", code)
		}
		for i, sizes := range pairs {
			name := "sizer/" + label(i)
			over := wantOver(sizes)
			if over == "" && !oks[name] {
				t.Errorf("%s %v is within the cap: want ok, got FAIL%s", name, sizes, fails[name])
			}
			if over != "" && fails[name] != " "+over {
				t.Errorf("%s %v: want FAIL %s: %s, got %q (ok %t)", name, sizes, name, over, fails[name], oks[name])
			}
		}
	})

	t.Run("record", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		pairs := sizePairs(20261105, 12)
		cases := map[string]map[string]string{}
		for i, sizes := range pairs {
			cases[label(i)] = map[string]string{"args": fmt.Sprintf("%d\n%d\n", sizes[0], sizes[1]), "expected.out": "keep\n"}
		}
		dir := f.program("rec", "#!/bin/sh\necho port\n", "slow\n", cases, map[string]string{"record.sh": writer, "origin": "a fixture original\n"})
		code, out := f.run("-timeout", "30s", "-record", "rec")
		small(t, "-record", out)
		_, fails := verdicts(out)
		if code != 1 {
			t.Errorf("-record: exit %d, want 1", code)
		}
		for i, sizes := range pairs {
			name := "rec/" + label(i)
			over := wantOver(sizes)
			golden := func(file string) (string, bool) { return read(t, filepath.Join(dir, "cases", label(i), file)) }
			gotOut, _ := golden("expected.out")
			gotErr, hasErr := golden("expected.err")
			if over == "" {
				if !strings.Contains(out, "recorded "+name+"\n") || gotOut != zeros(sizes[0]) || hasErr != (sizes[1] > 0) || gotErr != zeros(sizes[1]) {
					t.Errorf("-record %s %v is within the cap: want recorded with goldens of %v bytes; got FAIL%s, expected.out %d bytes, expected.err %d bytes (present %t)",
						name, sizes, sizes, fails[name], len(gotOut), len(gotErr), hasErr)
				}
				continue
			}
			if fails[name] != " "+over || strings.Contains(out, "recorded "+name+"\n") {
				t.Errorf("-record %s %v: want FAIL %s: %s and no recorded line, got %q", name, sizes, name, over, fails[name])
			}
			if gotOut != "keep\n" || hasErr {
				t.Errorf("-record %s %v is over the cap and wrote a golden: expected.out %d bytes, expected.err present %t", name, sizes, len(gotOut), hasErr)
			}
		}
		code, out = f.run("-timeout", "30s", "-record", "-check", "rec")
		small(t, "-record -check", out)
		oks, fails := verdicts(out)
		if code != 1 {
			t.Errorf("-record -check: exit %d, want 1", code)
		}
		for i, sizes := range pairs {
			name := "rec/" + label(i)
			over := wantOver(sizes)
			if over == "" && !oks[name] {
				t.Errorf("-record -check %s %v against its fresh goldens: want ok, got FAIL%s", name, sizes, fails[name])
			}
			if over != "" && fails[name] != " "+over {
				t.Errorf("-record -check %s %v: want FAIL %s: %s, got %q", name, sizes, name, over, fails[name])
			}
		}
	})

	t.Run("test leg", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		pairs := sizePairs(20261106, 12)
		hi := map[string]map[string]string{"basic": {"expected.out": "hi\n"}}
		for i, sizes := range pairs {
			f.program("t"+label(i), fmt.Sprintf("#!/bin/sh\necho hi\nexit 0\n# TESTSIZE %d %d\ntest Fixture {\n}\n", sizes[0], sizes[1]), "default\n", hi, nil)
		}
		code, out := f.run()
		small(t, "the test leg", out)
		oks, _ := verdicts(out)
		if code != 1 {
			t.Errorf("exit %d, want 1", code)
		}
		lines := strings.Split(out, "\n")
		for i, sizes := range pairs {
			name := "t" + label(i)
			over := wantOver(sizes)
			passed := strings.Contains(out, "ok "+name+": test (")
			failed := strings.Contains(out, "FAIL "+name+": test")
			if over == "" && (!passed || failed) {
				t.Errorf("%s %v is within the cap: want ok %s: test", name, sizes, name)
			}
			if over != "" {
				want := "FAIL " + name + ": test: " + over
				if passed || !strings.Contains(out, want+"\n") {
					t.Errorf("%s %v: want the line %q", name, sizes, want)
				}
				for _, line := range lines {
					if strings.HasPrefix(line, "FAIL "+name+": test") && line != want {
						t.Errorf("%s: an extra test-leg line %q", name, line)
					}
				}
			}
			if !oks[name+"/basic"] {
				t.Errorf("no ok %s/basic: the test leg must not stop the cases", name)
			}
		}
	})
}

// A case that writes past the cap fails at once, well before -timeout: the
// runner stops its group, background children too, and keeps nothing of the
// stream on disk.
func TestOutputCapStopsTheGroupAtOnce(t *testing.T) {
	t.Parallel()
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			ticks := filepath.Join(t.TempDir(), "ticks")
			redirect := map[string]string{"stdout": "", "stderr": " >&2"}[stream]
			f.program("flood", "#!/bin/sh\n"+ticker(`"$TICKS"`)+fmt.Sprintf("head -c %d /dev/zero%s\nsleep 20\n", 4*outputCap, redirect), "slow\n",
				map[string]map[string]string{"out": {"env": "TICKS=" + ticks + "\n", "expected.out": ""}}, nil)
			started := time.Now()
			code, out := f.run("-timeout", "20s", "flood")
			took := time.Since(started)
			_, fails := verdicts(out)
			want := fmt.Sprintf(" %s over %d bytes", stream, outputCap)
			if code != 1 || fails["flood/out"] != want {
				t.Fatalf("want exit 1 and FAIL flood/out:%s; exit %d\n%.2000s", want, code, out)
			}
			if took > 5*time.Second {
				t.Errorf("the run took %s: the cap must stop the case at once, not at -timeout 20s", took)
			}
			if grows(t, ticks) {
				t.Error("the over-cap case's background child still runs")
			}
		})
	}
}

// A stream that a process outside the group holds (it called setsid) holds
// the runner at most about streamDelay after the group stopped, and a case
// whose leader exits while its own child holds stdout does not hold it.
func TestAStreamHolderOutsideTheGroupDoesNotHoldTheRunner(t *testing.T) {
	t.Parallel()
	if _, err := os.Stat("/usr/bin/setsid"); err != nil {
		t.Skip("no /usr/bin/setsid on this host")
	}
	f := newFixture(t)
	// The holder sleeps 4 s with the case's stdout open, then marks its end;
	// the test waits for the mark, so no holder outlives the test.
	ended := filepath.Join(t.TempDir(), "ended")
	t.Cleanup(func() {
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			if _, err := os.Stat(ended); err == nil {
				return
			}
		}
		t.Error("the holder never ended")
	})
	f.program("escaper", "#!/bin/sh\n/usr/bin/setsid sh -c 'sleep 4; touch \"$ENDED\"' &\necho done\n", "slow\n",
		map[string]map[string]string{"basic": {"env": "ENDED=" + ended + "\n", "expected.out": "done\n"}}, nil)
	started := time.Now()
	code, out := f.run("-timeout", "20s", "escaper")
	took := time.Since(started)
	if oks, _ := verdicts(out); code != 0 || !oks["escaper/basic"] {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if took > 3*time.Second {
		t.Fatalf("a stream held outside the group held the runner %s", took)
	}
}
