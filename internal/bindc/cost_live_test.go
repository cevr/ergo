package bindc

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The cost run: costRounds rounds of costCalls operations per variant, in
// costChunks chunks per round that interleave the variants, and the bar on
// the median of the per-round ratios generated/hand-written of each kind.
// A chunk of 1,000 operations takes about 0.5 ms on the workbox, so a
// change of host load falls on every variant alike.
const (
	costRounds = 21
	costCalls  = 100000
	costChunks = 100
	costBar    = 1.25
	// costParity is amendment B1's parity line (1x hand-written Go with 5%
	// for noise). The receipt states each kind against it; the test fails
	// only above costBar.
	costParity = 1.05
)

// costExport gives the cost driver the generated thread-identity helper.
// The test adds it to the fixture package in the temporary module only.
const costExport = `package fixture

// CostThread is cbThread, for the cost driver.
func CostThread() uintptr { return cbThread() }
`

// costRound is one round of the cost driver: ns/op by variant.
type costRound struct {
	load string
	ns   map[string]float64
}

// costVariants are the cost driver's variants, in its order.
var costVariants = []string{"call_gen", "call_hand", "cb_gen", "cb_hand", "cb_bare", "call_guard"}

// parseCost reads the driver's round, allocs and thread lines.
func parseCost(out string) (rounds []costRound, allocs map[string]string, thread string, err error) {
	allocs = map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) == 4+2*len(costVariants) && f[0] == "round" && f[2] == "load":
			r := costRound{load: f[3], ns: map[string]float64{}}
			for i := 4; i < len(f); i += 2 {
				v, perr := strconv.ParseFloat(f[i+1], 64)
				if perr != nil || v <= 0 || f[i] != costVariants[(i-4)/2] {
					return nil, nil, "", fmt.Errorf("round line %q: %v", line, perr)
				}
				r.ns[f[i]] = v
			}
			rounds = append(rounds, r)
		case len(f) == 3 && f[0] == "allocs":
			allocs[f[1]] = f[2]
		case len(f) == 2 && f[0] == "thread":
			thread = f[1]
		}
	}
	return rounds, allocs, thread, nil
}

// median of xs (xs is sorted in place).
func median(xs []float64) float64 {
	slices.Sort(xs)
	n := len(xs)
	if n%2 == 1 {
		return xs[n/2]
	}
	return (xs[n/2-1] + xs[n/2]) / 2
}

// TestLiveCallbackCost (A8, A9, ERGO_BINDC_LIVE=1) drives the fixture in one
// process (testdata/live/cost/main.go): costRounds interleaved rounds of
// costCalls operations per variant. call: a typed call of the generated
// binding (fixture.NodeCount) against fx_node_count through a hand-written
// purego RegisterFunc with the same status check. callback: a C loop
// (fx_context_repeat) that calls fx_write_callback through the generated
// binding (typed func, registry, containment) against the same loop
// through a hand-written purego NewCallback that keeps the same contract
// (amendment B1: a token key, a pending panic per thread raised again after
// C returns, Goexit ends the process). For information only: a bare
// NewCallback with no key and no containment (the cost of the callback
// contract), and the hand-written call with the guard that containment
// adds to a typed call (the cost of that guard). The receipt cost-<goos>-<goarch>.txt (in $ERGO_BINDC_RECEIPTS) has
// the host, go version, target, load, the ns/op of each variant in each
// round, the allocations per operation, the cost of the thread-identity
// helper, the spread of the per-round ratios and the lines "median_ratio
// call <r>" and "median_ratio callback <r>". The test fails when a median
// ratio is above costBar (1.25).
func TestLiveCallbackCost(t *testing.T) {
	e := newLiveEnv(t)
	e.write(filepath.Join(e.mod, "fixture", "costexport.go"), []byte(costExport))
	lib, driver := e.fixtureDriverFrom("testdata/live/cost/main.go")
	before := loadAverage()
	out, err := e.try(e.root, "", nil, driver, lib, strconv.Itoa(costRounds), strconv.Itoa(costCalls), strconv.Itoa(costChunks))
	after := loadAverage()
	if err != nil || !slices.Equal(okLines(out), []string{"cost measured"}) {
		t.Fatalf("the cost driver: %v\n%s", err, out)
	}
	rounds, allocs, thread, err := parseCost(out)
	if err != nil || len(rounds) != costRounds || thread == "" || len(allocs) != len(costVariants) {
		t.Fatalf("the cost driver's output (%d rounds, thread %q, %d allocs lines, %v):\n%s", len(rounds), thread, len(allocs), err, out)
	}

	type kind struct{ name, gen, hand string }
	kinds := []kind{
		{"call", "call_gen", "call_hand"}, {"callback", "cb_gen", "cb_hand"},
		{"callback_bare", "cb_gen", "cb_bare"}, {"call_guarded", "call_gen", "call_guard"}, {"guard", "call_guard", "call_hand"},
	}
	ratios := map[string][]float64{}
	var b strings.Builder
	host, _ := os.Hostname()
	ver, _ := e.try(e.root, "", nil, e.goTool, "version")
	fmt.Fprintf(&b, "bind-c-2 cost receipt (TestLiveCallbackCost)\n")
	fmt.Fprintf(&b, "host %s, %d CPUs\n%s", host, runtime.NumCPU(), ver)
	fmt.Fprintf(&b, "GOOS %s GOARCH %s, driver CGO_ENABLED=0, purego v0.11.3\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "load average before %s, after %s\n", before, after)
	fmt.Fprintf(&b, "%d rounds of %d operations per variant, interleaved in %d chunks per round (rotating order)\n", costRounds, costCalls, costChunks)
	fmt.Fprintf(&b, "call_gen fixture.NodeCount; call_hand fx_node_count by purego.RegisterFunc, same status check\n")
	fmt.Fprintf(&b, "cb_gen fixture.ContextRepeat (trampoline, registry, containment); cb_hand purego.NewCallback with the same contract by hand; cb_bare purego.NewCallback, no key, no containment\n")
	fmt.Fprintf(&b, "call_guard call_hand with the guard containment adds to a typed call (LockOSThread, the pending panic taken after C returns)\n")
	fmt.Fprintf(&b, "round load %s (ns/op; load is the 1 minute average at the round's start)\n", strings.Join(costVariants, " "))
	for i, r := range rounds {
		fmt.Fprintf(&b, "round %d %s", i, r.load)
		for _, v := range costVariants {
			fmt.Fprintf(&b, " %.1f", r.ns[v])
		}
		b.WriteString("\n")
		for _, k := range kinds {
			ratios[k.name] = append(ratios[k.name], r.ns[k.gen]/r.ns[k.hand])
		}
	}
	for _, v := range costVariants {
		fmt.Fprintf(&b, "allocs/op %s %s\n", v, allocs[v])
	}
	fmt.Fprintf(&b, "thread-identity helper (cbThread) %s ns/op\n", thread)
	meds := map[string]float64{}
	for _, k := range kinds {
		rs := slices.Clone(ratios[k.name])
		m := median(rs)
		meds[k.name] = m
		q1, q3 := rs[len(rs)/4], rs[(3*len(rs))/4]
		fmt.Fprintf(&b, "spread %s min %.3f q1 %.3f median %.3f q3 %.3f max %.3f\n", k.name, rs[0], q1, m, q3, rs[len(rs)-1])
	}
	fmt.Fprintf(&b, "info_ratio callback_bare %.3f (generated against a bare callback with no containment: the cost of the contract)\n", meds["callback_bare"])
	fmt.Fprintf(&b, "info_ratio call_guarded %.3f (generated against the hand-written call with the containment guard)\n", meds["call_guarded"])
	fmt.Fprintf(&b, "info_ratio guard %.3f (the hand-written call with the guard against the one without: the cost of the guard)\n", meds["guard"])
	for _, k := range []string{"call", "callback"} {
		fmt.Fprintf(&b, "parity %s %.3f %s %.2f (amendment B1)\n", k, meds[k], map[bool]string{true: "<=", false: ">"}[meds[k] <= costParity], costParity)
	}
	fmt.Fprintf(&b, "bar %.2f (TestLiveCallbackCost fails above it)\n", costBar)
	fmt.Fprintf(&b, "median_ratio call %.3f\nmedian_ratio callback %.3f\n", meds["call"], meds["callback"])
	receipt := b.String()
	t.Log(receipt)
	writeReceipt(t, fmt.Sprintf("cost-%s-%s.txt", runtime.GOOS, runtime.GOARCH), receipt)
	for _, k := range []string{"call", "callback"} {
		if m := meds[k]; !(m > 0 && m <= costBar) {
			t.Errorf("median_ratio %s %.3f is not in (0, %.2f]", k, m, costBar)
		}
	}
}
