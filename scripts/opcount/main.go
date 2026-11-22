// Command opcount writes the counted receipt of an example program or of a
// command (docs/tooling.md, "Counted receipts"). It is a tool, not a test:
// it pins no count, and its receipt is a bench.v1 file (internal/opcount).
//
// Program mode, from the repository root:
//
//	go run ./scripts/opcount [-control] [-text] [-rounds N] [-o FILE] [-ergo BIN] NAME
//
// reads examples/NAME/workload (or FOLDER/workload for a NAME with a slash,
// such as benchmarks/forms/step, whose program is the nearest folder from
// it up that holds ergo.mod), builds the program with the exit hook
// (ergo build -work, then ergo's go build action on the emitted module
// with an -overlay, in a scratch folder) and its hand Go control with
// -control, all with the release ergo builds with and its patched compile.
// The program builds as the product does: each package's IR bodies read by
// the patched compile (-ergoir); the receipt names the path and, per
// package, how many function bodies came from IR. -text builds the same IR
// bodies printed as Go text (ergo build -go-text) instead, for
// comparison. It runs each at
// the workload's two sizes, with the default collector and with GOGC=off,
// the sides alternating (A B, then B A) for N rounds. Per side it writes
// user instructions per unit (and with GOGC=off), allocations and bytes
// per unit, the GC share of instructions ((default - off) / default), GC
// cycles, the runtime's GC CPU share, the live heap after a final
// collection and the peak resident set (VmHWM) at the large size, and the
// parts of the instructions per unit (internal/opcount, share.go: the
// program, Ergo's runtime, Go's collector, allocation, barriers,
// scheduler, stacks and the rest of its runtime), with Go's runtime share
// of them and the samples they rest on. With
// -control the control must print what the program prints on the small
// input, or every comparison is refused (NotEquivalent). -control also
// measures the workload's floor, the hand-optimized Go-runtime floor, when
// it names one, under the same rule, and compares the program with each.
//
// Command mode compares two commands on each target:
//
//	go run ./scripts/opcount -cmd -base 'BIN' -head 'BIN' [-each GLOB] [-rounds N] -- ARGS…
//
// runs `BIN ARGS…` with {} in ARGS replaced by each target the glob
// matches, the two binaries alternating, and writes user instructions, CPU
// time, peak resident set and the parts per side and target. It replaces run-dir
// check-cpu.sh, which timed at 10 ms and read a failed `ergo check` as a
// fast one (`|| true`): here a run that does not exit 0 is RunIncomplete.
//
// Every run is a fresh process, bounded by -memory, in a process group of
// its own (internal/opcount.Count). opcount exits 1 when any series or
// comparison is refused, and 2 when it cannot run.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/ergobuild"
	"ergo.local/prototype/internal/opcount"
	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/toolchain"
)

func main() {
	code, err := run(os.Args[1:], os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "opcount:", err)
	}
	os.Exit(code)
}

type options struct {
	control, cmd     bool
	text             bool
	rounds           int
	out, ergo, work  string
	base, head, each string
	memory           uint64
	// profile is -profile FILE: write the program's CPU profile (and
	// FILE.control, FILE.floor with -control) and count nothing; pgo is
	// -pgo FILE: build the program with go build -pgo=FILE (each reference
	// side with FILE.SIDE when it exists) and count (U4 PGO-RELEASE).
	profile, pgo string
}

func run(args []string, stdout, stderr io.Writer) (int, error) {
	var o options
	var memory string
	flags := flag.NewFlagSet("opcount", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.BoolVar(&o.control, "control", false, "also measure the workload's hand Go control and Go-runtime floor (each it names), and compare")
	flags.BoolVar(&o.text, "text", false, "build the program's IR bodies as Go text (ergo build -go-text), for comparison with the IR build")
	flags.BoolVar(&o.cmd, "cmd", false, "command mode: compare -base and -head on each target")
	flags.IntVar(&o.rounds, "rounds", 3, "rounds; each runs every side once, in alternating order")
	flags.StringVar(&o.out, "o", "", "write the bench.v1 receipt to this file (default: stdout)")
	flags.StringVar(&o.ergo, "ergo", "", "the ergo binary that builds the program (default: go build ./cmd/ergo)")
	flags.StringVar(&o.work, "work", "", "scratch folder, kept (default: a new temporary folder, removed)")
	flags.StringVar(&o.base, "base", "", "command mode: the base binary")
	flags.StringVar(&o.head, "head", "", "command mode: the head binary")
	flags.StringVar(&o.each, "each", "", "command mode: a glob of targets; {} in ARGS is each")
	flags.StringVar(&memory, "memory", "4GiB", "memory limit of each program the kernel enforces")
	flags.StringVar(&o.profile, "profile", "", "write the CPU profile of -rounds runs of the program at the large size to `FILE` (each reference side's to FILE.SIDE with -control), merged, and count nothing")
	flags.StringVar(&o.pgo, "pgo", "", "build the program with go build -pgo=`FILE`, and each reference side with FILE.SIDE when it exists")
	if err := flags.Parse(args); err != nil {
		return 2, nil
	}
	for _, p := range []*string{&o.profile, &o.pgo} {
		if *p != "" {
			abs, err := filepath.Abs(*p)
			if err != nil {
				return 2, err
			}
			*p = abs
		}
	}
	if o.profile != "" && o.pgo != "" {
		return 2, errors.New("-profile and -pgo: profile a build without a profile")
	}
	limit, err := proclimit.ParseSize(memory)
	if err != nil {
		return 2, fmt.Errorf("-memory %q: %v", memory, err)
	}
	o.memory = limit
	if o.rounds < 1 {
		return 2, errors.New("-rounds must be at least 1")
	}
	if o.work == "" {
		dir, err := os.MkdirTemp("", "opcount-")
		if err != nil {
			return 2, err
		}
		defer os.RemoveAll(dir)
		o.work = dir
	} else if err := os.MkdirAll(o.work, 0o755); err != nil {
		return 2, err
	}
	receipt := opcount.Receipt{Schema: opcount.Schema, Tool: "opcount", Commit: commit(), Host: host(), Priority: priority()}
	receipt.Load[0] = loadavg()
	if o.cmd {
		if o.base == "" || o.head == "" || flags.NArg() == 0 {
			return 2, errors.New("-cmd needs -base, -head and the command's arguments after --")
		}
		cases, err := commandCases(o, flags.Args(), stderr)
		if err != nil {
			return 2, err
		}
		receipt.Cases = cases
	} else {
		if flags.NArg() != 1 {
			return 2, errors.New("usage: opcount [-control] [-rounds N] [-o FILE] NAME (an examples/ folder) or FOLDER, or opcount -cmd …")
		}
		c, err := programCase(o, flags.Arg(0), stderr)
		if err != nil {
			return 2, err
		}
		if o.profile != "" {
			return 0, nil // the profiles are the output (measurer.profiles)
		}
		receipt.Cases = []opcount.Case{c}
	}
	receipt.Load[1] = loadavg()
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return 2, err
	}
	data = append(data, '\n')
	if o.out == "" {
		stdout.Write(data)
	} else if err := os.WriteFile(o.out, data, 0o644); err != nil {
		return 2, err
	}
	refused := report(receipt, stderr)
	if refused > 0 {
		fmt.Fprintf(stderr, "opcount: %d refused; a refused series or comparison has no number\n", refused)
		return 1, nil
	}
	return 0, nil
}

// report prints the receipt as a table and counts its refusals.
func report(r opcount.Receipt, w io.Writer) int {
	refused := 0
	fmt.Fprintf(w, "load %s -> %s, nice %d, %s\n", r.Load[0], r.Load[1], r.Priority, r.Host.Counter)
	for _, c := range r.Cases {
		unit := ""
		if c.Unit != "" {
			unit = " per " + c.Unit
		}
		fmt.Fprintf(w, "== %s (%d rounds%s)\n", c.Case, c.Rounds, map[bool]string{true: ", sizes " + fmt.Sprint(c.Sizes), false: ""}[len(c.Sizes) > 0])
		if c.Path != "" {
			fmt.Fprintf(w, "  path %s\n", c.Path)
		}
		for _, p := range c.Bodies {
			fmt.Fprintf(w, "  IR bodies %-28s %d of %d functions\n", p.Package, p.IR, p.Funcs)
		}
		for _, s := range c.Series {
			if s.Refusal != nil {
				refused++
				fmt.Fprintf(w, "  %-8s %-18s refused %s: %s\n", s.Side, s.Counter, s.Refusal.ID, s.Refusal.Reason)
				continue
			}
			per := ""
			if s.PerUnit {
				per = unit
			}
			fmt.Fprintf(w, "  %-8s %-18s %.2f%s (spread %.1f%%, %s)\n", s.Side, s.Counter, *s.Estimate, per, 100**s.Spread, s.Kind)
		}
		for _, cmp := range c.Comparisons {
			switch {
			case cmp.Verdict != nil:
				refused++
				fmt.Fprintf(w, "  %s/%s %-18s refused %s: %s\n", cmp.Head, cmp.Base, cmp.Counter, cmp.Verdict.Refusal.ID, cmp.Verdict.Refusal.Reason)
			case cmp.Ratio != nil:
				fmt.Fprintf(w, "  %s/%s %-18s %.3f (median of %d ratios; no verdict)\n", cmp.Head, cmp.Base, cmp.Counter, *cmp.Ratio, cmp.Pairs)
			}
		}
	}
	return refused
}

// side is one measured binary.
type side struct {
	name, bin string
}

// sample is what one round measured of one side: per counter a value or a
// refusal.
type sample map[string]result

type result struct {
	value   float64
	refused *opcount.Refused
}

type counter struct {
	name    string
	kind    opcount.Kind
	perUnit bool
}

// counters is the catalogue of program mode: name, kind, per unit. The
// parts' counters (opcount.Parts) and RuntimeShare follow the hook's.
var counters = append([]counter{
	{"Instructions", opcount.Counted, true},
	{"InstructionsGCOff", opcount.Counted, true},
	{"GCShare", opcount.Counted, false},
	{"Allocs", opcount.Exact, true},
	{"AllocBytes", opcount.Exact, true},
	{"GCCycles", opcount.Exact, false},
	{"GCCPUShare", opcount.Timed, false},
	{"LiveHeapBytes", opcount.Timed, false},
	{"PeakRSSKiB", opcount.Timed, false},
}, partCounters(true)...)

// partCounters are the parts' instructions (per unit in program mode),
// Go's runtime share of them, and the samples the large run took.
func partCounters(perUnit bool) []counter {
	var out []counter
	for _, p := range opcount.Parts {
		out = append(out, counter{p.Counter(), opcount.Sampled, perUnit})
	}
	return append(out, counter{"RuntimeShare", opcount.Sampled, false}, counter{"Samples", opcount.Exact, false})
}

// parts sets the parts' counters of out from the runs at the two sizes
// (small is nil in command mode, where a part is the run's whole): each
// part's instructions, per unit between the sizes; RuntimeShare, the five
// runtime parts' sum over all parts'; and Samples, the large run's. A run
// whose parts are refused refuses them all.
func parts(out sample, small *opcount.Shares, large opcount.Shares, smallN, largeN int) {
	runs := []opcount.Shares{large}
	if small != nil {
		runs = append(runs, *small)
	}
	for _, s := range runs {
		if s.Refused != "" {
			for _, k := range partCounters(false) {
				out[k.name] = result{refused: &opcount.Refused{ID: s.Refused, Reason: s.Why}}
			}
			return
		}
	}
	var all, rt float64
	for _, p := range opcount.Parts {
		v := large.Instructions(p)
		if small != nil {
			v = opcount.Slope(small.Instructions(p), v, smallN, largeN)
		}
		out[p.Counter()] = result{value: v}
		all += v
		if slices.Contains(opcount.RuntimeParts, p) {
			rt += v
		}
	}
	out["RuntimeShare"] = result{value: rt / all}
	out["Samples"] = result{value: float64(large.Total())}
}

// programCase measures examples/NAME, or the folder NAME when it holds a
// slash (benchmarks/forms/step): its workload file, and the program of
// the nearest folder from it up that holds ergo.mod (one module, a
// workload per kind).
func programCase(o options, name string, log io.Writer) (opcount.Case, error) {
	root, err := os.Getwd()
	if err != nil {
		return opcount.Case{}, err
	}
	rel := "examples/" + name
	if strings.Contains(name, "/") {
		rel = filepath.ToSlash(filepath.Clean(name))
	}
	dir := filepath.Join(root, filepath.FromSlash(rel))
	module := dir
	for module != root {
		if _, err := os.Stat(filepath.Join(module, "ergo.mod")); err == nil {
			break
		}
		module = filepath.Dir(module)
	}
	if module == root {
		module = dir
	}
	w, problems, ok := opcount.CheckIn(root, dir, module)
	if !ok {
		return opcount.Case{}, fmt.Errorf("%s has no %s file (run from the repository root)", rel, opcount.WorkloadFile)
	}
	file, err := filepath.Rel(root, opcount.Path(dir))
	if err != nil {
		return opcount.Case{}, err
	}
	file = filepath.ToSlash(file)
	if len(problems) > 0 {
		var lines []string
		for _, p := range problems {
			lines = append(lines, fmt.Sprintf("%s: %s", file, p))
		}
		return opcount.Case{}, errors.New(strings.Join(lines, "\n"))
	}
	if o.control && w.Control == "" && w.Floor == "" {
		return opcount.Case{}, fmt.Errorf("%s names no control and no floor", file)
	}
	release, err := patchedRelease(log)
	if err != nil {
		return opcount.Case{}, err
	}
	b := builder{o: o, root: root, log: log, release: release}
	ergoSide, err := b.program(filepath.Join(module, "cmd", w.Binary), w.Binary)
	if err != nil {
		return opcount.Case{}, err
	}
	program, goLine := ergoSide.bin, ergoSide.goLine
	sides := []side{{"ergo", program}}
	if o.control {
		// The hand Go control and the Go-runtime floor, each the workload names.
		for _, ref := range []struct{ name, dir string }{{"control", w.Control}, {"floor", w.Floor}} {
			if ref.dir == "" {
				continue
			}
			bin, err := b.goPackage(ref.dir, ref.name, goLine, true)
			if err != nil {
				return opcount.Case{}, err
			}
			sides = append(sides, side{ref.name, bin})
		}
	}
	inputs := map[int]string{}
	if w.InputKind != "" {
		gen := program
		if w.InputKind == "go" {
			if gen, err = b.goPackage(w.InputDir, "gen", goLine, false); err != nil {
				return opcount.Case{}, err
			}
		}
		for _, n := range []int{w.Small, w.Large} {
			path := filepath.Join(o.work, "input-"+strconv.Itoa(n))
			if err := b.generate(gen, opcount.Expand(w.InputArgs, n, ""), path); err != nil {
				return opcount.Case{}, err
			}
			inputs[n] = path
		}
	}
	m := measurer{o: o, w: w, inputs: inputs}
	if o.profile != "" {
		return opcount.Case{}, m.profiles(b, sides)
	}
	c := opcount.Case{Case: name, Unit: w.Unit, Sizes: []int{w.Small, w.Large}, Rounds: o.rounds, Workload: &w, Path: "ir", Bodies: ergoSide.bodies}
	if o.text {
		c.Path = "text"
	}
	var equivalence *opcount.Refused
	if o.control {
		equivalence, err = m.agree(sides)
		if err != nil {
			return opcount.Case{}, err
		}
	}
	rounds := make(map[string][]sample)
	for r := range o.rounds {
		order := sides
		if r%2 == 1 {
			order = reversed(sides)
		}
		for _, s := range order {
			fmt.Fprintf(log, "round %d: %s\n", r+1, s.name)
			got, err := m.round(s)
			if err != nil {
				return opcount.Case{}, err
			}
			rounds[s.name] = append(rounds[s.name], got)
		}
	}
	for _, s := range sides {
		for _, k := range counters {
			c.Series = append(c.Series, series(s.name, k.name, k.kind, k.perUnit, rounds[s.name]))
		}
	}
	if o.control {
		// The program against each reference side: ergo/control, ergo/floor.
		for k := 1; k < len(sides); k++ {
			for i := range counters {
				c.Comparisons = append(c.Comparisons, opcount.Compare(c.Series[k*len(counters)+i], c.Series[i]))
			}
		}
		if equivalence != nil {
			c.Refuse(*equivalence)
		}
	}
	return c, nil
}

// series gathers one counter of one side over the rounds: its values, or
// the first refusal of any round.
func series(sideName, counter string, kind opcount.Kind, perUnit bool, rounds []sample) opcount.Series {
	var values []float64
	for _, s := range rounds {
		got, ok := s[counter]
		if !ok {
			return opcount.NewSeries(sideName, counter, kind, perUnit, nil, &opcount.Refused{ID: opcount.NoMetric, Reason: "the run reported no " + counter})
		}
		if got.refused != nil {
			return opcount.NewSeries(sideName, counter, kind, perUnit, nil, got.refused)
		}
		values = append(values, got.value)
	}
	return opcount.NewSeries(sideName, counter, kind, perUnit, values, nil)
}

func reversed(s []side) []side {
	out := make([]side, len(s))
	for i := range s {
		out[i] = s[len(s)-1-i]
	}
	return out
}

// measurer runs one side of a workload.
type measurer struct {
	o      options
	w      opcount.Workload
	inputs map[int]string
}

// one is a counted run of bin at size n; with metrics it also reads the
// exit hook's lines.
type one struct {
	run     opcount.Run
	shares  opcount.Shares
	metrics map[string]float64
}

func (m measurer) command(bin string, n int, stdout, stderr *os.File) (*exec.Cmd, func(), error) {
	cmd := exec.Command(bin, opcount.Expand(m.w.Args, n, m.inputs[n])...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Env = append(os.Environ(), "GOMAXPROCS="+strconv.Itoa(m.w.Procs), "GODEBUG=asyncpreemptoff=1")
	if m.w.FromStdin() {
		f, err := os.Open(m.inputs[n])
		if err != nil {
			return nil, nil, err
		}
		cmd.Stdin = f
		return cmd, func() { f.Close() }, nil
	}
	return cmd, func() {}, nil
}

func (m measurer) once(bin string, n int, gcOff bool) (one, error) {
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return one{}, err
	}
	defer null.Close()
	cmd, done, err := m.command(bin, n, null, null)
	if err != nil {
		return one{}, err
	}
	defer done()
	out := filepath.Join(m.o.work, "metrics.txt")
	os.Remove(out)
	if gcOff {
		cmd.Env = append(cmd.Env, "GOGC=off")
	} else {
		cmd.Env = append(cmd.Env, opcount.HookVariable+"="+out)
	}
	if gcOff {
		r, err := opcount.Count(cmd, m.o.memory)
		if err != nil {
			return one{}, fmt.Errorf("%s: %w", bin, err)
		}
		return one{run: r}, nil
	}
	r, shares, err := opcount.CountShares(cmd, m.o.memory, bin)
	if err != nil {
		return one{}, fmt.Errorf("%s: %w", bin, err)
	}
	return one{run: r, shares: shares, metrics: readMetrics(out)}, nil
}

// profiles writes -profile's files: for each side, -rounds runs at the
// large size with the profile hook's variable set, merged into one
// profile by go tool pprof -proto (pprof sums the samples), the program's
// to -profile and each reference side's to -profile.SIDE. A run that does
// not exit 0 fails it: a profile of a failed run is not the program's.
func (m measurer) profiles(b builder, sides []side) error {
	for _, s := range sides {
		out := m.o.profile
		if s.name != "ergo" {
			out += "." + s.name
		}
		var parts []string
		for r := range m.o.rounds {
			part := filepath.Join(m.o.work, fmt.Sprintf("profile-%s-%d.pprof", s.name, r))
			null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
			if err != nil {
				return err
			}
			cmd, done, err := m.command(s.bin, m.w.Large, null, null)
			if err != nil {
				null.Close()
				return err
			}
			cmd.Env = append(cmd.Env, opcount.ProfileVariable+"="+part)
			err = proclimit.Run(cmd, m.o.memory)
			done()
			null.Close()
			if err != nil {
				return fmt.Errorf("%s: the profiled run: %v", s.name, err)
			}
			parts = append(parts, part)
		}
		merged, err := os.Create(out)
		if err != nil {
			return err
		}
		cmd := exec.Command(b.release.Go, append([]string{"tool", "pprof", "-proto"}, parts...)...)
		cmd.Env = append(os.Environ(), "GOROOT="+b.release.Root, "GOTOOLCHAIN=local")
		var stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = merged, &stderr
		err = cmd.Run()
		if cerr := merged.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("go tool pprof -proto: %v\n%s", err, stderr.Bytes())
		}
		fmt.Fprintf(b.log, "profile %s: %s (%d runs at %d)\n", s.name, out, len(parts), m.w.Large)
	}
	return nil
}

// readMetrics reads the hook's "NAME VALUE" lines; nil when it wrote none.
func readMetrics(path string) map[string]float64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	out := map[string]float64{}
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, " "); ok {
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				out[k] = f
			}
		}
	}
	return out
}

// round runs one side at both sizes, with and without the collector, and
// derives the counters.
func (m measurer) round(s side) (sample, error) {
	runs := map[string]one{}
	for _, n := range []int{m.w.Small, m.w.Large} {
		for _, off := range []bool{false, true} {
			got, err := m.once(s.bin, n, off)
			if err != nil {
				return nil, err
			}
			runs[fmt.Sprintf("%d/%v", n, off)] = got
		}
	}
	small, large := runs[fmt.Sprintf("%d/false", m.w.Small)], runs[fmt.Sprintf("%d/false", m.w.Large)]
	smallOff, largeOff := runs[fmt.Sprintf("%d/true", m.w.Small)], runs[fmt.Sprintf("%d/true", m.w.Large)]
	out := sample{}
	// A run that did not finish refuses every counter of the round.
	for _, r := range []one{small, large, smallOff, largeOff} {
		if id, why := r.run.Refusal(); id == opcount.RunIncomplete {
			for _, k := range counters {
				out[k.name] = result{refused: &opcount.Refused{ID: id, Reason: why}}
			}
			return out, nil
		}
	}
	instr := func(name string, runs ...one) {
		for _, r := range runs {
			if id, why := r.run.Refusal(); id != "" {
				out[name] = result{refused: &opcount.Refused{ID: id, Reason: why}}
				return
			}
		}
		switch name {
		case "Instructions":
			out[name] = result{value: opcount.Slope(float64(small.run.Instructions), float64(large.run.Instructions), m.w.Small, m.w.Large)}
		case "InstructionsGCOff":
			out[name] = result{value: opcount.Slope(float64(smallOff.run.Instructions), float64(largeOff.run.Instructions), m.w.Small, m.w.Large)}
		case "GCShare":
			on, off := float64(large.run.Instructions), float64(largeOff.run.Instructions)
			out[name] = result{value: (on - off) / on}
		}
	}
	instr("Instructions", small, large)
	instr("InstructionsGCOff", smallOff, largeOff)
	instr("GCShare", large, largeOff)
	metric := func(name, key string, perUnit bool) {
		a, okA := small.metrics[key]
		b, okB := large.metrics[key]
		switch {
		case !okB || perUnit && !okA:
			out[name] = result{refused: &opcount.Refused{ID: opcount.NoMetric, Reason: "the exit hook reported no " + key}}
		case perUnit:
			out[name] = result{value: opcount.Slope(a, b, m.w.Small, m.w.Large)}
		default:
			out[name] = result{value: b}
		}
	}
	metric("Allocs", "allocs", true)
	metric("AllocBytes", "alloc_bytes", true)
	metric("GCCycles", "gc_cycles", false)
	metric("GCCPUShare", "gc_cpu_share", false)
	metric("LiveHeapBytes", "live_heap_bytes", false)
	metric("PeakRSSKiB", "peak_rss_kib", false)
	parts(out, &small.shares, large.shares, m.w.Small, m.w.Large)
	return out, nil
}

// agree runs every side on the small input and compares what they print.
// A side that fails is an error; sides that print different bytes refuse
// the comparison (NotEquivalent).
func (m measurer) agree(sides []side) (*opcount.Refused, error) {
	var first []byte
	for i, s := range sides {
		path := filepath.Join(m.o.work, "agree-"+s.name)
		f, err := os.Create(path)
		if err != nil {
			return nil, err
		}
		cmd, done, err := m.command(s.bin, m.w.Small, f, f)
		if err != nil {
			f.Close()
			return nil, err
		}
		r, err := opcount.Count(cmd, m.o.memory)
		done()
		f.Close()
		if err != nil {
			return nil, err
		}
		got, _ := os.ReadFile(path)
		if id, why := r.Refusal(); id == opcount.RunIncomplete {
			return nil, fmt.Errorf("%s on the small input: %s\n%s", s.name, why, got)
		}
		if i == 0 {
			first = got
		} else if !bytes.Equal(got, first) {
			return &opcount.Refused{ID: opcount.NotEquivalent, Reason: fmt.Sprintf("on the small input %s printed %d bytes that differ from %s's %d", s.name, len(got), sides[0].name, len(first))}, nil
		}
	}
	return nil, nil
}

// commandCases is command mode: each target, base and head alternating.
func commandCases(o options, args []string, log io.Writer) ([]opcount.Case, error) {
	targets := []string{""}
	if o.each != "" {
		matches, err := filepath.Glob(o.each)
		if err != nil || len(matches) == 0 {
			return nil, fmt.Errorf("-each %q matches nothing", o.each)
		}
		targets = matches
	}
	sides := []side{{"base", o.base}, {"head", o.head}}
	var cases []opcount.Case
	for _, t := range targets {
		argv := make([]string, len(args))
		for i, a := range args {
			argv[i] = strings.ReplaceAll(a, "{}", t)
		}
		values := map[string]map[string][]float64{}
		refused := map[string]*opcount.Refused{}
		for r := range o.rounds {
			order := sides
			if r%2 == 1 {
				order = reversed(sides)
			}
			for _, s := range order {
				null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
				if err != nil {
					return nil, err
				}
				cmd := exec.Command(s.bin, argv...)
				cmd.Stdout, cmd.Stderr = null, null
				bin, err := exec.LookPath(s.bin)
				if err != nil {
					null.Close()
					return nil, err
				}
				got, shares, err := opcount.CountShares(cmd, o.memory, bin)
				null.Close()
				if err != nil {
					return nil, fmt.Errorf("%s: %w", s.bin, err)
				}
				if values[s.name] == nil {
					values[s.name] = map[string][]float64{}
				}
				if id, why := got.Refusal(); id == opcount.RunIncomplete {
					refused[s.name+"/*"] = &opcount.Refused{ID: id, Reason: why}
				} else if id != "" {
					refused[s.name+"/Instructions"] = &opcount.Refused{ID: id, Reason: why}
				}
				values[s.name]["Instructions"] = append(values[s.name]["Instructions"], float64(got.Instructions))
				values[s.name]["CPUSeconds"] = append(values[s.name]["CPUSeconds"], (got.UserCPU + got.SysCPU).Seconds())
				values[s.name]["MaxRSSKiB"] = append(values[s.name]["MaxRSSKiB"], float64(got.MaxRSSKiB))
				ps := sample{}
				parts(ps, nil, shares, 0, 0)
				for k, v := range ps {
					if v.refused != nil {
						refused[s.name+"/"+k] = v.refused
					}
					values[s.name][k] = append(values[s.name][k], v.value)
				}
			}
		}
		name := t
		if name == "" {
			name = strings.Join(args, " ")
		}
		fmt.Fprintf(log, "measured %s\n", name)
		c := opcount.Case{Case: name, Rounds: o.rounds}
		kinds := append([]counter{{"Instructions", opcount.Counted, false}, {"CPUSeconds", opcount.Timed, false}, {"MaxRSSKiB", opcount.Timed, false}}, partCounters(false)...)
		for _, s := range sides {
			for _, k := range kinds {
				why := refused[s.name+"/*"]
				if why == nil {
					why = refused[s.name+"/"+k.name]
				}
				c.Series = append(c.Series, opcount.NewSeries(s.name, k.name, k.kind, false, values[s.name][k.name], why))
			}
		}
		for i := range kinds {
			c.Comparisons = append(c.Comparisons, opcount.Compare(c.Series[i], c.Series[len(kinds)+i]))
		}
		cases = append(cases, c)
	}
	return cases, nil
}

// builder builds the measured binaries into the scratch folder, every Go
// build with release: the Go release ergo builds with, with ergo's patched
// compile (internal/toolchain, Patch), which compiles a package without
// -ergoir as the stock compile does.
type builder struct {
	o       options
	root    string
	log     io.Writer
	release toolchain.Patched
}

// patchedRelease is the release ergo build runs, with ergo's patched
// compile: the go command ERGO_GO selects, the pins of ergo's cache, as
// cmd/ergo's buildAction takes them.
func patchedRelease(log io.Writer) (toolchain.Patched, error) {
	root, err := build.DefaultRoot()
	if err != nil {
		return toolchain.Patched{}, err
	}
	selected, err := toolchain.Select(os.Getenv, root, log)
	if err != nil {
		return toolchain.Patched{}, err
	}
	pins, err := toolchain.PinSource(root, os.Getenv("ERGO_PIN_URL"), log)
	if err != nil {
		return toolchain.Patched{}, err
	}
	return sketchfront.PinnedRelease(selected, root, &pins, log)
}

var goLinePattern = regexp.MustCompile(`(?m)^go [0-9.]+$`)

// built is the Ergo side's binary, the go line of its module and, on the
// IR path, its bodies per package.
type built struct {
	bin, goLine string
	bodies      []opcount.Bodies
}

// program builds the Ergo program at cmdDir as the product does: ergo
// build -work emits the module, and opcount builds it again with the exit
// hook added by an overlay, so neither the checkout nor the emission cache
// is written, by ergo's own go build action (build.Toolchain.Command):
// each package with an IR file compiled with -ergoir. With -text it builds
// the module ergo build -go-text writes instead, the same IR bodies
// printed as Go text, which compiles without -ergoir.
func (b builder) program(cmdDir, name string) (built, error) {
	ergo := b.o.ergo
	if ergo == "" {
		ergo = filepath.Join(b.o.work, "ergo")
		build := ergobuild.Command(ergo, "./cmd/ergo")
		build.Dir = b.root
		build.Env = ergobuild.Env(append(os.Environ(), "GOWORK=off"))
		if err := b.run(build); err != nil {
			return built{}, err
		}
	}
	args := []string{"build", "-work"}
	work := ""
	if b.o.text {
		work = filepath.Join(b.o.work, name+"-text")
		args = []string{"build", "-go-text", work}
	}
	args = append(args, "-o", filepath.Join(b.o.work, name+"-plain"), cmdDir)
	var stderr bytes.Buffer
	cmd := exec.Command(ergo, args...)
	cmd.Stdout, cmd.Stderr = io.Discard, &stderr
	if err := proclimit.Run(cmd, b.o.memory); err != nil {
		return built{}, fmt.Errorf("ergo %s: %v\n%s", strings.Join(args, " "), err, stderr.Bytes())
	}
	for _, line := range strings.Split(stderr.String(), "\n") {
		if v, ok := strings.CutPrefix(line, "WORK="); ok && !b.o.text {
			work = v
		}
	}
	if work == "" {
		return built{}, fmt.Errorf("ergo build -work %s printed no WORK= line:\n%s", cmdDir, stderr.Bytes())
	}
	mod, err := os.ReadFile(filepath.Join(work, "go.mod"))
	if err != nil {
		return built{}, err
	}
	goLine := string(goLinePattern.Find(mod))
	if goLine == "" {
		return built{}, fmt.Errorf("%s/go.mod has no go line", work)
	}
	hook := filepath.Join(b.o.work, "hook-"+name+".go")
	if err := os.WriteFile(hook, []byte(opcount.Hook), 0o644); err != nil {
		return built{}, err
	}
	replace := map[string]string{filepath.Join(work, opcount.HookFile): hook}
	if b.o.profile != "" {
		profileHook := filepath.Join(b.o.work, "profile-hook-"+name+".go")
		if err := os.WriteFile(profileHook, []byte(opcount.ProfileHook), 0o644); err != nil {
			return built{}, err
		}
		replace[filepath.Join(work, opcount.ProfileHookFile)] = profileHook
	}
	overlay, err := json.Marshal(map[string]map[string]string{"Replace": replace})
	if err != nil {
		return built{}, err
	}
	overlayFile := filepath.Join(b.o.work, "overlay-"+name+".json")
	if err := os.WriteFile(overlayFile, overlay, 0o644); err != nil {
		return built{}, err
	}
	out := built{bin: filepath.Join(b.o.work, name), goLine: goLine}
	if err := b.run(moduleBuild(b.release, work, overlayFile, out.bin, b.o.pgo)); err != nil {
		return built{}, err
	}
	if !b.o.text {
		if out.bodies, err = irBodies(work); err != nil {
			return built{}, err
		}
	}
	return out, nil
}

// moduleBuild is ergo's go build action (build.Toolchain.Command) of the
// emitted module at dir with release, unstripped, the overlay and
// -checklinkname=0 (the exit hook's linkname) in GOFLAGS, after the
// caller's own GOFLAGS (such as -tags=ergo_stockpark, the runtime's stock
// park body: PARK1): each package that has an IR file is compiled with its
// -ergoir. A pgo profile builds it as ergo build --release does
// (build.Toolchain.PGO).
func moduleBuild(release toolchain.Patched, dir, overlay, bin, pgo string) *exec.Cmd {
	tc := build.HostToolchain()
	tc.Go, tc.Root = release.Go, release.Root
	tc.GOOS, tc.GOARCH = runtime.GOOS, runtime.GOARCH
	tc.GOFLAGS = strings.TrimSpace(os.Getenv("GOFLAGS") + " '-overlay=" + overlay + "' -ldflags=-checklinkname=0")
	tc.GoBuild = true
	tc.PGO = pgo
	return tc.Command(build.Module{Dir: dir}, bin)
}

// irBodies counts, per Go package of the module at dir (not of a module
// nested in it, such as the runtime's), its function
// declarations and the functions its IR file names, whose bodies the
// patched compile takes from IR: packages in path order, "." the root.
func irBodies(dir string) ([]opcount.Bodies, error) {
	counts := map[string]*opcount.Bodies{}
	at := func(rel string) *opcount.Bodies {
		rel = filepath.ToSlash(rel)
		if counts[rel] == nil {
			counts[rel] = &opcount.Bodies{Package: rel}
		}
		return counts[rel]
	}
	err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			// A folder with a go.mod of its own (the runtime's) is
			// another module: Ergo bodies are lowered into this one.
			if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil && p != dir {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, filepath.Dir(p))
		if err != nil {
			return err
		}
		switch {
		case e.Name() == build.IRFile:
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			var ir struct {
				Funcs []json.RawMessage `json:"funcs"`
			}
			if err := json.Unmarshal(data, &ir); err != nil {
				return fmt.Errorf("%s: %v", p, err)
			}
			at(rel).IR += len(ir.Funcs)
		case strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go"):
			f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			for _, d := range f.Decls {
				if _, ok := d.(*ast.FuncDecl); ok {
					at(rel).Funcs++
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]opcount.Bodies, 0, len(counts))
	for _, c := range counts {
		out = append(out, *c)
	}
	slices.SortFunc(out, func(a, b opcount.Bodies) int { return strings.Compare(a.Package, b.Package) })
	return out, nil
}

// goPackage builds the main package rel (from the repository root) as a
// module of its own with the emitted module's go line, so one toolchain
// compiles both sides; hooked adds the exit hook.
func (b builder) goPackage(rel, role, goLine string, hooked bool) (string, error) {
	src := filepath.Join(b.root, filepath.FromSlash(rel))
	dir := filepath.Join(b.o.work, role)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	files, err := filepath.Glob(filepath.Join(src, "*.go"))
	if err != nil || len(files) == 0 {
		return "", fmt.Errorf("%s holds no Go file", rel)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(f)), data, 0o644); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module "+role+"\n\n"+goLine+"\n"), 0o644); err != nil {
		return "", err
	}
	if hooked {
		if err := os.WriteFile(filepath.Join(dir, opcount.HookFile), []byte(opcount.Hook), 0o644); err != nil {
			return "", err
		}
		if b.o.profile != "" {
			if err := os.WriteFile(filepath.Join(dir, opcount.ProfileHookFile), []byte(opcount.ProfileHook), 0o644); err != nil {
				return "", err
			}
		}
	}
	bin := filepath.Join(b.o.work, role+"-bin")
	args := []string{"build", "-trimpath", "-buildvcs=false", "-ldflags=-checklinkname=0"}
	if b.o.pgo != "" && hooked {
		// The reference side's own profile, taken by the same method
		// (-profile wrote it beside the program's); none leaves it plain.
		if _, err := os.Stat(b.o.pgo + "." + role); err == nil {
			args = append(args, "-pgo="+b.o.pgo+"."+role)
		} else {
			fmt.Fprintf(b.log, "opcount: no %s.%s; the %s builds without a profile\n", b.o.pgo, role, role)
		}
	}
	cmd := exec.Command(b.release.Go, append(args, "-o", bin, ".")...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOROOT="+b.release.Root, "GOTOOLCHAIN=local")
	if err := b.run(cmd); err != nil {
		return "", err
	}
	return bin, nil
}

// generate writes what gen prints with args to path.
func (b builder) generate(gen string, args []string, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var stderr bytes.Buffer
	cmd := exec.Command(gen, args...)
	cmd.Stdout, cmd.Stderr = f, &stderr
	if err := proclimit.Run(cmd, b.o.memory); err != nil {
		return fmt.Errorf("generate %s %s: %v\n%s", filepath.Base(gen), strings.Join(args, " "), err, stderr.Bytes())
	}
	return nil
}

// exec runs a build step in dir, bounded, outside any Go workspace.
func (b builder) exec(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	return b.run(cmd)
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

func host() opcount.Host {
	h := opcount.Host{OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(), GoVersion: runtime.Version(), Counter: "user instructions",
		SamplePeriod: opcount.Period(), Level: build.Toolchain{GOARCH: runtime.GOARCH}.TargetLevel()}
	if out, err := exec.Command("go", "env", "GOVERSION").Output(); err == nil {
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

// priority is this process's nice value (field 19 of /proc/self/stat), 0
// where it cannot be read.
func priority() int {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0
	}
	s := string(data)
	if i := strings.LastIndexByte(s, ')'); i >= 0 {
		fields := strings.Fields(s[i+1:])
		if len(fields) > 16 {
			n, _ := strconv.Atoi(fields[16])
			return n
		}
	}
	return 0
}

// run runs cmd under the memory limit, logging its words.
func (b builder) run(cmd *exec.Cmd) error {
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	fmt.Fprintf(b.log, "%s\n", strings.Join(cmd.Args, " "))
	if err := proclimit.Run(cmd, b.o.memory); err != nil {
		return fmt.Errorf("%s: %v\n%s", strings.Join(cmd.Args, " "), err, out.Bytes())
	}
	return nil
}
