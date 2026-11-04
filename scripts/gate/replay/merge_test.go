package replay

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestMergeMismatchWithdrawsFirst is a property over the merge tier's end:
// uncached verdicts that differ from W(K) (a failed step, a step not run, a
// step set that differs) or that equal it, against an end collection that
// finds the key unchanged, a changed tracked input, a changed ignored input
// (a write the merge run admits), or an error. A proven mismatch withdraws
// W(K) and exits 1 in every case; equal verdicts keep W(K). (Round-1
// finding M1: an end-key change or an end-collection error returned before
// the withdrawal.)
func TestMergeMismatchWithdrawsFirst(t *testing.T) {
	type outcome struct {
		code      int
		withdrawn bool
	}
	ends := []string{"unchanged", "tracked input changed", "ignored input changed", "collection error"}
	checked := 0
	for seed := uint64(1); seed <= 25; seed++ {
		rng := rand.New(rand.NewPCG(seed, 0x6d65))
		names := []string{"a", "b", "c"}[:1+rng.IntN(3)]
		for _, mismatch := range []string{"none", "failed", "not run", "steps differ"} {
			for _, end := range ends {
				base := mergeFacts(rng)
				store := Open(t.TempDir())
				key := base().Key()
				recordPass(t, store, base, names)
				declared, verdicts := slices.Clone(names), map[string]Verdict{}
				for _, name := range names {
					verdicts[name] = Pass
				}
				victim := names[rng.IntN(len(names))]
				switch mismatch {
				case "failed":
					verdicts[victim] = Failed
				case "not run":
					delete(verdicts, victim)
				case "steps differ":
					declared = append(declared, "new step")
					verdicts["new step"] = Pass
				}
				collected := 0
				tier := MergeTier{
					Store: store,
					Collect: func() (*Facts, error) {
						collected++
						f := base()
						if collected == 1 {
							return f, nil
						}
						switch end {
						case "tracked input changed":
							f.Entries[1].Value = "-r--r--r--:changed"
						case "ignored input changed":
							f.Entries = append(f.Entries, Entry{Path: "dist/main", Class: Ignored, Kind: File, Value: fmt.Sprintf("%d:1:1:1:-rwxr-xr-x", rng.Int())})
						case "collection error":
							return nil, fmt.Errorf("the end collection failed")
						}
						return f, nil
					},
					Inner:    func() bool { t.Fatal("the inner tier ran with a record"); return false },
					Uncached: func() ([]string, map[string]Verdict) { return declared, verdicts },
				}
				var out bytes.Buffer
				code := tier.Run(&out)
				_, err := store.Load(key)
				got := outcome{code, err != nil}
				var want outcome
				switch {
				case mismatch != "none":
					want = outcome{1, true}
				case end == "unchanged", end == "ignored input changed":
					want = outcome{0, false}
				default:
					want = outcome{1, false}
				}
				if got != want {
					t.Errorf("seed %d, mismatch %s, end %s: exit %d withdrawn %t, want exit %d withdrawn %t:\n%s",
						seed, mismatch, end, got.code, got.withdrawn, want.code, want.withdrawn, out.String())
				}
				if mismatch != "none" && !strings.Contains(out.String(), "merge: FAILED: verdicts differ: ") {
					t.Errorf("seed %d, mismatch %s, end %s: no summary line:\n%s", seed, mismatch, end, out.String())
				}
				checked++
			}
		}
	}
	t.Logf("%d merges", checked)
}

// TestMergeMismatchWithdrawsOnEndChange is the round-1 verifier's
// reproduction of M1, kept as a regression test: a recorded pass at K, an
// uncached FAILED, then HEAD changes or the end collection fails.
func TestMergeMismatchWithdrawsOnEndChange(t *testing.T) {
	for _, collectErr := range []bool{false, true} {
		t.Run(fmt.Sprint(collectErr), func(t *testing.T) {
			store := Open(t.TempDir())
			facts := func() *Facts { return &Facts{Root: "/checkout", Head: "one"} }
			start := facts()
			key := start.Key()
			run := store.Begin(start)
			object, err := run.Output()
			if err != nil {
				t.Fatal(err)
			}
			_, _ = object.Write([]byte("passed\n"))
			run.Passed("step", object, "ran", 1, time.Now())
			if out := run.Finish(true, func() (*Facts, error) { return facts(), nil }, Meta{}); !strings.Contains(out, "recorded") {
				t.Fatal(out)
			}
			calls := 0
			tier := MergeTier{Store: store, Collect: func() (*Facts, error) {
				calls++
				f := facts()
				if calls > 1 {
					if collectErr {
						return nil, fmt.Errorf("cannot collect end facts")
					}
					f.Head = "two"
				}
				return f, nil
			}, Inner: func() bool { t.Fatal("unexpected inner"); return false },
				Uncached: func() ([]string, map[string]Verdict) {
					return []string{"step"}, map[string]Verdict{"step": Failed}
				}}
			var out bytes.Buffer
			code := tier.Run(&out)
			_, err = store.Load(key)
			t.Logf("merge exit=%d record retained=%t\n%s", code, err == nil, out.String())
			if code == 1 && err == nil {
				t.Fatal("FAILURE RECORD RETAINED after a proven verdict mismatch")
			}
		})
	}
}

// mergeFacts makes a generated checkout's facts: a source, an untracked
// file and generated host facts.
func mergeFacts(rng *rand.Rand) func() *Facts {
	head := fmt.Sprintf("head-%d", rng.IntN(1000))
	cpus := fmt.Sprint(1 + rng.IntN(64))
	return func() *Facts {
		return &Facts{
			Root: "/checkout", Head: head, Index: []string{"100644 b 0\tsrc/a.go"},
			Entries: []Entry{
				{Path: "src", Kind: Directory},
				{Path: "src/a.go", Class: Tracked, Kind: File, Value: "-r--r--r--:a"},
				{Path: "notes.txt", Class: Untracked, Kind: File, Value: "-r--r--r--:n"},
			},
			Environment: []Variable{{"GOFLAGS", false, ""}},
			Host:        []Fact{{"cpus", cpus}},
		}
	}
}

// recordPass records a passed run of names at the facts.
func recordPass(t *testing.T, store Store, facts func() *Facts, names []string) {
	t.Helper()
	run := store.Begin(facts())
	for _, name := range names {
		object, err := run.Output()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = object.Write([]byte(name + " passed\n"))
		run.Passed(name, object, "ran", 1, time.Now())
	}
	if line := run.Finish(true, func() (*Facts, error) { return facts(), nil }, Meta{Now: time.Now()}); !strings.HasPrefix(line, "gate: recorded") {
		t.Fatalf("the inner run did not record: %q", line)
	}
}
