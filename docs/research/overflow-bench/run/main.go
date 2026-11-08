// Command run measures the overflow-bench kernels for issue #108 and writes
// the receipts. Run it from the repository root:
//
//	go run ./docs/research/overflow-bench/run
//
// It writes into docs/research/overflow-bench/results/<UTC stamp>/:
//
//   - env.txt: head commit, Go version, kernel, CPU, load at start.
//   - tests.txt: the agreement and overflow-control tests, run before any
//     timing; a failure stops the run.
//   - count10.txt: `go test -bench . -benchmem -count 10` output, each
//     benchmark's samples back to back.
//   - interleaved.txt and samples.tsv: one process per sample, every round
//     in a fresh shuffled order, with the one-minute load before each sample.
//   - loads.tsv: /proc/loadavg every 10 seconds, by phase.
//   - asm.txt and inline.txt: `go build -gcflags=-S` for the kernels and
//     `-gcflags=-m` inlining decisions.
//   - summary.md: median, range, ratio to wrap and paired per-round ratios.
//
// This is decision evidence for #81, not a performance claim.
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const pkg = "./docs/research/overflow-bench"

func main() {
	out := flag.String("out", "", "result directory (default docs/research/overflow-bench/results/<UTC stamp>)")
	rounds := flag.Int("rounds", 10, "interleaved rounds, one sample per benchmark each")
	benchtime := flag.String("benchtime", "1s", "benchtime per sample")
	count := flag.Int("count", 10, "samples per benchmark in the contiguous run (0 skips it)")
	summarize := flag.Bool("summarize", false, "only rewrite summary.md from the receipts already in -out")
	flag.Parse()
	if *out == "" {
		*out = filepath.Join("docs/research/overflow-bench/results", time.Now().UTC().Format("20060102T150405Z"))
	}
	var err error
	if *summarize {
		err = writeSummary(*out, *count)
	} else {
		err = run(*out, *rounds, *count, *benchtime)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		os.Exit(1)
	}
	fmt.Println("results in", *out)
}

func run(out string, rounds, count int, benchtime string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	loads := newLoadSampler(filepath.Join(out, "loads.tsv"))
	defer loads.stop()

	loads.phase("env")
	if err := writeEnv(filepath.Join(out, "env.txt")); err != nil {
		return err
	}

	tmp, err := os.MkdirTemp("", "overflow-bench-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	bin := filepath.Join(tmp, "overflow-bench.test")
	if _, err := command("go", "test", "-c", "-o", bin, pkg); err != nil {
		return err
	}

	// Agreement and overflow controls before any timing.
	loads.phase("tests")
	tests, err := command(bin, "-test.run", ".", "-test.count", "1", "-test.v")
	if werr := os.WriteFile(filepath.Join(out, "tests.txt"), tests, 0o644); werr != nil {
		return werr
	}
	if err != nil {
		return fmt.Errorf("tests failed; no timing recorded: %w", err)
	}

	listing, err := command(bin, "-test.run", "^$", "-test.bench", ".", "-test.benchtime", "1x")
	if err != nil {
		return err
	}
	names := benchNames(listing)
	if len(names) == 0 {
		return fmt.Errorf("no benchmarks found")
	}

	if count > 0 {
		loads.phase("count10")
		b, err := command(bin, "-test.run", "^$", "-test.bench", ".", "-test.benchmem",
			"-test.count", strconv.Itoa(count), "-test.benchtime", benchtime)
		if werr := os.WriteFile(filepath.Join(out, "count10.txt"), b, 0o644); werr != nil {
			return werr
		}
		if err != nil {
			return err
		}
	}

	loads.phase("interleaved")
	var inter bytes.Buffer
	var tsv bytes.Buffer
	tsv.WriteString("round\torder\tname\tns_op\tb_op\tallocs_op\tload1\n")
	for r := range rounds {
		order := slices.Clone(names)
		rng := rand.New(rand.NewPCG(0x108, uint64(r)))
		rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		for i, name := range order {
			load := load1()
			b, err := command(bin, "-test.run", "^$", "-test.bench", anchored(name), "-test.benchmem",
				"-test.count", "1", "-test.benchtime", benchtime)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			samples := parseBench(b)
			if len(samples) != 1 || samples[0].name != name {
				return fmt.Errorf("%s: expected one sample, got %d", name, len(samples))
			}
			s := samples[0]
			inter.WriteString(s.line + "\n")
			fmt.Fprintf(&tsv, "%d\t%d\t%s\t%g\t%d\t%d\t%.2f\n", r, i, name, s.ns, s.bytes, s.allocs, load)
		}
	}
	if err := os.WriteFile(filepath.Join(out, "interleaved.txt"), inter.Bytes(), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "samples.tsv"), tsv.Bytes(), 0o644); err != nil {
		return err
	}

	loads.phase("asm")
	if err := writeAsm(out, root); err != nil {
		return err
	}
	loads.stop()

	return writeSummary(out, count)
}

// ---- commands ---------------------------------------------------------------

func command(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if err != nil {
		return buf.Bytes(), fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, tail(buf.String(), 2000))
	}
	return buf.Bytes(), nil
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func writeEnv(path string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "date: %s\n", time.Now().UTC().Format(time.RFC3339))
	if head, err := command("git", "--no-optional-locks", "rev-parse", "HEAD"); err == nil {
		fmt.Fprintf(&b, "head: %s", head)
	}
	// The result directories are excluded: the run's own directory is
	// untracked while it runs.
	if dirty, err := command("git", "--no-optional-locks", "status", "--porcelain", "--", "docs/research/overflow-bench",
		":(exclude)docs/research/overflow-bench/results"); err == nil {
		fmt.Fprintf(&b, "package clean (results excluded): %v\n", len(bytes.TrimSpace(dirty)) == 0)
		if s := strings.TrimSpace(string(dirty)); s != "" {
			fmt.Fprintf(&b, "package changes:\n%s\n", s)
		}
	}
	if v, err := command("go", "version"); err == nil {
		fmt.Fprintf(&b, "go: %s", v)
	}
	if v, err := command("go", "env", "GOAMD64", "GOARCH", "GOOS"); err == nil {
		fmt.Fprintf(&b, "go env GOAMD64 GOARCH GOOS: %s\n", strings.Join(strings.Fields(string(v)), " "))
	}
	if v, err := command("uname", "-srm"); err == nil {
		fmt.Fprintf(&b, "kernel: %s", v)
	}
	fmt.Fprintf(&b, "cpu: %s\n", cpuModel())
	fmt.Fprintf(&b, "logical cpus: %d\n", runtime.NumCPU())
	if l, err := os.ReadFile("/proc/loadavg"); err == nil {
		fmt.Fprintf(&b, "loadavg at start: %s", l)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func cpuModel() string {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return "unknown"
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), ":"); ok && strings.TrimSpace(k) == "model name" {
			return strings.TrimSpace(v)
		}
	}
	return "unknown"
}

func load1() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return -1
	}
	f, err := strconv.ParseFloat(strings.Fields(string(b))[0], 64)
	if err != nil {
		return -1
	}
	return f
}

// ---- load sampling ----------------------------------------------------------

type loadSampler struct {
	mu      sync.Mutex
	current string
	f       *os.File
	done    chan struct{}
	once    sync.Once
	wg      sync.WaitGroup
}

func newLoadSampler(path string) *loadSampler {
	f, err := os.Create(path)
	if err != nil {
		panic(err)
	}
	fmt.Fprintln(f, "utc\tphase\tload1\tload5\tload15")
	s := &loadSampler{f: f, done: make(chan struct{}), current: "start"}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			s.sample()
			select {
			case <-s.done:
				return
			case <-t.C:
			}
		}
	}()
	return s
}

func (s *loadSampler) phase(p string) {
	s.mu.Lock()
	s.current = p
	s.mu.Unlock()
	s.sample()
}

func (s *loadSampler) sample() {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return
	}
	fs := strings.Fields(string(b))
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f != nil {
		fmt.Fprintf(s.f, "%s\t%s\t%s\t%s\t%s\n", time.Now().UTC().Format(time.RFC3339), s.current, fs[0], fs[1], fs[2])
	}
}

func (s *loadSampler) stop() {
	s.once.Do(func() {
		close(s.done)
		s.wg.Wait()
		s.mu.Lock()
		s.f.Close()
		s.f = nil
		s.mu.Unlock()
	})
}

// ---- benchmark output -------------------------------------------------------

type sample struct {
	name          string
	ns            float64
	bytes, allocs int64
	line          string
}

var benchLine = regexp.MustCompile(`^(Benchmark\S+?)(?:-\d+)?\s+\d+\s+([0-9.]+) ns/op(?:\s+(\d+) B/op\s+(\d+) allocs/op)?`)

func parseBench(b []byte) []sample {
	var out []sample
	for _, line := range strings.Split(string(b), "\n") {
		m := benchLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ns, _ := strconv.ParseFloat(m[2], 64)
		s := sample{name: m[1], ns: ns, line: line}
		if m[3] != "" {
			s.bytes, _ = strconv.ParseInt(m[3], 10, 64)
			s.allocs, _ = strconv.ParseInt(m[4], 10, 64)
		}
		out = append(out, s)
	}
	return out
}

func benchNames(listing []byte) []string {
	var names []string
	for _, s := range parseBench(listing) {
		if !slices.Contains(names, s.name) {
			names = append(names, s.name)
		}
	}
	return names
}

// anchored turns BenchmarkA/b/c into ^BenchmarkA$/^b$/^c$.
func anchored(name string) string {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		parts[i] = "^" + regexp.QuoteMeta(p) + "$"
	}
	return strings.Join(parts, "/")
}

// ---- assembly ----------------------------------------------------------------

var asmNoise = regexp.MustCompile(`PCDATA|FUNCDATA|^\s+0x[0-9a-f]{4} [0-9a-f]{2} |^\s+rel `)

func writeAsm(out, root string) error {
	asm, err := command("go", "build", "-a", "-gcflags=-S", pkg)
	if err != nil {
		return err
	}
	inl, err := command("go", "build", "-a", "-gcflags=-m", pkg)
	if err != nil {
		return err
	}
	clean := func(b []byte) string {
		s := strings.ReplaceAll(string(b), root+"/docs/research/overflow-bench/", "")
		s = strings.ReplaceAll(s, root+"/", "")
		return strings.ReplaceAll(s, "ergo.local/prototype/docs/research/overflow-bench.", "ob.")
	}
	var keep strings.Builder
	in := false
	for _, line := range strings.Split(clean(asm), "\n") {
		if strings.Contains(line, " STEXT") {
			name := strings.Fields(line)[0]
			in = regexp.MustCompile(`^ob\.(Sum|Hash|Op)[A-Za-z]+$`).MatchString(name)
		}
		if in && !asmNoise.MatchString(line) {
			keep.WriteString(line + "\n")
		}
	}
	if err := os.WriteFile(filepath.Join(out, "asm.txt"), []byte(keep.String()), 0o644); err != nil {
		return err
	}
	var decisions strings.Builder
	for _, line := range strings.Split(clean(inl), "\n") {
		if strings.Contains(line, "inline") {
			decisions.WriteString(line + "\n")
		}
	}
	return os.WriteFile(filepath.Join(out, "inline.txt"), []byte(decisions.String()), 0o644)
}

// ---- summary -----------------------------------------------------------------

func writeSummary(out string, count int) error {
	var b strings.Builder
	env, _ := os.ReadFile(filepath.Join(out, "env.txt"))
	fmt.Fprintf(&b, "# #108 overflow-bench run %s\n\n```\n%s```\n\n", filepath.Base(out), env)
	b.WriteString(loadSummary(filepath.Join(out, "loads.tsv")))
	var canonical []string
	if raw, err := os.ReadFile(filepath.Join(out, "count10.txt")); err == nil {
		fmt.Fprintf(&b, "\n## Contiguous run (-count %d)\n\n", count)
		canonical = benchNames(raw)
		b.WriteString(table(parseBench(raw), nil, canonical))
	}
	raw, err := os.ReadFile(filepath.Join(out, "interleaved.txt"))
	if err != nil {
		return err
	}
	tsv, err := os.ReadFile(filepath.Join(out, "samples.tsv"))
	if err != nil {
		return err
	}
	b.WriteString("\n## Interleaved run (one process per sample, shuffled each round)\n\n")
	b.WriteString(table(parseBench(raw), pairedRatios(tsv), canonical))
	return os.WriteFile(filepath.Join(out, "summary.md"), []byte(b.String()), 0o644)
}

func loadSummary(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	byPhase := map[string][]float64{}
	var phases []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n")[1:] {
		f := strings.Split(line, "\t")
		if len(f) < 3 {
			continue
		}
		v, err := strconv.ParseFloat(f[2], 64)
		if err != nil {
			continue
		}
		if _, ok := byPhase[f[1]]; !ok {
			phases = append(phases, f[1])
		}
		byPhase[f[1]] = append(byPhase[f[1]], v)
	}
	var b strings.Builder
	b.WriteString("## Host load (one-minute load average, sampled every 10 s)\n\n| Phase | Samples | Median | Min | Max |\n| --- | ---: | ---: | ---: | ---: |\n")
	for _, p := range phases {
		vs := byPhase[p]
		fmt.Fprintf(&b, "| %s | %d | %.2f | %.2f | %.2f |\n", p, len(vs), median(vs), slices.Min(vs), slices.Max(vs))
	}
	return b.String()
}

type cell struct {
	ns            []float64
	bytes, allocs int64
}

// baseline names the wrap sibling of a benchmark.
func baseline(name string) string {
	i := strings.LastIndex(name, "/")
	if i < 0 {
		return ""
	}
	return name[:i] + "/wrap"
}

// table orders rows by canonical when it names them (the contiguous run is
// in declaration order), else by first appearance.
func table(samples []sample, paired map[string][]float64, canonical []string) string {
	cells := map[string]*cell{}
	var order []string
	for _, s := range samples {
		c, ok := cells[s.name]
		if !ok {
			c = &cell{}
			cells[s.name] = c
			order = append(order, s.name)
		}
		c.ns = append(c.ns, s.ns)
		c.bytes, c.allocs = s.bytes, s.allocs
	}
	rank := func(name string) int {
		if i := slices.Index(canonical, name); i >= 0 {
			return i
		}
		return len(canonical) + slices.Index(order, name)
	}
	slices.SortStableFunc(order, func(a, b string) int { return rank(a) - rank(b) })
	var b strings.Builder
	b.WriteString("| Benchmark | n | Median ns/op | Min | Max | Median vs wrap |")
	if paired != nil {
		b.WriteString(" Paired ratio median [min, max] |")
	}
	b.WriteString(" B/op | allocs/op |\n| --- | ---: | ---: | ---: | ---: | ---: |")
	if paired != nil {
		b.WriteString(" ---: |")
	}
	b.WriteString(" ---: | ---: |\n")
	for _, name := range order {
		c := cells[name]
		ratio := "-"
		if base, ok := cells[baseline(name)]; ok {
			ratio = fmt.Sprintf("%.2fx", median(c.ns)/median(base.ns))
		}
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %s |", strings.TrimPrefix(name, "Benchmark"), len(c.ns),
			num(median(c.ns)), num(slices.Min(c.ns)), num(slices.Max(c.ns)), ratio)
		if paired != nil {
			if rs := paired[name]; len(rs) > 0 {
				fmt.Fprintf(&b, " %.2fx [%.2f, %.2f] |", median(rs), slices.Min(rs), slices.Max(rs))
			} else {
				b.WriteString(" - |")
			}
		}
		fmt.Fprintf(&b, " %d | %d |\n", c.bytes, c.allocs)
	}
	b.WriteString("\nRaw ns/op samples, in run order:\n\n```\n")
	for _, name := range order {
		var vs []string
		for _, v := range cells[name].ns {
			vs = append(vs, num(v))
		}
		fmt.Fprintf(&b, "%s: %s\n", strings.TrimPrefix(name, "Benchmark"), strings.Join(vs, " "))
	}
	b.WriteString("```\n")
	return b.String()
}

// pairedRatios divides each sample by the wrap sample of the same round.
func pairedRatios(tsv []byte) map[string][]float64 {
	type key struct {
		round string
		name  string
	}
	ns := map[key]float64{}
	var rows [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(tsv)), "\n")[1:] {
		f := strings.Split(line, "\t")
		v, _ := strconv.ParseFloat(f[3], 64)
		ns[key{f[0], f[2]}] = v
		rows = append(rows, f)
	}
	out := map[string][]float64{}
	for _, f := range rows {
		base, ok := ns[key{f[0], baseline(f[2])}]
		if !ok || base == 0 {
			continue
		}
		out[f[2]] = append(out[f[2]], ns[key{f[0], f[2]}]/base)
	}
	return out
}

func median(vs []float64) float64 {
	s := slices.Clone(vs)
	sort.Float64s(s)
	n := len(s)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func num(v float64) string {
	if v >= 100 {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}
