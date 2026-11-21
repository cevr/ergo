// Command checkfloor is the checker's floor probe (LOWER BOUND and TENTH
// OF GO, capstone S3d4). It counts three checkers on one package of N
// functions of one shape, in user instructions per function, as the slope
// between two sizes:
//
//   - the self-hosted checker, compiler/cmd/exprprobe: compiler/syntax,
//     check/bind, check/expr's Load and its typer, built by ergo build;
//   - the Go checker, internal/sketch/check (the product's checker);
//   - the floor: the least work a checker of the probe's language needs,
//     written by hand (floor.go): one scan for the signatures, one pass per
//     body that types each expression once, no tree.
//
// Usage, from the repository root:
//
//	go run ./scripts/checkfloor [-rounds N] [-sizes A,B,C] [-ergo BIN] [-o FILE]
//
// builds exprprobe as scripts/opcount builds a program (build.go), checks
// that the three agree (each accepts the package and rejects each bad one,
// badEdits, and every side reads the same source bytes), then counts
// each side's modes at each size for N rounds, the sides alternating, with
// the default collector and with GOGC=off (the collector's work is the
// noise of a count): instructions, allocations and bytes. It writes a
// Markdown receipt.
// Each mode stops at a phase, so a phase's cost is one mode's slope less
// the one before it, and writing the source cancels. It pins no count. Every
// run is a fresh process under a memory bound (internal/opcount.Count).
//
// The two Go sides are this command's own modes:
//
//	checkfloor go N gen|parse|check|check-bad-RULE
//	checkfloor floor N gen|check|check-bad-RULE
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/opcount"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// source is exprprobe's package of n shaped functions, byte for byte; bad
// makes the last function compare its string parameter with an int.
func source(n int, rule string) string {
	var b strings.Builder
	b.WriteString("func id[T](x T) T { x }\n")
	e, bad := badEdits[rule]
	for i := range n {
		f := fmt.Sprintf("\nfunc f%d(p int, q string, xs []int) int {\n\tlet mut acc = p + 1\n\tlet a = id(p)\n\tlet c = xs[0] * 2\n\tfor j, e in xs {\n\t\tacc = acc + e - j\n\t}\n\tif acc > 3 && q != \"x\" {\n\t\tacc = acc + len(q)\n\t}\n\tacc + a + c\n}\n", i)
		if bad && i == n-1 {
			f = strings.ReplaceAll(f, e[0], e[1])
		}
		b.WriteString(f)
	}
	return b.String()
}

// badEdits are the bad packages' edits of the last function, by the rule
// each breaks (exprprobe's badEdit, byte for byte): a type error, a name
// nothing declares, an assignment to a parameter, and a call of two
// arguments to one parameter. Every side must reject each.
var badEdits = map[string][2]string{
	"type":  {`q != "x"`, "q != 1"},
	"name":  {"acc + a + c\n", "acc + a + zz\n"},
	"mut":   {"\t\tacc = acc + len(q)", "\t\tp = acc + len(q)"},
	"arity": {"id(p)", "id(p, p)"},
}

func main() {
	if len(os.Args) == 4 && (os.Args[1] == "go" || os.Args[1] == "floor") {
		os.Exit(side(os.Args[1], os.Args[2], os.Args[3], os.Stdout))
	}
	code, err := run(os.Args[1:], os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "checkfloor:", err)
	}
	os.Exit(code)
}

// side runs one Go side's mode and prints what it reached; a rejected
// package exits 1.
func side(which, size, mode string, out io.Writer) int {
	n, err := strconv.Atoi(size)
	if err != nil || n < 1 {
		fmt.Fprintln(os.Stderr, "checkfloor: bad size", size)
		return 2
	}
	defer writeMemStats()
	src := source(n, strings.TrimPrefix(mode, "check-bad-"))
	if mode == "gen" {
		fmt.Fprintf(out, "bytes=%d\n", len(src))
		return 0
	}
	switch which {
	case "go":
		file, err := syntax.Parse(src)
		if err != nil {
			fmt.Fprintln(out, "rejected:", err)
			return 1
		}
		if mode == "parse" {
			fmt.Fprintf(out, "decls=%d\n", len(file.Decls))
			return 0
		}
		pkg, ds := check.Check("main", []*syntax.File{file}, nil)
		if len(ds) > 0 || pkg == nil {
			fmt.Fprintln(out, "rejected:", len(ds), "diagnostics")
			return 1
		}
		fmt.Fprintf(out, "fns=%d typed=%d\n", n+1, len(pkg.Info.Types))
	case "floor":
		fns, typed, err := checkFloor([]byte(src))
		if err != nil {
			fmt.Fprintln(out, "rejected:", err)
			return 1
		}
		fmt.Fprintf(out, "fns=%d typed=%d\n", fns, typed)
	}
	return 0
}

// mode is one counted program: a side and the phase it stops at.
type mode struct {
	side, name string
	argv       func(n int) []string
}

func run(args []string, log io.Writer) (int, error) {
	flags := flag.NewFlagSet("checkfloor", flag.ContinueOnError)
	rounds := flags.Int("rounds", 3, "rounds per program and size (the counted receipt: 5)")
	sizes := flags.String("sizes", "50,100,200", "three package sizes, in functions (the counted receipt: 200,400,800)")
	ergo := flags.String("ergo", "bin/ergo", "the ergo command that builds exprprobe")
	outPath := flags.String("o", "", "the receipt (default: stdout)")
	memory := flags.Uint64("memory", 4<<30, "each run's memory bound, in bytes")
	if err := flags.Parse(args); err != nil {
		return 2, err
	}
	var ns []int
	for _, s := range strings.Split(*sizes, ",") {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return 2, fmt.Errorf("bad size %q", s)
		}
		ns = append(ns, n)
	}
	if len(ns) != 3 || !slices.IsSorted(ns) {
		return 2, errors.New("-sizes takes three increasing sizes")
	}
	if err := opcount.Available(); err != nil {
		return 2, err
	}
	work, err := os.MkdirTemp("", "checkfloor-")
	if err != nil {
		return 2, err
	}
	defer os.RemoveAll(work)
	self, err := os.Executable()
	if err != nil {
		return 2, err
	}
	ergoBin, err := filepath.Abs(*ergo)
	if err != nil {
		return 2, err
	}
	probe, err := buildProbe(ergoBin, work, *memory, log)
	if err != nil {
		return 2, err
	}
	ergoMode := func(m string) func(int) []string {
		return func(n int) []string { return []string{probe, strconv.Itoa(n), m} }
	}
	goMode := func(s, m string) func(int) []string {
		return func(n int) []string { return []string{self, s, strconv.Itoa(n), m} }
	}
	modes := []mode{
		{"ergo", "gen", ergoMode("gen")}, {"ergo", "parse", ergoMode("parse")}, {"ergo", "setup", ergoMode("setup")}, {"ergo", "check", ergoMode("check")}, {"ergo", "check-fused", ergoMode("check-fused")},
		{"go", "gen", goMode("go", "gen")}, {"go", "parse", goMode("go", "parse")}, {"go", "check", goMode("go", "check")},
		{"floor", "gen", goMode("floor", "gen")}, {"floor", "check", goMode("floor", "check")},
	}
	// Agreement: one source, every side accepts it and rejects the bad one.
	small := ns[0]
	outs := map[string]string{}
	for _, m := range modes {
		o, code, err := once(work, m.argv(small))
		if err != nil || code != 0 {
			return 1, fmt.Errorf("%s %s at %d: exit %d %v: %s", m.side, m.name, small, code, err, o)
		}
		outs[m.side+" "+m.name] = o
	}
	if outs["ergo gen"] != outs["go gen"] || outs["go gen"] != outs["floor gen"] {
		return 1, fmt.Errorf("the sides read different sources: %q %q %q", outs["ergo gen"], outs["go gen"], outs["floor gen"])
	}
	if outs["ergo check"] != outs["ergo check-fused"] {
		return 1, fmt.Errorf("the fused walk typed another count: %q %q", outs["ergo check"], outs["ergo check-fused"])
	}
	for _, rule := range slices.Sorted(maps.Keys(badEdits)) {
		m := "check-bad-" + rule
		for _, bad := range [][]string{ergoMode(m)(small), goMode("go", m)(small), goMode("floor", m)(small)} {
			o, code, _ := once(work, bad)
			if code != 1 {
				return 1, fmt.Errorf("%s accepted the package that breaks %s: %s", strings.Join(bad, " "), rule, o)
			}
		}
	}
	var b strings.Builder
	for _, gc := range []string{"", "off"} {
		counts, err := countAll(modes, ns, *rounds, *memory, gc, work, log)
		if err != nil {
			return 1, err
		}
		metrics := []string{"instructions"}
		if gc != "" {
			// Allocations do not depend on the collector: once.
			metrics = append(metrics, "allocations", "bytes")
		}
		for _, metric := range metrics {
			b.WriteString(report(modes, counts[metric], ns, *rounds, gc, metric))
		}
	}
	receipt := header(ns[0], outs) + b.String()
	if *outPath == "" {
		fmt.Print(receipt)
		return 0, nil
	}
	return 0, os.WriteFile(*outPath, []byte(receipt), 0o644)
}

// countAll counts every mode at every size for the rounds, with GOGC=gc
// ("" for the default collector): counts[metric][mode][size] holds one
// count per round, of instructions, allocations and bytes.
func countAll(modes []mode, ns []int, rounds int, memory uint64, gc, work string, log io.Writer) (map[string][][][]float64, error) {
	counts := map[string][][][]float64{}
	for _, metric := range []string{"instructions", "allocations", "bytes"} {
		c := make([][][]float64, len(modes))
		for i := range c {
			c[i] = make([][]float64, len(ns))
		}
		counts[metric] = c
	}
	stats := filepath.Join(work, "stats")
	env := append(os.Environ(), opcount.HookVariable+"="+stats)
	if gc != "" {
		env = append(env, "GOGC="+gc)
	}
	for r := range rounds {
		order := slices.Clone(modes)
		if r%2 == 1 {
			slices.Reverse(order)
		}
		for _, m := range order {
			i := slices.IndexFunc(modes, func(x mode) bool { return x.side == m.side && x.name == m.name })
			for j, n := range ns {
				argv := m.argv(n)
				cmd := exec.Command(argv[0], argv[1:]...)
				cmd.Env = env
				f, err := os.Create(filepath.Join(work, "out"))
				if err != nil {
					return nil, err
				}
				cmd.Stdout, cmd.Stderr = f, f
				got, err := opcount.Count(cmd, memory)
				f.Close()
				if err != nil {
					return nil, err
				}
				if why, text := got.Refusal(); why != "" {
					return nil, fmt.Errorf("%s %s at %d (GOGC=%s): %s: %s", m.side, m.name, n, gc, why, text)
				}
				allocs, bytes, err := readStats(stats)
				if err != nil {
					return nil, fmt.Errorf("%s %s at %d: %v", m.side, m.name, n, err)
				}
				counts["instructions"][i][j] = append(counts["instructions"][i][j], float64(got.Instructions))
				counts["allocations"][i][j] = append(counts["allocations"][i][j], allocs)
				counts["bytes"][i][j] = append(counts["bytes"][i][j], bytes)
			}
			fmt.Fprintf(log, "round %d (GOGC=%s): %s %s\n", r+1, gc, m.side, m.name)
		}
	}
	return counts, nil
}

// readStats reads the allocations and bytes a run wrote at its exit (the
// hook's lines), and removes the file so the next run writes its own.
func readStats(path string) (float64, float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, fmt.Errorf("the run wrote no allocation counts: %v", err)
	}
	os.Remove(path)
	var allocs, bytes float64
	seen := 0
	for _, line := range strings.Split(string(data), "\n") {
		name, v, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		x, err := strconv.ParseFloat(v, 64)
		if err != nil {
			continue
		}
		switch name {
		case "allocs":
			allocs, seen = x, seen+1
		case "alloc_bytes":
			bytes, seen = x, seen+1
		}
	}
	if seen != 2 {
		return 0, 0, fmt.Errorf("allocation counts missing in %q", data)
	}
	return allocs, bytes, nil
}

// header is the receipt's title and the agreement it checked.
func header(small int, outs map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Checker floor probe (scripts/checkfloor)\n\n")
	fmt.Fprintf(&b, "Per function: the slope between sizes, the median of the rounds (spread: (max - min) / median). Instructions are user instructions of the process tree, counted; allocations and bytes are the runtime's MemStats at exit (Mallocs, TotalAlloc), exact.\n\n")
	fmt.Fprintf(&b, "Agreement at %d: ergo `%s`; go `%s`; floor `%s`; each side rejects each bad package (a type error, a name nothing declares, an assignment to a parameter, a call of two arguments to one parameter).\n", small, outs["ergo check"], outs["go check"], outs["floor check"])
	return b.String()
}

// once runs argv and returns what it printed and its exit code.
func once(work string, argv []string) (string, int, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return strings.TrimSpace(string(out)), exit.ExitCode(), nil
	}
	return strings.TrimSpace(string(out)), 0, err
}

// phase is a cost the receipt reports: mode hi's slope less mode lo's,
// per function, one value per round, at the first and the second interval.
type phase struct {
	side, name string
	hi, lo     string
}

func report(modes []mode, counts [][][]float64, ns []int, rounds int, gc, metric string) string {
	at := func(side, name string) int {
		return slices.IndexFunc(modes, func(m mode) bool { return m.side == side && m.name == name })
	}
	// slope of mode i over interval k (sizes k, k+1), per round.
	slope := func(i, k int) []float64 {
		var xs []float64
		for r := range rounds {
			xs = append(xs, opcount.Slope(counts[i][k][r], counts[i][k+1][r], ns[k], ns[k+1]))
		}
		return xs
	}
	cost := func(p phase, k int) []float64 {
		hi, lo := slope(at(p.side, p.hi), k), slope(at(p.side, p.lo), k)
		xs := make([]float64, rounds)
		for r := range xs {
			xs[r] = hi[r] - lo[r]
		}
		return xs
	}
	phases := []phase{
		{"ergo", "parse", "parse", "gen"},
		{"ergo", "bind and load", "setup", "parse"},
		{"ergo", "typing", "check", "setup"},
		{"ergo", "checker (parse to types)", "check", "gen"},
		{"ergo", "typing, fused (no Entries)", "check-fused", "setup"},
		{"ergo", "checker, fused", "check-fused", "gen"},
		{"go", "parse", "parse", "gen"},
		{"go", "check", "check", "parse"},
		{"go", "checker (parse to types)", "check", "gen"},
		{"floor", "checker (bytes to types)", "check", "gen"},
	}
	var b strings.Builder
	title := "default collector"
	if gc != "" {
		title = "GOGC=" + gc
	}
	fmt.Fprintf(&b, "\n## %s per function, %s, %d rounds\n\n", metric, title, rounds)
	fmt.Fprintf(&b, "| side | phase | %d→%d | %d→%d |\n| --- | --- | --- | --- |\n", ns[0], ns[1], ns[1], ns[2])
	med := map[string][2]float64{}
	for _, p := range phases {
		var cells [2]string
		for k := range 2 {
			xs := cost(p, k)
			m := opcount.Median(xs)
			cells[k] = fmt.Sprintf("%.0f (spread %.1f%%)", m, 100*spread(xs, m))
			if metric == "allocations" {
				cells[k] = fmt.Sprintf("%.2f (spread %.1f%%)", m, 100*spread(xs, m))
			}
			v := med[p.side+" "+p.name]
			v[k] = m
			med[p.side+" "+p.name] = v
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", p.side, p.name, cells[0], cells[1])
	}
	ratio := func(a, c string) string {
		x, y := med[a], med[c]
		return fmt.Sprintf("%.2f / %.2f", x[0]/y[0], x[1]/y[1])
	}
	fmt.Fprintf(&b, "\nRatios (first / second interval):\n\n")
	fmt.Fprintf(&b, "- self-hosted / Go checker: %s\n", ratio("ergo checker (parse to types)", "go checker (parse to types)"))
	fmt.Fprintf(&b, "- self-hosted / floor: %s\n", ratio("ergo checker (parse to types)", "floor checker (bytes to types)"))
	fmt.Fprintf(&b, "- Go checker / floor: %s\n", ratio("go checker (parse to types)", "floor checker (bytes to types)"))
	fmt.Fprintf(&b, "- self-hosted, fused / Go checker: %s\n", ratio("ergo checker, fused", "go checker (parse to types)"))
	fmt.Fprintf(&b, "- self-hosted, fused / floor: %s\n", ratio("ergo checker, fused", "floor checker (bytes to types)"))
	fmt.Fprintf(&b, "- self-hosted typing alone / floor: %s\n", ratio("ergo typing", "floor checker (bytes to types)"))
	fmt.Fprintf(&b, "- self-hosted typing, fused / floor: %s\n", ratio("ergo typing, fused (no Entries)", "floor checker (bytes to types)"))
	return b.String()
}

func spread(xs []float64, m float64) float64 {
	if m == 0 {
		return 0
	}
	return (slices.Max(xs) - slices.Min(xs)) / m
}
