package main

import (
	"bufio"
	"cmp"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Alpha (CI-SELECT CS-ALPHA): `go run ./scripts/mutants -alpha [-o FILE]
// [-fail-logs DIR] PACKAGE...` prints one row per Go test of the
// packages: its class, seconds, unique kills (rows it alone kills), total
// kills, change coupling and failures, ranked as a deletion list: class
// row with no unique kill first, by seconds, then by coupling. It is
// reported, never enforced by the gate.
//
// The kills come from the kill matrix (killmatrix.go): every row of the
// packages' ledgers, and every row of another ledger that names one of
// their tests, is judged with every candidate run (Config.All), not until
// the first kill. A row whose lines no test of the packages covers gives
// them no kill and is not run.

// alphaRow is one test's line of the table.
type alphaRow struct {
	Package, Test, Class string
	Seconds              float64
	Unique, Total        int
	Coupling, Failures   int
	// Held counts the rows the test is a candidate of that were not
	// judged (red on the unmutated tree, invalid, interrupted): its kills
	// are not known, so it is never on the deletion list.
	Held int
}

// deletable says whether the row is on the deletion list.
func (r alphaRow) deletable() bool { return r.Class == "row" && r.Unique == 0 && r.Held == 0 }

// alphaClasses are the name patterns of the test classes, in order; a
// test no pattern matches is a row. The allow file overrides them.
var alphaClasses = []struct {
	class   string
	pattern *regexp.Regexp
}{
	{"law", regexp.MustCompile(`Laws?([A-Z_]|$)|Lawful`)},
	{"model", regexp.MustCompile(`Model([A-Z_]|$)`)},
	{"property", regexp.MustCompile(`Propert(y|ies)|Holds|Seeded|Fuzz`)},
	{"agreement", regexp.MustCompile(`Agrees?([A-Z_]|$)|Matches|SameAs|Equals`)},
	{"example", regexp.MustCompile(`Examples?([A-Z_]|$)`)},
}

// alphaAllowFile is the reviewed class of each test the patterns place
// wrong: lines `DIR:TestName class # why`.
const alphaAllowFile = "scripts/mutants/testdata/alpha-classes.txt"

func readAllow(root string) (map[string]string, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(alphaAllowFile)))
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for n, line := range strings.Split(string(data), "\n") {
		line, _, _ = strings.Cut(line, "#")
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 || !strings.Contains(fields[0], ":") {
			return nil, fmt.Errorf("%s:%d: want `DIR:TestName class`", alphaAllowFile, n+1)
		}
		out[fields[0]] = fields[1]
	}
	return out, nil
}

// classify is the class of dir's test name.
func classify(allow map[string]string, dir, name string) string {
	if c, ok := allow[dir+":"+name]; ok {
		return c
	}
	for _, c := range alphaClasses {
		if c.pattern.MatchString(name) {
			return c.class
		}
	}
	return "row"
}

// alphaKills counts each target test's unique and total kills and held
// rows over the matrix.
func alphaKills(matrix []killRow, rows map[string]*alphaRow) {
	for _, k := range matrix {
		judged := k.Status == Killed || k.Status == Survived
		for _, c := range k.Candidates {
			if r := rows[c]; r != nil && !judged {
				r.Held++
			}
		}
		for _, id := range k.Killers {
			if r := rows[id]; r != nil {
				r.Total++
				if len(k.Killers) == 1 {
					r.Unique++
				}
			}
		}
	}
}

// rankAlpha orders the table: the deletion list first, by seconds then
// coupling, both descending; then the rest by class, unique kills, then
// seconds descending.
func rankAlpha(rows []alphaRow) {
	slices.SortStableFunc(rows, func(a, b alphaRow) int {
		if a.deletable() != b.deletable() {
			if a.deletable() {
				return -1
			}
			return 1
		}
		if !a.deletable() {
			if c := cmp.Compare(a.Class, b.Class); c != 0 {
				return c
			}
			if c := cmp.Compare(a.Unique, b.Unique); c != 0 {
				return c
			}
		}
		return cmp.Or(cmp.Compare(b.Seconds, a.Seconds), cmp.Compare(b.Coupling, a.Coupling), cmp.Compare(a.Package, b.Package), cmp.Compare(a.Test, b.Test))
	})
}

// alphaMatrix judges the matrix rows of jobs that a target test can
// kill, taking from each ledger's kills.json the rows whose key holds,
// and writes the matrix back. cfg is the run's, scoped to jobs; target
// says whether a test id is one of the packages'.
func alphaMatrix(ctx context.Context, cfg Config, jobs []Job, target func(string) bool) ([]killRow, *coverage, error) {
	cov, err := Measure(ctx, cfg, jobs)
	if err != nil {
		return nil, nil, err
	}
	type pending struct {
		job        int
		m          *Mutant
		candidates []string
	}
	var rows []pending
	var keyed []*Mutant
	for i, j := range jobs {
		for _, m := range j.Ledger.Mutants {
			if j.Select != nil && !j.Select.MatchString(m.Name) {
				continue
			}
			candidates, ok := candidatesOf(cfg.Root, cov, m)
			// A row no target test can kill gives the targets nothing: it
			// is not run (the segment filter).
			if !ok || !slices.ContainsFunc(candidates, target) {
				continue
			}
			rows = append(rows, pending{i, m, candidates})
			keyed = append(keyed, &Mutant{Name: m.Name, File: m.File, Decl: m.Decl, From: m.From, To: m.To, Killers: candidates})
		}
	}
	in, err := gatherReuse(cfg.Root, []Job{{Ledger: &Ledger{Mutants: keyed}}})
	if err != nil {
		fmt.Fprintf(cfg.Log, "alpha: no kill reused: %v\n", err)
		in = nil
	}
	if in != nil {
		// kills.json is derived and no test reads it: writing it does not
		// move the keys it holds.
		for p := range in.files {
			if strings.HasSuffix(p, "/testdata/kills.json") {
				delete(in.files, p)
			}
		}
		in.ignores = slices.DeleteFunc(in.ignores, func(p string) bool { return strings.HasSuffix(p, "/testdata/kills.json") })
	}
	old := make([]map[string]killRow, len(jobs))
	for i, j := range jobs {
		if old[i], err = loadKills(j.Path); err != nil {
			return nil, nil, err
		}
	}
	kept := make([]map[string]killRow, len(jobs))
	judge := make([][]string, len(jobs))
	keys := map[string]string{}
	candidates := map[string][]string{}
	for i := range kept {
		kept[i] = map[string]killRow{}
	}
	for _, p := range rows {
		key := matrixKey(in, p.m, p.candidates)
		id := jobs[p.job].Path + "\x00" + p.m.Name
		keys[id], candidates[id] = key, p.candidates
		// Only a judged row is reused: one held (red, invalid, cut) is
		// judged again.
		if k, ok := old[p.job][p.m.Name]; ok && key != "" && k.Key == key && (k.Status == Killed || k.Status == Survived) {
			kept[p.job][p.m.Name] = k
			continue
		}
		judge[p.job] = append(judge[p.job], p.m.Name)
	}
	var run []Job
	for i, names := range judge {
		if len(names) == 0 {
			continue
		}
		quoted := make([]string, len(names))
		for n, name := range names {
			quoted[n] = regexp.QuoteMeta(name)
		}
		run = append(run, Job{Path: jobs[i].Path, Ledger: jobs[i].Ledger, Select: regexp.MustCompile("^(" + strings.Join(quoted, "|") + ")$")})
	}
	if len(run) > 0 {
		all := cfg
		all.All = true
		results, _, err := Run(ctx, all, run)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range results {
			i := slices.IndexFunc(jobs, func(j Job) bool { return j.Path == r.Ledger })
			id := r.Ledger + "\x00" + r.Name
			if i >= 0 {
				kept[i][r.Name] = matrixRow(r, keys[id], candidates[id])
			}
		}
	}
	var matrix []killRow
	for i, j := range jobs {
		for k, r := range old[i] {
			if _, ok := kept[i][k]; !ok && slices.ContainsFunc(j.Ledger.Mutants, func(m *Mutant) bool { return m.Name == k }) && !picks(j, k) {
				kept[i][k] = r // a row this run did not consider keeps its entry
			}
		}
		if err := saveKills(j.Path, j.Ledger, kept[i]); err != nil {
			return nil, nil, err
		}
		for _, m := range j.Ledger.Mutants {
			if r, ok := kept[i][m.Name]; ok && picks(j, m.Name) {
				matrix = append(matrix, r)
			}
		}
	}
	return matrix, cov, nil
}

// picks says whether j selects its row name.
func picks(j Job, name string) bool { return j.Select == nil || j.Select.MatchString(name) }

// alphaTable is the ranked table of the target suites' tests over matrix.
func alphaTable(root string, cfg Config, cov *coverage, matrix []killRow, targetDirs []string, failLogs string) ([]alphaRow, error) {
	allow, err := readAllow(root)
	if err != nil {
		return nil, err
	}
	failures, err := countFailures(failLogs)
	if err != nil {
		return nil, err
	}
	byID := map[string]*alphaRow{}
	var ids []string
	for _, t := range cov.Tests {
		suite, name := splitID(t.ID)
		dir, ok := suiteDir(cfg, suite, targetDirs)
		if !ok {
			continue
		}
		byID[t.ID] = &alphaRow{Package: dir, Test: name, Class: classify(allow, dir, name), Seconds: t.Seconds, Failures: failures[name]}
		ids = append(ids, t.ID)
	}
	alphaKills(matrix, byID)
	for _, dir := range targetDirs {
		coupling := changeCoupling(root, dir)
		for _, id := range ids {
			if r := byID[id]; r.Package == dir {
				r.Coupling = coupling[r.Test]
			}
		}
	}
	out := make([]alphaRow, 0, len(ids))
	for _, id := range ids {
		out = append(out, *byID[id])
	}
	rankAlpha(out)
	return out, nil
}

// suiteDir is the target package of a Go suite, if it is one's.
func suiteDir(cfg Config, suite string, targetDirs []string) (string, bool) {
	i := slices.IndexFunc(cfg.Suites, func(s Suite) bool { return s.Name == suite })
	if i < 0 || cfg.Suites[i].Package == "" || cfg.Suites[i].Module != "" {
		return "", false
	}
	dir := strings.TrimPrefix(cfg.Suites[i].Package, "./")
	return dir, slices.Contains(targetDirs, dir)
}

// failLine is a top-level test's failure in a go test log.
var failLine = regexp.MustCompile(`(?m)^--- FAIL: (Test[^\s/]*)`)

// countFailures counts each test's failures in the logs under dir.
func countFailures(dir string) (map[string]int, error) {
	out := map[string]int{}
	if dir == "" {
		return out, nil
	}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range failLine.FindAllSubmatch(data, -1) {
			out[string(m[1])]++
		}
		return nil
	})
	return out, err
}

// couplingCommits bounds the history change coupling reads: the work is
// counted in commits, not timed.
const couplingCommits = 500

// changeCoupling is, for each test of the package in dir, the number of
// the last couplingCommits commits that touch dir in which the test's
// file changed together with a non-test Go file of dir. With no history
// it is empty.
func changeCoupling(root, dir string) map[string]int {
	out := map[string]int{}
	files := map[string][]string{} // test file -> its tests
	entries, _ := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
	testFunc := regexp.MustCompile(`(?m)^func (Test\w*)\(`)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(dir), e.Name()))
		if err != nil {
			continue
		}
		for _, m := range testFunc.FindAllSubmatch(data, -1) {
			files[e.Name()] = append(files[e.Name()], string(m[1]))
		}
	}
	cmd := exec.Command("git", "--no-optional-locks", "-C", root, "log", "-n", fmt.Sprint(couplingCommits), "--format=%x00", "--name-only", "--", dir)
	data, err := cmd.Output()
	if err != nil {
		return out
	}
	for _, commit := range strings.Split(string(data), "\x00") {
		var tests []string
		code := false
		for _, p := range strings.Fields(commit) {
			if filepath.ToSlash(filepath.Dir(p)) != dir {
				continue
			}
			base := filepath.Base(p)
			switch {
			case strings.HasSuffix(base, "_test.go"):
				tests = append(tests, files[base]...)
			case strings.HasSuffix(base, ".go"):
				code = true
			}
		}
		if code {
			for _, t := range tests {
				out[t]++
			}
		}
	}
	return out
}

// writeAlpha writes the table as tab-separated values with a header.
func writeAlpha(w io.Writer, rows []alphaRow) error {
	b := bufio.NewWriter(w)
	fmt.Fprintln(b, "package\ttest\tclass\tseconds\tunique\ttotal\tcoupling\tfailures\theld\tdelete")
	for _, r := range rows {
		del := "no"
		if r.deletable() {
			del = "yes"
		}
		fmt.Fprintf(b, "%s\t%s\t%s\t%.2f\t%d\t%d\t%d\t%d\t%d\t%s\n", r.Package, r.Test, r.Class, r.Seconds, r.Unique, r.Total, r.Coupling, r.Failures, r.Held, del)
	}
	return b.Flush()
}

// alphaJobs adds to jobs, for each ledger of the checkout outside them,
// the rows that name a test of the targets: their kills count too.
func alphaJobs(root string, jobs []Job, targetDirs []string) ([]Job, error) {
	names, err := listFiles(root, "-c", "--", "*testdata/mutants.json")
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		path := filepath.Join(root, filepath.FromSlash(name))
		if slices.ContainsFunc(jobs, func(j Job) bool { return filepath.Clean(j.Path) == filepath.Clean(path) }) {
			continue
		}
		l, err := readLedger(path)
		if err != nil {
			return nil, err
		}
		var picked []string
		for _, m := range l.Mutants {
			if slices.ContainsFunc(killerIDs(m), func(id string) bool {
				suite, _ := splitID(id)
				suite, _, _ = strings.Cut(suite, "[")
				return slices.Contains(targetDirs, suite)
			}) {
				picked = append(picked, regexp.QuoteMeta(m.Name))
			}
		}
		if len(picked) > 0 {
			jobs = append(jobs, Job{Path: path, Ledger: l, Select: regexp.MustCompile("^(" + strings.Join(picked, "|") + ")$")})
		}
	}
	return jobs, nil
}

// targetDirsOf are the package directories of jobs' ledgers.
func targetDirsOf(root string, jobs []Job) []string {
	var out []string
	for _, j := range jobs {
		rel, err := filepath.Rel(root, filepath.Dir(filepath.Dir(j.Path)))
		if err == nil && !slices.Contains(out, filepath.ToSlash(rel)) {
			out = append(out, filepath.ToSlash(rel))
		}
	}
	return out
}

// runAlpha is -alpha: cfg and jobs are the run's (jobs the packages'
// ledgers, unselected).
func runAlpha(ctx context.Context, cfg Config, jobs []Job, out, failLogs string, stdout io.Writer) error {
	dirs := targetDirsOf(cfg.Root, jobs)
	jobs, err := alphaJobs(cfg.Root, jobs, dirs)
	if err != nil {
		return err
	}
	if cfg.Cover, cfg.Tools, cfg.Suites, err = suitesFor(cfg.Root, jobs); err != nil {
		return err
	}
	target := func(id string) bool {
		suite, _ := splitID(id)
		_, ok := suiteDir(cfg, suite, dirs)
		return ok
	}
	matrix, cov, err := alphaMatrix(ctx, cfg, jobs, target)
	if err != nil {
		return err
	}
	rows, err := alphaTable(cfg.Root, cfg, cov, matrix, dirs, failLogs)
	if err != nil {
		return err
	}
	w := stdout
	if out != "" {
		f, err := os.Create(out)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	if err := writeAlpha(w, rows); err != nil {
		return err
	}
	deletable, seconds := 0, 0.0
	for _, r := range rows {
		if r.deletable() {
			deletable++
			seconds += r.Seconds
		}
	}
	fmt.Fprintf(stdout, "alpha %s: %d tests, %d matrix rows, %d on the deletion list (%.0f s)\n", strings.Join(dirs, " "), len(rows), len(matrix), deletable, seconds)
	return nil
}
