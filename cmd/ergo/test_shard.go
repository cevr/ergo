package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"ergo.local/prototype/internal/receipt"
	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/stage"
)

type shardSpec struct {
	enabled bool
	index   int
	total   int
}

type testShardOptions struct {
	parallel      bool
	jobs          int
	jobsSet       bool
	shard         shardSpec
	timingsPath   string
	updateTimings bool
	updatePath    string
}

func (o testShardOptions) active() bool {
	return o.parallel || o.jobsSet || o.shard.enabled || o.timingsPath != "" || o.updateTimings
}

func (o testShardOptions) timingsReadPath() string {
	if o.timingsPath != "" {
		return o.timingsPath
	}
	return o.updatePath
}

func (o testShardOptions) timingsWritePath() string {
	if o.updatePath != "" {
		return o.updatePath
	}
	return o.timingsPath
}

func parseShardSpec(s string) (shardSpec, error) {
	left, right, ok := strings.Cut(s, "/")
	if !ok || strings.Contains(right, "/") {
		return shardSpec{}, fmt.Errorf("--shard takes i/n (1 <= i <= n), not %q", s)
	}
	if left == "" || left[0] == '+' || right == "" || right[0] == '+' {
		return shardSpec{}, fmt.Errorf("--shard takes i/n with 1 <= i <= n, not %q", s)
	}
	i, err1 := strconv.Atoi(left)
	n, err2 := strconv.Atoi(right)
	if err1 != nil || err2 != nil || i < 1 || n < 1 || i > n {
		return shardSpec{}, fmt.Errorf("--shard takes i/n with 1 <= i <= n, not %q", s)
	}
	return shardSpec{enabled: true, index: i, total: n}, nil
}

func parseJobsCount(s string) (int, error) {
	n, err := parseStrictPositiveInt(s)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("-j/--jobs takes a positive integer >= 1, not %q", s)
	}
	return n, nil
}

func parseStrictPositiveInt(s string) (int, error) {
	if s == "" || s[0] == '+' || s[0] == '-' {
		return 0, fmt.Errorf("not a positive decimal integer")
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("not a positive decimal integer")
	}
	return n, nil
}

var errTargetFilteredOut = errors.New("filtered out")

func effectiveJobs(o testShardOptions) int {
	if o.jobsSet {
		return o.jobs
	}
	p := runtime.GOMAXPROCS(0)
	if p < 1 {
		p = 1
	}
	if p > 8 {
		p = 8
	}
	return p
}

type TestTimings struct {
	Version  int                `json:"version"`
	Packages map[string]float64 `json:"packages"`
}

func loadTestTimings(p string) (TestTimings, error) {
	empty := TestTimings{Version: 1, Packages: map[string]float64{}}
	if p == "" {
		return empty, nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return empty, nil
		}
		return TestTimings{}, fmt.Errorf("--timings %s: %w", p, err)
	}
	var t TestTimings
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		return TestTimings{}, fmt.Errorf("--timings %s: malformed JSON: %w", p, err)
	}
	if t.Version != 0 && t.Version != 1 {
		return TestTimings{}, fmt.Errorf("--timings %s: unsupported version %d", p, t.Version)
	}
	if t.Version == 0 {
		t.Version = 1
	}
	if t.Packages == nil {
		t.Packages = map[string]float64{}
	}
	for k, v := range t.Packages {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return TestTimings{}, fmt.Errorf("--timings %s: invalid duration %v for %q", p, v, k)
		}
	}
	return t, nil
}

func updateTestTimings(p string, measured map[string]float64) error {
	if p == "" {
		return nil
	}
	existing, err := loadTestTimings(p)
	if err != nil {
		return err
	}
	for k, v := range measured {
		existing.Packages[k] = roundSeconds(v)
	}
	existing.Version = 1
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("--update-timings %s: %w", p, err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(existing); err != nil {
		return fmt.Errorf("--update-timings %s: %w", p, err)
	}
	tmp, err := os.CreateTemp(dir, ".ergo-timings-*")
	if err != nil {
		return fmt.Errorf("--update-timings %s: %w", p, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("--update-timings %s: %w", p, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("--update-timings %s: %w", p, err)
	}
	if err := os.Rename(tmpName, p); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("--update-timings %s: %w", p, err)
	}
	return nil
}

func roundSeconds(s float64) float64 {
	if s < 0 {
		s = 0
	}
	return math.Round(s*1000) / 1000
}

type testTarget struct {
	label     string
	dir       string
	modRoot   string
	timingKey string
	isFile    bool
}

func (t testTarget) localityGroup() string {
	if t.modRoot != "" {
		return filepath.ToSlash(filepath.Clean(t.modRoot)) + "|" + path.Dir(t.timingKey)
	}
	return path.Dir(t.timingKey)
}

func medianWeight(timings TestTimings, targets []testTarget) float64 {
	var vals []float64
	for _, t := range targets {
		if d := timings.Packages[t.timingKey]; d > 0 {
			vals = append(vals, d)
		}
	}
	if len(vals) == 0 {
		return 1.0
	}
	slices.Sort(vals)
	mid := len(vals) / 2
	if len(vals)%2 == 1 {
		return vals[mid]
	}
	return (vals[mid-1] + vals[mid]) / 2.0
}

func planShards(targets []testTarget, total int, timings TestTimings) [][]testTarget {
	if total <= 1 {
		return [][]testTarget{slices.Clone(targets)}
	}
	shards := make([][]testTarget, total)
	if len(targets) == 0 {
		return shards
	}

	type weighted struct {
		target testTarget
		weight float64
		order  int
	}
	fallback := medianWeight(timings, targets)
	items := make([]weighted, len(targets))
	for i, t := range targets {
		w := timings.Packages[t.timingKey]
		if w <= 0 {
			w = fallback
		}
		items[i] = weighted{target: t, weight: w, order: i}
	}

	// Longest-Processing-Time-First (descending weight), breaking equal-weight
	// ties by (localityGroup, timingKey, dir, label) so sibling packages stay
	// adjacent for cache-local contiguous assignment.
	slices.SortFunc(items, func(a, b weighted) int {
		if n := cmp.Compare(b.weight, a.weight); n != 0 {
			return n
		}
		if n := cmp.Compare(a.target.localityGroup(), b.target.localityGroup()); n != 0 {
			return n
		}
		if n := cmp.Compare(a.target.timingKey, b.target.timingKey); n != 0 {
			return n
		}
		if n := cmp.Compare(a.target.dir, b.target.dir); n != 0 {
			return n
		}
		return cmp.Compare(a.target.label, b.target.label)
	})

	loads := make([]float64, total)
	counts := make([]int, total)
	lightest := func() int {
		best := 0
		for s := 1; s < total; s++ {
			if loads[s] < loads[best] || (loads[s] == loads[best] && counts[s] < counts[best]) {
				best = s
			}
		}
		return best
	}

	for start := 0; start < len(items); {
		end := start + 1
		for end < len(items) && items[end].weight == items[start].weight {
			end++
		}
		tier := items[start:end]
		quota := make([]int, total)
		for _, it := range tier {
			s := lightest()
			quota[s]++
			loads[s] += it.weight
			counts[s]++
		}
		cursor := 0
		for s := 0; s < total; s++ {
			q := quota[s]
			if q == 0 {
				continue
			}
			for _, it := range tier[cursor : cursor+q] {
				shards[s] = append(shards[s], it.target)
			}
			cursor += q
		}
		start = end
	}

	// Restore deterministic target order inside each shard.
	origOrder := make(map[string]int, len(targets))
	for i, t := range targets {
		origOrder[t.timingKey+"\x00"+t.dir+"\x00"+t.label] = i
	}
	for s := range shards {
		slices.SortFunc(shards[s], func(a, b testTarget) int {
			ka := origOrder[a.timingKey+"\x00"+a.dir+"\x00"+a.label]
			kb := origOrder[b.timingKey+"\x00"+b.dir+"\x00"+b.label]
			return cmp.Compare(ka, kb)
		})
	}
	return shards
}

func expandTestTargets(opts fileOptions) ([]testTarget, bool, error) {
	rawTargets := append([]string{opts.file}, opts.more...)
	multiMode := len(rawTargets) > 1 || recursivePattern(opts.file) || opts.sharding.shard.enabled
	var out []testTarget
	for _, t := range rawTargets {
		if recursivePattern(t) {
			pkgs, err := sketchfront.DiscoverPackages(filepath.Dir(filepath.Clean(t)))
			if err != nil {
				return nil, multiMode, invalidInvocation(err)
			}
			for _, pkg := range pkgs {
				out = append(out, testTarget{
					label:     pkg.Path,
					dir:       pkg.Dir,
					modRoot:   pkg.Root,
					timingKey: pkg.Path,
				})
			}
			continue
		}
		if isPackageFolder(t) {
			modRoot, importPath := discoverSinglePackageIdentity(t)
			key := importPath
			if key == "" {
				key = filepath.ToSlash(filepath.Clean(t))
			}
			out = append(out, testTarget{
				label:     t,
				dir:       t,
				modRoot:   modRoot,
				timingKey: key,
			})
			continue
		}
		out = append(out, testTarget{
			label:     t,
			dir:       t,
			timingKey: filepath.ToSlash(filepath.Clean(t)),
			isFile:    true,
		})
	}
	return out, multiMode, nil
}

func discoverSinglePackageIdentity(dir string) (modRoot, importPath string) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", ""
	}
	pkgs, err := sketchfront.DiscoverPackages(abs)
	if err == nil {
		for _, p := range pkgs {
			if filepath.Clean(p.Dir) == filepath.Clean(abs) {
				return p.Root, p.Path
			}
		}
	}
	return "", ""
}

type targetOutcome struct {
	stdout   bytes.Buffer
	stderr   bytes.Buffer
	err      error
	duration time.Duration
}

type sharedTargetRunner struct {
	run func(outW, errW io.Writer) error
}

func prepareSharedModuleRunners(opts fileOptions, selected []testTarget, multiMode bool, errW io.Writer) (map[int]sharedTargetRunner, func()) {
	if len(selected) < 2 || opts.work || opts.explore || opts.search.mode != "" || opts.pruneCases {
		return nil, func() {}
	}
	module := moduleTestOptions{law: opts.law, receiptDir: opts.receipt}
	byMod := map[string][]int{}
	var modOrder []string
	for i, t := range selected {
		if t.isFile || t.modRoot == "" {
			continue
		}
		if _, seen := byMod[t.modRoot]; !seen {
			modOrder = append(modOrder, t.modRoot)
		}
		byMod[t.modRoot] = append(byMod[t.modRoot], i)
	}
	platform, err := platformOf("", false, os.Getenv("GOOS"), os.Getenv("GOARCH"))
	if err != nil {
		return nil, func() {}
	}
	root, err := build.DefaultRoot()
	if err != nil {
		return nil, func() {}
	}
	tc, err := buildAction(opts)
	if err != nil {
		return nil, func() {}
	}

	var cleanupDirs []string
	cleanup := func() {
		for _, d := range cleanupDirs {
			_ = os.RemoveAll(d)
		}
	}
	runners := map[int]sharedTargetRunner{}

	for _, modRoot := range modOrder {
		idxs := byMod[modRoot]
		if len(idxs) < 2 {
			continue
		}
		sources := make([]receipt.Location, len(idxs))
		dirs := make([]string, len(idxs))
		ok := true
		for k, idx := range idxs {
			src, err := receipt.LocateSource(selected[idx].dir)
			if err != nil {
				ok = false
				break
			}
			sources[k] = src
			dirs[k] = src.Admitted()
		}
		if !ok {
			continue
		}
		analyses, err := sketchfront.CheckPackages(dirs, platform)
		if err != nil || len(analyses) != len(idxs) {
			continue
		}
		loads := make([]loaded, len(idxs))
		var built []sketchfront.Analysis
		builtIdx := make([]int, len(idxs))
		for k, a := range analyses {
			uri, uerr := report.FileURI(dirs[k])
			if uerr != nil {
				ok = false
				break
			}
			loads[k] = loaded{
				source:   sources[k],
				name:     filepath.Base(dirs[k]),
				snapshot: report.SourceSnapshot{URI: uri, Origin: "disk", Files: a.Files()},
				analysis: a,
				result:   a.Result(a.Text(), opts.target),
			}
			builtIdx[k] = -1
			if !loads[k].result.Checked {
				if len(a.Diagnostics) == 0 {
					ok = false
					break
				}
				continue
			}
			if !nothingToTest(a.Root().Package) {
				builtIdx[k] = len(built)
				built = append(built, a)
			}
		}
		if !ok {
			continue
		}
		tmpDir, err := os.MkdirTemp("", "ergo-shard-test-")
		if err != nil {
			continue
		}
		cleanupDirs = append(cleanupDirs, tmpDir)
		binary := filepath.Join(tmpDir, "module.test")
		if len(built) > 0 {
			var held bytes.Buffer
			if _, err := sketchfront.BuildTests(built, sketchfront.BuildRequest{
				Output:     binary,
				Cache:      build.Cache{Root: root},
				Toolchain:  tc,
				IR:         true,
				NoFallback: fallbackOff(opts),
				Stderr:     &held,
			}); err != nil {
				if os.Getenv(SharedFallbackVariable) == "fail" {
					_, _ = sharedFallback(errW, true, err, held.String())
				}
				continue
			}
			stage.Add(stage.SharedTestModules, 1)
		}
		for k, idx := range idxs {
			one := opts
			one.file, one.more = selected[idx].dir, nil
			one.sharding = testShardOptions{}
			if multiMode {
				one.law, one.receipt = "", ""
			}
			l := loads[k]
			bi := builtIdx[k]
			bin := binary
			runners[idx] = sharedTargetRunner{
				run: func(outW, errW io.Writer) error {
					if !l.result.Checked {
						return refuseUncheckedIO(l, one, outW, errW)
					}
					if multiMode && (module.law != "" || module.receiptDir != "") {
						keepPkg, err := module.select_(&one, l)
						if err != nil {
							return err
						}
						if !keepPkg {
							return errTargetFilteredOut
						}
					}
					selOpts, runPkg, matched, err := resolveTestSelection(one, l)
					if err != nil {
						return invalidInvocation(err)
					}
					if selOpts.filter != "" && !matched {
						return errTargetFilteredOut
					}
					if !runPkg {
						return emptyTestRun(selOpts, l.result.Revision, outW, errW)
					}
					one = selOpts
					if bi < 0 {
						err := emptyTestRun(one, l.result.Revision, outW, errW)
						if err == nil && one.law == "" && one.skipLaw == "" && one.run == "" && one.filter == "" && one.skip == "" && one.selectNames == "" && !one.explore {
							err = writeLedgerIO(one, l, outW, errW)
						}
						return err
					}
					if answered, err := evaluatedTestRun(one, l, outW, errW, func([]byte) {}); answered {
						return err
					}
					return runTestBinaryIO(one, l, tc, bin, []string{lower.TestPackageFlag, strconv.Itoa(bi)}, outW, errW, nil)
				},
			}
		}
	}
	return runners, cleanup
}

func runTargetsParallel(selected []testTarget, jobs int, timings TestTimings, execTarget func(i int, outW, errW io.Writer) error) []targetOutcome {
	outcomes := make([]targetOutcome, len(selected))
	if len(selected) == 0 {
		return outcomes
	}
	if jobs <= 1 || len(selected) == 1 {
		for i := range selected {
			start := time.Now()
			outcomes[i].err = execTarget(i, &outcomes[i].stdout, &outcomes[i].stderr)
			outcomes[i].duration = time.Since(start)
		}
		return outcomes
	}

	// Schedule heaviest estimated packages first across the worker pool to
	// minimize tail straggler latency.
	order := make([]int, len(selected))
	for i := range selected {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int {
		wa := timings.Packages[selected[a].timingKey]
		wb := timings.Packages[selected[b].timingKey]
		if n := cmp.Compare(wb, wa); n != 0 {
			return n
		}
		return cmp.Compare(a, b)
	})

	workers := jobs
	if workers > len(selected) {
		workers = len(selected)
	}
	workCh := make(chan int, len(order))
	for _, idx := range order {
		workCh <- idx
	}
	close(workCh)

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for idx := range workCh {
				start := time.Now()
				outcomes[idx].err = execTarget(idx, &outcomes[idx].stdout, &outcomes[idx].stderr)
				outcomes[idx].duration = time.Since(start)
			}
		}()
	}
	wg.Wait()
	return outcomes
}

func aggregateOutcomes(selected []testTarget, outcomes []targetOutcome, printPkgHeader bool, stdout, stderr io.Writer) error {
	failed := false
	for i, t := range selected {
		if errors.Is(outcomes[i].err, errTargetFilteredOut) {
			continue
		}
		if printPkgHeader {
			fmt.Fprintf(stdout, "package %s\n", t.label)
		}
		_, _ = stdout.Write(outcomes[i].stdout.Bytes())
		_, _ = stderr.Write(outcomes[i].stderr.Bytes())
		stop, fail := packageOutcomeIO(t.label, outcomes[i].err, stderr)
		if stop != nil {
			return stop
		}
		if fail {
			failed = true
		}
	}
	if failed {
		return exitStatusError{code: 1, handled: true}
	}
	return nil
}

func shardedTestCommand(opts fileOptions, stdout, stderr io.Writer) error {
	timings, err := loadTestTimings(opts.sharding.timingsReadPath())
	if err != nil {
		return invalidInvocation(err)
	}
	targets, multiMode, err := expandTestTargets(opts)
	if err != nil {
		return err
	}
	selected := targets
	if opts.sharding.shard.enabled {
		shards := planShards(targets, opts.sharding.shard.total, timings)
		selected = shards[opts.sharding.shard.index-1]
		if len(selected) == 0 {
			fmt.Fprintf(stdout, "shard %d/%d: 0 packages\n", opts.sharding.shard.index, opts.sharding.shard.total)
			return nil
		}
	}

	sharedRunners, cleanup := prepareSharedModuleRunners(opts, selected, multiMode, stderr)
	defer cleanup()

	jobs := effectiveJobs(opts.sharding)
	outcomes := runTargetsParallel(selected, jobs, timings, func(i int, outW, errW io.Writer) error {
		if r, ok := sharedRunners[i]; ok {
			return r.run(outW, errW)
		}
		one := opts
		one.file, one.more = selected[i].dir, nil
		one.sharding = testShardOptions{}
		if selected[i].isFile {
			return testPackageIO(one, outW, errW)
		}
		l, err := loadForPlatform(one.file, one.target, one.platform)
		if err != nil {
			return err
		}
		if !l.result.Checked {
			return refuseUncheckedIO(l, one, outW, errW)
		}
		if multiMode && (opts.law != "" || opts.receipt != "") {
			mod := moduleTestOptions{law: opts.law, receiptDir: opts.receipt}
			one.law, one.receipt = "", ""
			keepPkg, serr := mod.select_(&one, l)
			if serr != nil {
				return serr
			}
			if !keepPkg {
				return errTargetFilteredOut
			}
		}
		if one.filter != "" {
			selOpts, runPkg, matched, ferr := resolveTestSelection(one, l)
			if ferr != nil {
				return invalidInvocation(ferr)
			}
			if !matched {
				return errTargetFilteredOut
			}
			if !runPkg {
				return emptyTestRun(selOpts, l.result.Revision, outW, errW)
			}
			one = selOpts
		}
		return testLoadedPackageIO(one, l, outW, errW, nil)
	})

	runErr := aggregateOutcomes(selected, outcomes, multiMode, stdout, stderr)
	if runErr == nil && (opts.law != "" || opts.filter != "") && len(selected) > 0 {
		anyMatched := false
		for _, o := range outcomes {
			if !errors.Is(o.err, errTargetFilteredOut) {
				anyMatched = true
				break
			}
		}
		if !anyMatched {
			if opts.law != "" {
				return fmt.Errorf("no law %s in selected packages (--law)", opts.law)
			}
			return fmt.Errorf("no test or law matches --filter %q in selected packages", opts.filter)
		}
	}

	if opts.sharding.updateTimings {
		measured := make(map[string]float64, len(selected))
		for i, t := range selected {
			measured[t.timingKey] = outcomes[i].duration.Seconds()
		}
		if uerr := updateTestTimings(opts.sharding.timingsWritePath(), measured); uerr != nil && runErr == nil {
			return uerr
		}
	}
	return runErr
}
