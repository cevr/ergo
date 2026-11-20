// Command servercount writes the counted receipt of an HTTP service: the
// Ergo program, its hand Go control and its Go-runtime floor, each serving
// the same scripts of requests (docs/tooling.md, "Counted receipts").
// It is a tool, not a test: it pins no count, and its receipt is a
// bench.v1 file (internal/opcount).
//
// From the repository root:
//
//	go run ./scripts/servercount [-rounds N] [-conns N] [-procs N] [-kinds K,…] [-o FILE] [-ergo BIN] [-work DIR] [BENCH]
//
// BENCH (default benchmarks/server/registry) holds the program
// (cmd/NAME, NAME its last element), control/ (net/http and
// encoding/json) and floor/ (a raw HTTP/1.1 loop). servercount builds the
// three as scripts/opcount builds a program and its control, with
// opcount's exit hook and a probe that ends a server when its standard
// input ends (serve.go). Per route kind (registry.go, Kinds) it starts
// each side under a user-instruction counter that every thread of the
// server inherits (opcount.Count), replays the kind's script over -conns
// connections, checks every answer's status and body digest against the
// model, ends the server and reads its allocation and GC counts. It does
// so at the kind's two script lengths, for each side, sides alternating
// (A B C, then C B A) for N rounds, and writes per request the slope
// between the lengths: user instructions, bytes and objects allocated, and
// GC cycles per 1,000 requests. A wrong answer voids the run: the side's
// series and the case's comparisons are refused (NotEquivalent), and
// servercount exits 1, as it does for any refusal; 2 when it cannot run.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/opcount"
	"ergo.local/prototype/internal/proclimit"
)

func main() {
	code, err := run(os.Args[1:], os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "servercount: %v\n", err)
	}
	os.Exit(code)
}

// counters are a receipt's series per side, in order: user instructions,
// bytes and objects allocated per request, GC cycles per 1,000.
var counters = []struct {
	name, metric string
	kind         opcount.Kind
	scale        float64
}{
	{"Instructions", "", opcount.Counted, 1},
	{"AllocBytes", "alloc_bytes", opcount.Exact, 1},
	{"Allocs", "allocs", opcount.Exact, 1},
	{"GCCyclesPer1000", "gc_cycles", opcount.Exact, 1000},
}

func run(args []string, stdout, stderr io.Writer) (int, error) {
	flags := flag.NewFlagSet("servercount", flag.ContinueOnError)
	flags.SetOutput(stderr)
	rounds := flags.Int("rounds", 5, "rounds; each runs every side at both lengths, in alternating order")
	conns := flags.Int("conns", 4, "connections a script's main part is sent over at once")
	procs := flags.Int("procs", 1, "GOMAXPROCS of each server (0: the environment's); 1 keeps idle processors from spinning into a count")
	kinds := flags.String("kinds", strings.Join(Kinds, ","), "the route kinds to count")
	out := flags.String("o", "", "write the bench.v1 receipt to this file (default: stdout)")
	ergo := flags.String("ergo", "", "the ergo binary that builds the program (default: go build ./cmd/ergo)")
	work := flags.String("work", "", "scratch folder, kept (default: a new temporary folder, removed)")
	memory := flags.String("memory", "4GiB", "memory limit of each server and build the kernel enforces")
	if err := flags.Parse(args); err != nil {
		return 2, err
	}
	limit, err := proclimit.ParseSize(*memory)
	if err != nil {
		return 2, fmt.Errorf("-memory %q: %v", *memory, err)
	}
	bench := "benchmarks/server/registry"
	if flags.NArg() == 1 {
		bench = flags.Arg(0)
	} else if flags.NArg() > 1 {
		return 2, errors.New("usage: servercount [flags] [BENCH]")
	}
	for _, k := range strings.Split(*kinds, ",") {
		if !slices.Contains(Kinds, k) {
			return 2, fmt.Errorf("-kinds: no route kind %q (the kinds: %s)", k, strings.Join(Kinds, ", "))
		}
	}
	root, err := os.Getwd()
	if err != nil {
		return 2, err
	}
	if *work == "" {
		dir, err := os.MkdirTemp("", "servercount-")
		if err != nil {
			return 2, err
		}
		defer os.RemoveAll(dir)
		*work = dir
	} else if err := os.MkdirAll(*work, 0o755); err != nil {
		return 2, err
	}
	b, err := newBuilder(root, *work, *ergo, limit, stderr)
	if err != nil {
		return 2, err
	}
	sides, err := b.sides(bench)
	if err != nil {
		return 2, err
	}
	receipt := opcount.Receipt{Schema: opcount.Schema, Tool: "servercount", Commit: commit(), Host: host(b.release.Go), Priority: priority()}
	receipt.Load[0] = loadavg()
	for _, kind := range strings.Split(*kinds, ",") {
		c, err := measure(sides, bench, kind, *rounds, *conns, *procs, *work, limit, stderr)
		if err != nil {
			return 2, err
		}
		receipt.Cases = append(receipt.Cases, c)
	}
	receipt.Load[1] = loadavg()
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return 2, err
	}
	data = append(data, '\n')
	if *out == "" {
		stdout.Write(data)
	} else if err := os.WriteFile(*out, data, 0o644); err != nil {
		return 2, err
	}
	if refused := report(receipt, stderr); refused > 0 {
		fmt.Fprintf(stderr, "servercount: %d refused; a refused series or comparison has no number\n", refused)
		return 1, nil
	}
	return 0, nil
}

// measure is the case of one route kind: every side at the kind's two
// lengths for rounds rounds, and the program compared with the control
// and with the floor.
func measure(sides []side, bench, kind string, rounds, conns, procs int, work string, limit uint64, log io.Writer) (opcount.Case, error) {
	small, large := sizes(kind)
	scripts := []Script{KindScript(kind, small, conns), KindScript(kind, large, conns)}
	c := opcount.Case{Case: kind, Unit: "request", Sizes: []int{small, large}, Rounds: rounds, Workload: &opcount.Workload{Unit: "request", Args: []string{"-conns", strconv.Itoa(conns), "-kinds", kind}, Procs: procs, Control: bench + "/control", Floor: bench + "/floor"}}
	values := map[string]map[string][]float64{}
	refused := map[string]*opcount.Refused{}
	for r := range rounds {
		order := slices.Clone(sides)
		if r%2 == 1 {
			slices.Reverse(order)
		}
		for _, s := range order {
			if refused[s.name] != nil {
				continue
			}
			var runs [2]served
			for i, script := range scripts {
				got, err := serve(s.bin, work, fmt.Sprintf("%s-%s-%d-%d", kind, s.name, r, i), script, limit, procs)
				if err != nil {
					return c, err
				}
				switch {
				case got.wrong != nil:
					refused[s.name] = &opcount.Refused{ID: opcount.NotEquivalent, Reason: got.wrong.Error()}
				case got.run.Incomplete != "":
					refused[s.name] = &opcount.Refused{ID: opcount.RunIncomplete, Reason: got.run.Incomplete}
				case got.run.Uncounted != "":
					refused[s.name] = &opcount.Refused{ID: got.run.Uncounted, Reason: got.run.Why}
				}
				runs[i] = got
			}
			if refused[s.name] != nil {
				fmt.Fprintf(log, "servercount: %s %s: %s\n", kind, s.name, refused[s.name].Reason)
				continue
			}
			if values[s.name] == nil {
				values[s.name] = map[string][]float64{}
			}
			for _, k := range counters {
				a, b := float64(runs[0].run.Instructions), float64(runs[1].run.Instructions)
				if k.metric != "" {
					var okA, okB bool
					a, okA = runs[0].metrics[k.metric]
					b, okB = runs[1].metrics[k.metric]
					if !okA || !okB {
						refused[s.name] = &opcount.Refused{ID: opcount.NoMetric, Reason: "the exit hook reported no " + k.metric}
						break
					}
				}
				values[s.name][k.name] = append(values[s.name][k.name], k.scale*opcount.Slope(a, b, small, large))
			}
			fmt.Fprintf(log, "servercount: %s %s round %d: %.0f instructions per request\n", kind, s.name, r+1, values[s.name]["Instructions"][len(values[s.name]["Instructions"])-1])
		}
	}
	for _, s := range sides {
		for _, k := range counters {
			c.Series = append(c.Series, opcount.NewSeries(s.name, k.name, k.kind, true, values[s.name][k.name], refused[s.name]))
		}
	}
	at := func(sideName, counter string) opcount.Series {
		for _, s := range c.Series {
			if s.Side == sideName && s.Counter == counter {
				return s
			}
		}
		panic("servercount: no series " + sideName + " " + counter)
	}
	for _, base := range []string{"control", "floor"} {
		for _, k := range counters {
			c.Comparisons = append(c.Comparisons, opcount.Compare(at(base, k.name), at("program", k.name)))
		}
	}
	for _, s := range sides {
		if r := refused[s.name]; r != nil && r.ID == opcount.NotEquivalent {
			c.Refuse(*r)
		}
	}
	return c, nil
}

// report prints each case's estimates per side and counts the refusals.
func report(r opcount.Receipt, w io.Writer) int {
	refused := 0
	for _, c := range r.Cases {
		fmt.Fprintf(w, "%s (per %s, %d rounds)\n", c.Case, c.Unit, c.Rounds)
		for _, s := range c.Series {
			if s.Refusal != nil {
				refused++
				fmt.Fprintf(w, "  %-8s %-16s refused %s: %s\n", s.Side, s.Counter, s.Refusal.ID, s.Refusal.Reason)
				continue
			}
			fmt.Fprintf(w, "  %-8s %-16s %14.1f  spread %.3f\n", s.Side, s.Counter, *s.Estimate, *s.Spread)
		}
		for _, cmp := range c.Comparisons {
			switch {
			case cmp.Verdict != nil:
				refused++
			case cmp.Ratio != nil:
				fmt.Fprintf(w, "  %s/%s %-16s %.3f\n", cmp.Head, cmp.Base, cmp.Counter, *cmp.Ratio)
			}
		}
	}
	return refused
}

func commit() string {
	head, err := exec.Command("git", "--no-optional-locks", "rev-parse", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	c := strings.TrimSpace(string(head))
	if status, err := exec.Command("git", "--no-optional-locks", "status", "--porcelain").Output(); err == nil && len(bytes.TrimSpace(status)) > 0 {
		c += "+dirty"
	}
	return c
}

// host is the receipt's host: the go version is the release the sides
// were built with.
func host(goBin string) opcount.Host {
	h := opcount.Host{OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(), GoVersion: runtime.Version(), Counter: "user instructions"}
	if out, err := exec.Command(goBin, "env", "GOVERSION").Output(); err == nil {
		h.GoVersion = strings.TrimSpace(string(out))
	}
	if err := opcount.Available(); err != nil {
		h.Counter = err.Error()
	}
	return h
}

func loadavg() string {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(data))
}

// priority is this process's nice value (field 19 of /proc/self/stat).
func priority() int {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0
	}
	s := string(data)
	if i := strings.LastIndexByte(s, ')'); i >= 0 {
		if fields := strings.Fields(s[i+1:]); len(fields) > 16 {
			n, _ := strconv.Atoi(fields[16])
			return n
		}
	}
	return 0
}
