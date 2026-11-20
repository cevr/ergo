package eval

import (
	"strconv"
	"strings"
	"testing"
)

// TestSlotBytesAreGoBytes is a guard over the byte budget's counter: a
// collection's slots cost what the Go target allocates for its element
// type, not two words each (MX-EVSLOT: lzpack's [65536]int32 tables cost
// four times Go's bytes, and its laws stopped on the byte budget where
// Go's memory was a quarter of it). Under a budget of 14 bytes a slot, an
// [n]int32 default and an n-element []int32 literal run at n and stop at
// 2n, so each slot costs 4 bytes, as Go's int32 does, twice (the first
// write through a new local copies its store once: a follow-up), and a
// copy after a lend costs the same; n appends to a default []byte (an array's element),
// then n/2 to a slice of half of it, run under 20 bytes a slot (Go's
// append doubles its store). Charged 16 bytes a slot, each stops at n.
// Mutants it kills (testdata/mutants.json): evslot-two-words,
// evslot-literal-two-words, evslot-literal-elem-lost,
// evslot-copy-elem-lost, evslot-array-elem-lost, evslot-zero-slice-elem,
// evslot-append-two-words, evslot-append-copy-elem-lost.
func TestSlotBytesAreGoBytes(t *testing.T) {
	const n = 4096
	zeros := func(k int) string { return strings.TrimSuffix(strings.Repeat("0, ", k), ", ") }
	programs := map[string]func(k int) string{
		"array": func(k int) string {
			return "func build() int {\n\tlet mut a = [" + strconv.Itoa(k) + "]int32{}\n\ta[1] = 1\n\tlen(a)\n}\n"
		},
		// A second write after a lend copies the store again, at its
		// element's size.
		"literal": func(k int) string {
			return "func build() int {\n\tlet mut a = []int32{" + zeros(k) + "}\n\ta[1] = 1\n\tlet c = a\n\ta[2] = 1\n\tlen(a) + int(c[2])\n}\n"
		},
		// A record's default slice field, appended to; then a slice of
		// half of it, appended past its end (a new store).
		"append": func(k int) string {
			return "func build() int {\n\tlet bs = [1][]byte{}\n\tlet mut a = bs[0]\n\tfor _ in 0.." + strconv.Itoa(k) + " {\n\t\ta = a.Append(1)\n\t}\n\tlet mut s = a[0 : " + strconv.Itoa(k/2) + "]\n\tfor _ in 0.." + strconv.Itoa(k/2) + " {\n\t\ts = s.Append(2)\n\t}\n\tlen(s)\n}\n"
		},
	}
	status := func(src string, k int, perSlot int64) Status {
		b := DefaultBudget()
		b.Steps = 100 * n
		b.Bytes = perSlot * n
		rep, err := Run(checked(t, src+"\ntest Build {\n\tassert build() == "+strconv.Itoa(k)+"\n}\n"), b)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rep.Results {
			if r.Name == "Build" {
				if r.Status != Passed && r.Status != Incomplete {
					t.Fatalf("%d: %s (%s)", k, r.Status, r.Reason)
				}
				return r.Status
			}
		}
		t.Fatalf("Build did not report: %+v", rep.Results)
		return ""
	}
	for _, name := range []string{"array", "literal", "append"} {
		perSlot := int64(14)
		if name == "append" {
			perSlot = 20
		}
		if got := status(programs[name](n), n, perSlot); got != Passed {
			t.Errorf("%s of %d int32 slots under %d bytes a slot: %s, want passed", name, n, perSlot, got)
		}
		if name == "append" {
			continue
		}
		if got := status(programs[name](2*n), 2*n, perSlot); got != Incomplete {
			t.Errorf("%s of %d slots under %d bytes for %d: %s, want incomplete (the bytes grow with the slots)", name, 2*n, perSlot, n, got)
		}
	}
}

// TestAppenderKeepsItsPlace is a guard over counted work: d.out =
// push(d.out, b), a pure extern that gives its first argument extended,
// assigned back to the place it read, lends nothing, so the next write
// of d.win through the same borrow writes in place, as Go does. Lent, it
// copied the whole window at each write: 200 appends beside 200 window
// writes cost about 200 window copies more than the writes alone (lzpack's
// Decoder, MX-EVSLOT). It kills evslot-appender-lends
// (testdata/mutants.json).
func TestAppenderKeepsItsPlace(t *testing.T) {
	rep, err := Run(checked(t, `extern go append func push[T](xs []T, x T) []T

type Dec { out []byte, win [1024]byte }

func run(d mut Dec, n int, app bool) {
	for i in 0..n {
		if app {
			d.out = push(d.out, byte(i))
		}
		d.win[i&1023] = byte(i)
	}
}

func work(n int, app bool) int {
	let mut d = Dec{out: []byte{}, win: [1024]byte{}}
	run(&d, n, app)
	len(d.out) + int(d.win[3])
}

test With {
	assert work(200, true) == 203
}

test Without {
	assert work(200, false) == 3
}
`), DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	steps := map[string]int64{}
	for _, r := range rep.Results {
		if r.Status != Passed {
			t.Fatalf("%s: %s (%s)", r.Name, r.Status, r.Reason)
		}
		steps[r.Name] = r.Steps
	}
	if steps["With"] == 0 || steps["Without"] == 0 {
		t.Fatalf("the tests did not run: %+v", rep.Results)
	}
	if d := steps["With"] - steps["Without"]; d > 200*50 {
		t.Errorf("200 appends cost %d steps beside the window writes (%d with, %d without): each write copies the window", d, steps["With"], steps["Without"])
	}
}
