package build

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/proclimit"
)

// capInput is baseInput made distinct by i: one program of a family.
func capInput(i int) Input {
	in := baseInput()
	in.Sources[0].Text = []byte(fmt.Sprintf("func main() { _ = %d }\n", i))
	return in
}

// TestEntryCapHoldsAfterBuilds: with the cap set small and the age trim not
// due, after each of K builds of distinct programs Root/modules holds at
// most entryCap entries, and they are the entryCap most recently built:
// the cap pass removes the least recently used, never the newest. Mutants:
// a publication that does not count (cache-bound-cap-not-counted), a cap
// pass that removes newest first (cache-bound-cap-evicts-newest), one that
// removes past the excess (cache-bound-cap-removes-all).
func TestEntryCapHoldsAfterBuilds(t *testing.T) {
	needLocks(t)
	defer func(old int) { entryCap = old }(entryCap)
	entryCap = 3
	root := t.TempDir()
	c := Cache{Root: root}
	var built []string
	k := 3 * entryCap
	for i := range k {
		writeTrimStamp(t, root, 0) // the age trim is not due: only the cap removes
		in := capInput(i)
		m, err := c.Module(in, emitFor(in, nil))
		if err != nil {
			t.Fatal(err)
		}
		m.Release()
		// The file system's clock is coarse (a few milliseconds on ext4),
		// so builds this fast can share a mark; spread them a minute apart.
		ageBy(t, m.Dir, time.Duration(k-i)*time.Minute)
		built = append(built, filepath.Base(m.Dir))
		kept := entries(t, root, "modules")
		want := slices.Clone(built[max(0, len(built)-entryCap):])
		slices.Sort(want)
		if !slices.Equal(kept, want) {
			t.Fatalf("after build %d with entryCap %d: modules holds %q, want the %d newest %q", i+1, entryCap, kept, len(want), want)
		}
	}
}

// Variables of the cap-churn helper: its seed, its rounds and its cap.
const (
	seedVariable   = "ERGO_BUILD_TEST_SEED"
	roundsVariable = "ERGO_BUILD_TEST_ROUNDS"
	capVariable    = "ERGO_BUILD_TEST_CAP"
)

// capChurnHelper is helper mode "cap-churn": with entryCap from its
// environment it builds programs the seed picks from a small family into
// the cache at $ERGO_BUILD_TEST_ROOT, and while it holds each module it
// runs the cap pass itself, then checks that its module's directory is
// still the published entry and verifies. Two of these at once, each
// trimming while the other builds, are the property below. It prints the
// number of entries its passes removed.
func capChurnHelper() int {
	seed, err1 := strconv.ParseUint(os.Getenv(seedVariable), 10, 64)
	rounds, err2 := strconv.Atoi(os.Getenv(roundsVariable))
	limit, err3 := strconv.Atoi(os.Getenv(capVariable))
	if err1 != nil || err2 != nil || err3 != nil {
		fmt.Fprintln(os.Stderr, "cap-churn: bad seed, rounds or cap")
		return 2
	}
	entryCap = limit
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	c := Cache{Root: os.Getenv(rootVariable)}
	removed := 0
	for round := range rounds {
		in := capInput(r.IntN(3 * limit))
		m, err := c.Module(in, emitFor(in, nil))
		if err != nil {
			fmt.Fprintf(os.Stderr, "cap-churn round %d: %v\n", round, err)
			return 1
		}
		removed += c.capEntries()
		summary, err := in.Summarize()
		if err == nil {
			_, err = verify(m.Dir, m.Key, summary)
		}
		m.Release()
		if err != nil {
			fmt.Fprintf(os.Stderr, "cap-churn round %d: a trim removed or broke the entry this build holds: %v\n", round, err)
			return 1
		}
	}
	fmt.Println(removed)
	return 0
}

// TestCapNeverRemovesAHeldEntry is a seeded property with two processes:
// each builds programs of one small family into one cache whose cap is 2,
// and runs the cap pass while it holds each of its modules, so each pass
// races the other process's builds. No build ever finds the entry it holds
// removed or broken, and the passes did remove entries, so the race was
// run. Each helper runs under a memory bound the kernel enforces
// (proclimit). A failing seed prints its replay command. Mutant: a cap
// pass that removes without the entry's lock (cache-bound-cap-ignores-holds).
func TestCapNeverRemovesAHeldEntry(t *testing.T) {
	needLocks(t)
	seed := uint64(time.Now().UnixNano())
	if given := os.Getenv(seedVariable); given != "" {
		var err error
		if seed, err = strconv.ParseUint(given, 10, 64); err != nil {
			t.Fatal(err)
		}
	}
	replay := fmt.Sprintf("%s=%d go test -count=1 -run '^TestCapNeverRemovesAHeldEntry$' ./internal/sketch/build", seedVariable, seed)
	root := t.TempDir()
	var children []*exec.Cmd
	var outputs []*bytes.Buffer
	for i := range uint64(2) {
		child := helper(root, "cap-churn")
		child.Env = append(child.Env, seedVariable+"="+strconv.FormatUint(seed+i, 10), roundsVariable+"=150", capVariable+"=2")
		var out bytes.Buffer
		child.Stdout, child.Stderr = &out, &out
		if err := proclimit.Start(child, 512<<20); err != nil {
			t.Fatal(err)
		}
		children, outputs = append(children, child), append(outputs, &out)
	}
	removed := 0
	for i, child := range children {
		err := child.Wait()
		text := strings.TrimSpace(outputs[i].String())
		n, parseErr := strconv.Atoi(text)
		if err != nil || parseErr != nil {
			t.Fatalf("seed %d, process %d: %v\n%s\nreplay: %s", seed, i, err, text, replay)
		}
		removed += n
	}
	if removed == 0 {
		t.Fatalf("seed %d: no cap pass removed an entry, so no pass raced a build\nreplay: %s", seed, replay)
	}
}
