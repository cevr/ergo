// Command stdcount counts each hot std function three ways and writes the
// receipt docs/receipts/std-count.md: user-space instructions and heap
// allocations per operation of the Ergo std form (called from an Ergo
// program as a user writes it), of the Go std function, and of hand Go
// where a case names one (owner rule std-better, 2026-11-12: Ergo's std is
// measured against Go's in counted instructions and allocations).
//
//	go run ./scripts/stdcount -ergo ERGO -scratch DIR [-rounds 5] [-only RE] [-out FILE] [-check]
//
// ERGO is an ergo binary built from this checkout. The harness writes one
// Ergo and one Go program from the case table (cases.go) into DIR, builds
// both with an exit hook that prints the allocations, checks that each
// form of each case prints the same checksum (Go is the oracle), and then
// runs every form at two sizes, N/4 and N, for each round, interleaved.
// Per operation is the difference of the two counts over the difference of
// the sizes, so start-up and input set-up cancel; the receipt gives the
// median over the rounds and the spread. Instruction counts hold under
// host load, as wall times do not; the harness claims no time.
//
// It is a receipt tool, not a test: no count is asserted anywhere. Run it
// in a memory-bounded scope with GOMAXPROCS=1; it needs Linux and
// kernel.perf_event_paranoid 2 or lower.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

func main() {
	ergo := flag.String("ergo", "", "the ergo binary built from this checkout")
	scratch := flag.String("scratch", "", "a directory for the programs and binaries")
	rounds := flag.Int("rounds", 5, "interleaved rounds")
	only := flag.String("only", "", "count only the cases whose package/name matches this regexp")
	out := flag.String("out", "docs/receipts/std-count.md", "the receipt")
	check := flag.Bool("check", false, "build and check the checksums only")
	flag.Parse()
	if err := run(*ergo, *scratch, *rounds, *only, *out, *check); err != nil {
		fmt.Fprintln(os.Stderr, "stdcount:", err)
		os.Exit(1)
	}
}

// a form is one way to run a case: its binary and its case name there.
type form struct{ kind, bin, arg string }

// sample is one run's counts.
type sample struct{ instr, mallocs, bytes, writes float64 }

// perOp is a form's median counts per operation and the instruction spread.
type perOp struct {
	sample
	spread float64 // (max − min) / median of the per-op instruction slopes
}

func run(ergo, scratch string, rounds int, only, out string, checkOnly bool) error {
	if ergo == "" || scratch == "" {
		return fmt.Errorf("-ergo and -scratch are required")
	}
	if !filepath.IsAbs(scratch) {
		return fmt.Errorf("-scratch must be an absolute path")
	}
	repo, err := os.Getwd()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(repo, "scripts", "stdcount")); err != nil {
		return fmt.Errorf("run from the module root: %w", err)
	}
	selected := cases
	if only != "" {
		re, err := regexp.Compile(only)
		if err != nil {
			return err
		}
		selected = nil
		for _, c := range cases {
			if re.MatchString(c.Pkg + "/" + c.Name) {
				selected = append(selected, c)
			}
		}
	}
	ergoBin, goBin, err := buildPrograms(ergo, scratch, selected)
	if err != nil {
		return err
	}
	formsOf := func(i int, c Case) []form {
		fs := []form{{"ergo", ergoBin, fmt.Sprintf("c%d", i)}, {"go", goBin, fmt.Sprintf("g%d", i)}}
		if c.Hand != "" {
			fs = append(fs, form{"hand", goBin, fmt.Sprintf("h%d", i)})
		}
		return fs
	}
	outFile := filepath.Join(scratch, "stdout")
	// The oracle: every form prints Go's checksum.
	for i, c := range selected {
		var want string
		for _, f := range formsOf(i, c) {
			sum, _, err := runOnce(f, 1000, outFile)
			if err != nil {
				return fmt.Errorf("%s %s (%s): %w", c.Pkg, c.Name, f.kind, err)
			}
			if f.kind == "ergo" {
				want = sum
				continue
			}
			if sum != want {
				return fmt.Errorf("%s %s: the %s form prints %s, the Ergo form %s", c.Pkg, c.Name, f.kind, sum, want)
			}
		}
	}
	fmt.Fprintf(os.Stderr, "stdcount: %d cases agree with Go\n", len(selected))
	if checkOnly {
		return nil
	}
	loadBefore := loadavg()
	// slopes[case][form] holds one per-op sample per round.
	slopes := make([]map[string][]sample, len(selected))
	for i := range slopes {
		slopes[i] = map[string][]sample{}
	}
	for r := 0; r < rounds; r++ {
		for i, c := range selected {
			for _, f := range formsOf(i, c) {
				small, large := c.N/4, c.N
				_, a, err := runOnce(f, small, outFile)
				if err != nil {
					return fmt.Errorf("%s %s (%s): %w", c.Pkg, c.Name, f.kind, err)
				}
				_, b, err := runOnce(f, large, outFile)
				if err != nil {
					return fmt.Errorf("%s %s (%s): %w", c.Pkg, c.Name, f.kind, err)
				}
				d := float64(large - small)
				slopes[i][f.kind] = append(slopes[i][f.kind], sample{
					instr: (b.instr - a.instr) / d, mallocs: (b.mallocs - a.mallocs) / d,
					bytes: (b.bytes - a.bytes) / d, writes: (b.writes - a.writes) / d,
				})
			}
		}
		fmt.Fprintf(os.Stderr, "stdcount: round %d of %d done\n", r+1, rounds)
	}
	loadAfter := loadavg()
	results := make([]map[string]perOp, len(selected))
	for i := range selected {
		results[i] = map[string]perOp{}
		for kind, ss := range slopes[i] {
			results[i][kind] = summarize(ss)
		}
	}
	uses, err := countUses(repo, selected)
	if err != nil {
		return err
	}
	text := receipt(repo, selected, results, uses, rounds, loadBefore, loadAfter)
	return os.WriteFile(out, []byte(text), 0o644)
}

// runOnce runs one form n times and returns its checksum (the last line of
// its stdout) and its counts.
func runOnce(f form, n int, outFile string) (string, sample, error) {
	// A deadline only stops a hung run; no count depends on it.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.bin, f.arg, strconv.Itoa(n))
	stdout, err := os.Create(outFile)
	if err != nil {
		return "", sample{}, err
	}
	defer stdout.Close()
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = stdout, &stderr
	cmd.Env = append(os.Environ(), "GOMAXPROCS=1", "GODEBUG=asyncpreemptoff=1")
	instr, err := countInstructions(cmd)
	if err != nil {
		return "", sample{}, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	data, err := os.ReadFile(outFile)
	if err != nil {
		return "", sample{}, err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	s := sample{instr: float64(instr)}
	found := false
	for _, line := range strings.Split(stderr.String(), "\n") {
		if rest, ok := strings.CutPrefix(line, "ALLOCS "); ok {
			found = true
			for _, field := range strings.Fields(rest) {
				k, v, _ := strings.Cut(field, "=")
				x, err := strconv.ParseFloat(v, 64)
				if err != nil {
					return "", sample{}, fmt.Errorf("bad ALLOCS field %q", field)
				}
				switch k {
				case "mallocs":
					s.mallocs = x
				case "bytes":
					s.bytes = x
				case "syscw":
					s.writes = x
				}
			}
		}
	}
	if !found {
		return "", sample{}, fmt.Errorf("no ALLOCS line from the exit hook: %s", strings.TrimSpace(stderr.String()))
	}
	return lines[len(lines)-1], s, nil
}

func summarize(ss []sample) perOp {
	med := func(get func(sample) float64) float64 {
		xs := make([]float64, len(ss))
		for i, s := range ss {
			xs[i] = get(s)
		}
		slices.Sort(xs)
		if len(xs)%2 == 1 {
			return xs[len(xs)/2]
		}
		return (xs[len(xs)/2-1] + xs[len(xs)/2]) / 2
	}
	p := perOp{sample: sample{
		instr:   med(func(s sample) float64 { return s.instr }),
		mallocs: med(func(s sample) float64 { return s.mallocs }),
		bytes:   med(func(s sample) float64 { return s.bytes }),
		writes:  med(func(s sample) float64 { return s.writes }),
	}}
	lo, hi := ss[0].instr, ss[0].instr
	for _, s := range ss {
		lo, hi = min(lo, s.instr), max(hi, s.instr)
	}
	if p.instr != 0 {
		p.spread = (hi - lo) / p.instr
	}
	return p
}

// buildPrograms writes and builds both programs into scratch and returns
// the two binaries.
func buildPrograms(ergo, scratch string, cs []Case) (string, string, error) {
	ergoDir, goDir, binDir := filepath.Join(scratch, "ergo"), filepath.Join(scratch, "go"), filepath.Join(scratch, "bin")
	for _, d := range []string{ergoDir, goDir, binDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", "", err
		}
	}
	if err := os.WriteFile(filepath.Join(ergoDir, "main.ergo"), []byte(ergoProgram(cs)), 0o644); err != nil {
		return "", "", err
	}
	cmd := exec.Command(ergo, "build", "main.ergo", "-o", filepath.Join(binDir, "unhooked"), "-work")
	cmd.Dir = ergoDir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("ergo build: %w\n%s", err, stderr.String())
	}
	work := ""
	for _, line := range strings.Split(stderr.String(), "\n") {
		if w, ok := strings.CutPrefix(line, "WORK="); ok {
			work = strings.TrimSpace(w)
		}
	}
	if work == "" {
		return "", "", fmt.Errorf("ergo build printed no WORK= line")
	}
	// A copy of the emitted module: the emission cache stays untouched.
	mod := filepath.Join(scratch, "ergomod")
	if err := copyTree(work, mod); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(filepath.Join(mod, "zz_stdcount_hook.go"), []byte(allocHook), 0o644); err != nil {
		return "", "", err
	}
	ergoBin := filepath.Join(binDir, "stdcount-ergo")
	if err := goBuild(mod, ergoBin); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(filepath.Join(goDir, "go.mod"), []byte("module stdcount\n\ngo 1.24\n"), 0o644); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(filepath.Join(goDir, "main.go"), []byte(goProgram(cs)), 0o644); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(filepath.Join(goDir, "zz_stdcount_hook.go"), []byte(allocHook), 0o644); err != nil {
		return "", "", err
	}
	goBin := filepath.Join(binDir, "stdcount-go")
	if err := goBuild(goDir, goBin); err != nil {
		return "", "", err
	}
	return ergoBin, goBin, nil
}

func goBuild(dir, out string) error {
	cmd := exec.Command("go", "build", "-ldflags=-checklinkname=0", "-o", out, ".")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go build in %s: %w\n%s", dir, err, stderr.String())
	}
	return nil
}

// copyTree copies the regular files and directories below src into dst,
// which must not exist or must be an earlier copy (its files are
// overwritten).
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

func loadavg() string {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return "unknown"
	}
	f := strings.Fields(string(data))
	if len(f) < 3 {
		return "unknown"
	}
	return strings.Join(f[:3], " ")
}

// countUses counts each case's call texts in the examples' Ergo sources:
// the static weight of the case.
func countUses(repo string, cs []Case) ([]int, error) {
	var sources []string
	err := filepath.WalkDir(filepath.Join(repo, "examples"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".ergo") {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sources = append(sources, string(data))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]int, len(cs))
	for i, c := range cs {
		for _, s := range sources {
			for _, u := range c.Uses {
				out[i] += strings.Count(s, u)
			}
		}
	}
	return out, nil
}
