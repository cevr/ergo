package bindc

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The cost run (amendments B1 and B2): driver processes of costRounds
// rounds each; a round has costChunks slots, and in each slot every pair
// runs a chunk of costCalls/costChunks operations of its generated variant
// next to a chunk of its hand-written one. A chunk of 1,000 operations
// takes about 0.5 ms on the workbox. One observation is the median of a
// round's paired ratios; the decision is on the 95% interval of the median
// of the observations (the order-statistic interval benchstat uses).
const (
	costRounds       = 10
	costCalls        = 100000
	costChunks       = 100
	costMinProcesses = 3
	costMaxProcesses = 24
	// costBudget is the time budget of the measurement: the test adds
	// driver processes while a kind's interval contains costBar, until the
	// budget ends.
	costBudget = 150 * time.Second
	// costBar is amendment B1's parity line: 1x hand-written Go with 5% for
	// noise. A kind passes when the upper end of its interval is at most
	// costBar; an interval that still contains it at the end of the budget
	// fails.
	costBar = 1.05
)

// costExport gives the cost driver the generated thread-identity helper.
// The test adds it to the fixture package in the temporary module only.
const costExport = `package fixture

// CostThread is cbThread, for the cost driver.
func CostThread() uintptr { return cbThread() }
`

// costVariants are the cost driver's variants, in its order.
var costVariants = []string{"call_gen", "call_hand", "cb_gen", "cb_hand", "cb_bare", "call_guard"}

// costKinds are the driver's pairs: call and callback decide the test;
// callback_bare and guard are for information.
var costKinds = []string{"call", "callback", "callback_bare", "guard"}

// costRound is one round of one driver process.
type costRound struct {
	load string
	ns   map[string]float64 // ns/op by variant
	obs  map[string]float64 // the median paired ratio by kind
	n    map[string]int     // the number of paired ratios by kind
}

// parseCost reads the driver's round, pairs, allocs and thread lines.
func parseCost(out string) (rounds []costRound, allocs map[string]string, thread string, err error) {
	allocs = map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) == 4+2*len(costVariants) && f[0] == "round" && f[2] == "load":
			r := costRound{load: f[3], ns: map[string]float64{}, obs: map[string]float64{}, n: map[string]int{}}
			for i := 4; i < len(f); i += 2 {
				v, perr := strconv.ParseFloat(f[i+1], 64)
				if perr != nil || v <= 0 || f[i] != costVariants[(i-4)/2] {
					return nil, nil, "", fmt.Errorf("round line %q: %v", line, perr)
				}
				r.ns[f[i]] = v
			}
			rounds = append(rounds, r)
		case len(f) >= 4 && f[0] == "pairs":
			if len(rounds) == 0 || f[1] != strconv.Itoa(len(rounds)-1) || !slices.Contains(costKinds, f[2]) {
				return nil, nil, "", fmt.Errorf("pairs line %q out of place", line)
			}
			var xs []float64
			for _, s := range f[3:] {
				v, perr := strconv.ParseFloat(s, 64)
				if perr != nil || !(v > 0) {
					return nil, nil, "", fmt.Errorf("pairs line %q: %q", line, s)
				}
				xs = append(xs, v)
			}
			r := &rounds[len(rounds)-1]
			r.n[f[2]], r.obs[f[2]] = len(xs), median(xs)
		case len(f) == 3 && f[0] == "allocs":
			allocs[f[1]] = f[2]
		case len(f) == 2 && f[0] == "thread":
			thread = f[1]
		}
	}
	for i, r := range rounds {
		for _, k := range costKinds {
			if r.n[k] != costChunks {
				return nil, nil, "", fmt.Errorf("round %d has %d paired ratios of %s, want %d", i, r.n[k], k, costChunks)
			}
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

// binomLE is P(B <= k) for B ~ Binomial(n, 1/2).
func binomLE(n, k int) float64 {
	p, c := 0.0, 1.0 // c = C(n, i)
	for i := 0; i <= k; i++ {
		if i > 0 {
			c = c * float64(n-i+1) / float64(i)
		}
		p += c
	}
	return p / math.Pow(2, float64(n))
}

// medianCI is the distribution-free 95% interval of the median of xs (the
// order-statistic interval benchstat uses): the order statistics
// [x(j), x(n-j+1)] with the largest j whose coverage 1 - 2 P(B <= j-1),
// B ~ Binomial(n, 1/2), is at least 0.95. Below 6 samples no j reaches
// 0.95, and the interval is unbounded (too wide to decide).
func medianCI(xs []float64) (m, lo, hi float64) {
	s := slices.Clone(xs)
	m = median(s)
	n := len(s)
	j := 0
	for c := 1; 2*c <= n; c++ {
		if 1-2*binomLE(n, c-1) >= 0.95 {
			j = c
		}
	}
	if j == 0 {
		return m, math.Inf(-1), math.Inf(1)
	}
	return m, s[j-1], s[n-j]
}

// costDecision is the state of one kind after some processes.
type costDecision struct {
	m, lo, hi float64
	rounds    int
	decided   bool // hi <= costBar (pass) or lo > costBar (fail)
}

func decideCost(obs []float64) costDecision {
	m, lo, hi := medianCI(obs)
	return costDecision{m: m, lo: lo, hi: hi, rounds: len(obs), decided: hi <= costBar || lo > costBar}
}

// TestLiveCallbackCost (A8, A9, ERGO_BINDC_LIVE=1; amendments B1 and B2)
// drives the fixture (testdata/live/cost/main.go) in at least
// costMinProcesses processes. call: a typed call of the generated binding
// (fixture.NodeCount) against fx_node_count through a hand-written purego
// RegisterFunc with the same status check. callback: a C loop
// (fx_context_repeat) that calls fx_write_callback through the generated
// binding (typed func, registry, containment) against the same loop
// through a hand-written purego NewCallback that keeps the same contract
// (B1: a token key, a pending panic per thread raised again after C
// returns, Goexit ends the process). For information only: callback_bare,
// against a bare NewCallback with no key and no containment (the cost of
// the callback contract), and guard, the hand-written call with the guard
// that containment adds against the one without (the cost of that guard).
//
// Each pair's two chunks run next to each other, so one paired ratio sees
// one load; a round's observation is the median of its paired ratios. Per
// kind, the test computes the 95% interval of the median of all
// observations (medianCI). It adds processes while the interval of call or
// callback contains costBar, up to costBudget or costMaxProcesses. A kind
// passes when the upper end is at most costBar (1.05); at the end, an upper
// end above it fails, so an interval too wide to decide is a failure.
//
// The receipt cost-<goos>-<goarch>.txt (in $ERGO_BINDC_RECEIPTS) has the
// host, go version, target, the load before and after, the ns/op of each
// variant and the observation of each kind in each round, the allocations
// per operation, the cost of the thread-identity helper, and per kind the
// line "ratio <kind> median <m> ci95 <lo> <hi> rounds <n> processes <p>".
func TestLiveCallbackCost(t *testing.T) {
	e := newLiveEnv(t)
	e.write(filepath.Join(e.mod, "fixture", "costexport.go"), []byte(costExport))
	lib, driver := e.fixtureDriverFrom("testdata/live/cost/main.go")
	before := loadAverage()
	start := time.Now()
	var procs [][]costRound
	var allocs map[string]string
	var thread string
	obs := map[string][]float64{}
	var dec map[string]costDecision
	for {
		out, err := e.try(e.root, "", nil, driver, lib, strconv.Itoa(costRounds), strconv.Itoa(costCalls), strconv.Itoa(costChunks))
		if err != nil || !slices.Equal(okLines(out), []string{"cost measured"}) {
			t.Fatalf("the cost driver (process %d): %v\n%s", len(procs), err, out)
		}
		rounds, al, th, err := parseCost(out)
		if err != nil || len(rounds) != costRounds || th == "" || len(al) != len(costVariants) {
			t.Fatalf("the cost driver's output (process %d: %d rounds, thread %q, %d allocs lines, %v):\n%s", len(procs), len(rounds), th, len(al), err, out)
		}
		if allocs == nil {
			allocs, thread = al, th
		}
		procs = append(procs, rounds)
		for _, r := range rounds {
			for _, k := range costKinds {
				obs[k] = append(obs[k], r.obs[k])
			}
		}
		dec = map[string]costDecision{}
		for _, k := range costKinds {
			dec[k] = decideCost(obs[k])
		}
		if len(procs) < costMinProcesses {
			continue
		}
		if dec["call"].decided && dec["callback"].decided || time.Since(start) > costBudget || len(procs) >= costMaxProcesses {
			break
		}
	}
	spent := time.Since(start)
	after := loadAverage()

	var b strings.Builder
	host, _ := os.Hostname()
	ver, _ := e.try(e.root, "", nil, e.goTool, "version")
	fmt.Fprintf(&b, "bind-c-2 cost receipt (TestLiveCallbackCost, amendments B1 and B2)\n")
	fmt.Fprintf(&b, "host %s, %d CPUs\n%s", host, runtime.NumCPU(), ver)
	fmt.Fprintf(&b, "GOOS %s GOARCH %s, driver CGO_ENABLED=0, purego v0.11.3\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "load average before %s, after %s\n", before, after)
	fmt.Fprintf(&b, "method: processes of %d rounds; a round has %d slots; in each slot each pair runs a chunk of %d operations of its generated variant next to one of its hand-written variant (pair order rotates, first chunk alternates)\n",
		costRounds, costChunks, costCalls/costChunks)
	fmt.Fprintf(&b, "observation: the median of a round's %d paired ratios generated/hand-written; interval: the distribution-free 95%% interval of the median of the observations (order statistics, binomial coverage, as benchstat)\n", costChunks)
	fmt.Fprintf(&b, "decision: pass when ci95 hi <= %.2f; processes are added (at least %d, at most %d, budget %s) while the interval of call or callback contains %.2f; spent %s\n",
		costBar, costMinProcesses, costMaxProcesses, costBudget, costBar, spent.Round(time.Second))
	fmt.Fprintf(&b, "call_gen fixture.NodeCount; call_hand fx_node_count by purego.RegisterFunc, same status check\n")
	fmt.Fprintf(&b, "cb_gen fixture.ContextRepeat (trampoline, registry, containment); cb_hand purego.NewCallback with the same contract by hand; cb_bare purego.NewCallback, no key, no containment\n")
	fmt.Fprintf(&b, "call_guard call_hand with the guard containment adds to a typed call (the pending panic taken after C returns)\n")
	fmt.Fprintf(&b, "pairs: call (call_gen, call_hand), callback (cb_gen, cb_hand); for information callback_bare (cb_gen, cb_bare), guard (call_guard, call_hand)\n")
	fmt.Fprintf(&b, "round <process>.<round> load %s | %s (ns/op; observations; load is the 1 minute average at the round's start)\n",
		strings.Join(costVariants, " "), strings.Join(costKinds, " "))
	for p, rounds := range procs {
		for i, r := range rounds {
			fmt.Fprintf(&b, "round %d.%d %s", p, i, r.load)
			for _, v := range costVariants {
				fmt.Fprintf(&b, " %.1f", r.ns[v])
			}
			b.WriteString(" |")
			for _, k := range costKinds {
				fmt.Fprintf(&b, " %.4f", r.obs[k])
			}
			b.WriteString("\n")
		}
	}
	for _, v := range costVariants {
		fmt.Fprintf(&b, "allocs/op %s %s\n", v, allocs[v])
	}
	fmt.Fprintf(&b, "thread-identity helper (cbThread) %s ns/op\n", thread)
	for _, k := range costKinds {
		d := dec[k]
		prefix := "ratio"
		if k != "call" && k != "callback" {
			prefix = "info_ratio"
		}
		fmt.Fprintf(&b, "%s %s median %.4f ci95 %.4f %.4f rounds %d processes %d\n", prefix, k, d.m, d.lo, d.hi, d.rounds, len(procs))
	}
	for _, k := range []string{"call", "callback"} {
		verdict := "pass"
		if !(dec[k].hi <= costBar) {
			verdict = "fail"
		}
		fmt.Fprintf(&b, "verdict %s %s (ci95 hi %.4f, bar %.2f)\n", k, verdict, dec[k].hi, costBar)
	}
	receipt := b.String()
	t.Log(receipt)
	writeReceipt(t, fmt.Sprintf("cost-%s-%s.txt", runtime.GOOS, runtime.GOARCH), receipt)
	for _, k := range []string{"call", "callback"} {
		if d := dec[k]; !(d.hi <= costBar) {
			t.Errorf("ratio %s median %.4f ci95 %.4f %.4f (%d rounds, %d processes, %s): the upper end is above %.2f",
				k, d.m, d.lo, d.hi, d.rounds, len(procs), spent.Round(time.Second), costBar)
		}
	}
}

// TestCostMedianCI checks the interval against known cases: below 6
// samples it is unbounded; for 30 samples it is the 10th to the 21st order
// statistic (coverage 0.957; the 11th to the 20th covers 0.901).
func TestCostMedianCI(t *testing.T) {
	xs := make([]float64, 30)
	for i := range xs {
		xs[i] = float64(30 - i)
	}
	if m, lo, hi := medianCI(xs); m != 15.5 || lo != 10 || hi != 21 {
		t.Errorf("medianCI(1..30) = %v %v %v, want 15.5 10 21", m, lo, hi)
	}
	if _, lo, hi := medianCI([]float64{1, 2, 3, 4, 5}); !math.IsInf(lo, -1) || !math.IsInf(hi, 1) {
		t.Errorf("medianCI of 5 samples = %v %v, want unbounded", lo, hi)
	}
	if _, lo, hi := medianCI([]float64{1, 2, 3, 4, 5, 6}); lo != 1 || hi != 6 {
		t.Errorf("medianCI of 6 samples = %v %v, want 1 6 (coverage 0.969)", lo, hi)
	}
	if d := decideCost([]float64{1, 1, 1, 1, 1, 1.2}); d.decided {
		t.Errorf("decideCost: %+v, want undecided (the interval contains the bar)", d)
	}
	if d := decideCost([]float64{1, 1, 1, 1, 1, 1.04}); !d.decided || d.hi > costBar {
		t.Errorf("decideCost: %+v, want a pass", d)
	}
	if d := decideCost([]float64{1.1, 1.1, 1.1, 1.1, 1.1, 1.2}); !d.decided || d.lo <= costBar {
		t.Errorf("decideCost: %+v, want a fail", d)
	}
}
