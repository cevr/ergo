package fix_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"

	format "ergo.local/prototype/internal/hostsyntax"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/fix"
)

// TestMutDropsLent (MODES-4; SIMPLIFY U2, owner q169, q223) is a seeded
// property of the lent rewrite. Each seed writes one program twice: with
// generic functions marked lent ([T lent C], [T, U lent C], [T lent]) and
// called on a borrow &t, and as ergo fix writes it ([T C] with each
// parameter of type T, in the signature, in a function type of it and in
// a function literal of the body, written x mut T; [T] with its
// parameters read), beside generic functions with no mark that the fix
// leaves alone. Then: fix(old) is byte-equal to the new spelling and
// counts its edits; a second fix writes nothing; format leaves the new
// spelling as written; the new spelling checks clean; and the old one is
// refused with "lent is gone" on each line that writes lent, and on no
// line the fix leaves as it is.
// ERGO_SEED=<n> replays one seed; a failure prints both programs.
//
// Mutants it kills that no other test does (ledger: fix, modes4-*): fix
// deletes lent but writes no mut; fix misses a function type's or a
// function literal's parameter; fix writes mut for [T lent]; the checker
// admits lent again.
func TestMutDropsLent(t *testing.T) {
	var seeds []uint64
	if s := os.Getenv("ERGO_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{n}
	} else {
		for i := range uint64(120) {
			seeds = append(seeds, i+1)
		}
	}
	kinds := map[string]int{}
	for _, seed := range seeds {
		oldSrc, newSrc, edits, reached := lentProgram(rand.New(rand.NewPCG(seed, 0x1e47)))
		for k := range reached {
			kinds[k]++
		}
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d: %s\nReplay: ERGO_SEED=%d go test -run '^TestMutDropsLent$' ./internal/sketch/fix\nold:\n%s\nnew:\n%s", seed, fmt.Sprintf(format, args...), seed, oldSrc, newSrc)
		}
		fixed, n, err := fix.Mut(oldSrc)
		if err != nil {
			fail("fix: %v", err)
		}
		if fixed != newSrc {
			fail("fix(old) is not the new spelling:\n%s", fixed)
		}
		if n != edits {
			fail("fix counts %d edits, the new spelling makes %d", n, edits)
		}
		if again, k, _ := fix.Mut(newSrc); k != 0 || again != newSrc {
			fail("a second fix makes %d edits", k)
		}
		if out, err := format.Source(newSrc); err != nil || out != newSrc {
			fail("format gives (%v):\n%s", err, out)
		}
		if _, diags := check.CheckSource(newSrc); len(diags) != 0 {
			fail("the new spelling does not check: %v", diags)
		}
		_, diags := check.CheckSource(oldSrc)
		marked, changed := map[int]bool{}, map[int]bool{}
		newLines := strings.Split(newSrc, "\n")
		for i, l := range strings.Split(oldSrc, "\n") {
			if strings.Contains(l, " lent") {
				marked[i+1] = true
			}
			changed[i+1] = l != newLines[i]
		}
		gone := map[int]bool{}
		for _, d := range diags {
			if d.Code == "S108" && strings.HasPrefix(d.Message, "lent is gone") && marked[d.Line] {
				gone[d.Line] = true
				continue
			}
			if !changed[d.Line] && !strings.Contains(d.Message, " cannot take a borrow as its ") {
				// The old call that lent &t to a lent T is refused at the
				// call, which the fix leaves: the declaration changes.
				fail("the old spelling is refused on a line the fix leaves: %s", d)
			}
		}
		for l := range marked {
			if !gone[l] {
				fail("line %d writes lent and is not refused as lent", l)
			}
		}
	}
	t.Logf("%d seeds reach: %v", len(seeds), kinds)
	if len(seeds) > 1 {
		for _, k := range []string{"op", "group", "functype", "lit", "bare", "read op", "unmarked"} {
			if kinds[k] == 0 {
				t.Errorf("no seed reaches %s", k)
			}
		}
	}
}

// TestFixMarksMutOperations (MODES-4): after [T lent C] became
// [T C](x mut T), the implements pass of ergo fix marks mut each
// operation of C that a type argument implements with a function on
// mut T, and only those: Bump, whose function writes, becomes mut Bump;
// Get, whose function reads, stays. Then the program checks clean, and a
// second pass writes nothing. The rest of the pass is TestFixProperty's.
//
// Mutants it kills that no other test does (ledger: fix, modes4-*): the
// mut pair dropped (the constraint stays refused); every operation of C
// marked; mut written twice by two instantiations.
func TestFixMarksMutOperations(t *testing.T) {
	src := `// law-exempt: the test's operation type.
type Counter {
	Bump() int,
	Get() int,
}

type Tally { n int } implements Counter

func Bump(t mut Tally) int {
	t.n = t.n + 1
	t.n
}

func Get(t Tally) int { t.n }

func twice[C Counter](c mut C) int { c.Bump() + c.Bump() + c.Get() }

func again[C Counter](c mut C) int { c.Bump() }

func main() {
	let mut t = Tally{n: 0}
	let _ = twice(&t) + again(&t)
}
`
	file := t.TempDir() + "/main.ergo"
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := fix.Run(fix.Options{Paths: []string{file}, Write: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(src, "\tBump() int,", "\tmut Bump() int,", 1)
	if string(got) != want {
		t.Fatalf("ergo fix wrote (%s):\n%s", rep.Text(), got)
	}
	if _, diags := check.CheckSource(want); len(diags) != 0 {
		t.Fatalf("the fixed program does not check: %v", diags)
	}
	again, err := fix.Run(fix.Options{Paths: []string{file}, Write: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Written) != 0 || len(again.Unwritten) != 0 {
		t.Fatalf("a second pass writes: %s", again.Text())
	}
}

// lentProgram writes one program with lent (old) and as ergo fix writes
// it (new), the number of edits between them, and the kinds of site it
// reached.
func lentProgram(r *rand.Rand) (string, string, int, map[string]bool) {
	reached := map[string]bool{}
	var o, n, calls strings.Builder
	edits := 0
	both := func(s string) { o.WriteString(s); n.WriteString(s) }
	// pair writes old with each "@" deleted, and new with each " lent"
	// deleted and each "@" written "mut ".
	pair := func(s string) {
		o.WriteString(strings.ReplaceAll(s, "@", ""))
		n.WriteString(strings.ReplaceAll(strings.ReplaceAll(s, " lent", ""), "@", "mut "))
		edits += strings.Count(s, " lent") + strings.Count(s, "@")
	}
	both("// law-exempt: the property's operation type.\ntype Counter { mut Bump() int }\n\n")
	both("// law-exempt: the property's operation type.\ntype Reader { Get() int }\n\n")
	both("type Tally { n int } implements Counter, Reader\n\n")
	both("func Bump(t mut Tally) int {\n\tt.n = t.n + 1\n\tt.n\n}\n\n")
	both("func Get(t Tally) int { t.n }\n\n")
	for i := range 2 + r.IntN(4) {
		switch r.IntN(7) {
		case 0:
			reached["op"] = true
			pair(fmt.Sprintf("func g%d[T lent Counter](x @T, k int) int { x.Bump() + k }\n\n", i))
			fmt.Fprintf(&calls, "\tlet r%d = g%d(&t, %d)\n", i, i, i)
		case 1:
			reached["group"] = true
			pair(fmt.Sprintf("func g%d[T, U lent Counter](x @T, y @U) int { x.Bump() + y.Bump() }\n\n", i))
			fmt.Fprintf(&calls, "\tlet r%d = g%d(&t, &u)\n", i, i)
		case 2:
			reached["functype"] = true
			pair(fmt.Sprintf("func g%d[T lent Counter](x @T, f func(v @T) int) int { f(x) }\n\n", i))
			fmt.Fprintf(&calls, "\tlet r%d = g%d(&t, func(v mut Tally) int { v.Bump() })\n", i, i)
		case 3:
			reached["lit"] = true
			pair(fmt.Sprintf("func g%d[T lent Counter](x @T) int {\n\tlet h = func(v @T) int { v.Bump() }\n\th(x)\n}\n\n", i))
			fmt.Fprintf(&calls, "\tlet r%d = g%d(&t)\n", i, i)
		case 4:
			// [T lent]: T has no operation, so its parameters stay read.
			reached["bare"] = true
			pair(fmt.Sprintf("func g%d[T lent](x T) int { 1 }\n\n", i))
			fmt.Fprintf(&calls, "\tlet r%d = g%d(t)\n", i, i)
		case 5:
			reached["read op"] = true
			pair(fmt.Sprintf("func g%d[T lent Reader](x @T) int { x.Get() }\n\n", i))
			fmt.Fprintf(&calls, "\tlet r%d = g%d(&t)\n", i, i)
		default:
			reached["unmarked"] = true
			both(fmt.Sprintf("func g%d[T Counter](x mut T) int { x.Bump() }\n\n", i))
			fmt.Fprintf(&calls, "\tlet r%d = g%d(&t)\n", i, i)
		}
		fmt.Fprintf(&calls, "\tlet _ = r%d\n", i)
	}
	both("func main() {\n\tlet mut t = Tally{n: 0}\n\tlet mut u = Tally{n: 1}\n\tlet _ = &u\n")
	both(calls.String())
	both("}\n")
	return o.String(), n.String(), edits, reached
}
