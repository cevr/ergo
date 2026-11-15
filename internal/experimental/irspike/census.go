package irspike

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketchfront"
)

// Domain is where a censused body comes from.
type Domain string

const (
	Examples Domain = "examples" // the run builds of every example binary
	Tests    Domain = "tests"    // bodies only the examples' test builds lower
	Std      Domain = "std"      // every std body, deduplicated
	Corpus   Domain = "corpus"   // the lowering corpus's programs
)

// Domains in report order.
var Domains = []Domain{Examples, Tests, Std, Corpus}

// CensusFunc is one body of the census, deduplicated across targets: a
// sourced body by file and name, a std body by package and name.
type CensusFunc struct {
	Key     string
	Domain  Domain
	Reasons []string
}

// Census is every body the IR coverage tracks, with every reason each
// cannot be lowered yet.
type Census struct {
	Funcs []*CensusFunc
	// Targets are the programs lowered, by domain.
	Targets map[Domain]int
	// NotPrograms are corpus files that do not check or lower: not
	// programs, so not part of the coverage (negative fixtures).
	NotPrograms int
}

type censusBuilder struct {
	c     *Census
	byKey map[string]*CensusFunc
}

func (b *censusBuilder) add(d Domain, fs []lower.IRCensusFunc) {
	for _, f := range fs {
		key := f.Pkg + "." + f.Name
		switch {
		case f.Std:
			key = "std " + key
		case f.File != "":
			key = f.File + ":" + f.Name
		}
		dom := d
		if f.Std {
			dom = Std
		}
		cf := b.byKey[key]
		if cf == nil {
			cf = &CensusFunc{Key: key, Domain: dom}
			b.byKey[key] = cf
			b.c.Funcs = append(b.c.Funcs, cf)
		}
		for _, r := range f.Reasons {
			if !contains(cf.Reasons, r) {
				cf.Reasons = append(cf.Reasons, r)
			}
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ExampleTargets are the example folders under root/examples: mains, the
// folders holding main.ergo (each is one binary), and tests, every folder
// that holds .ergo files (each is one ergo test target).
func ExampleTargets(root string) (mains, tests []string, err error) {
	err = filepath.WalkDir(filepath.Join(root, "examples"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "testdata" {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() == "main.ergo" {
			mains = append(mains, filepath.Dir(path))
		}
		if strings.HasSuffix(d.Name(), ".ergo") {
			dir := filepath.Dir(path)
			if len(tests) == 0 || tests[len(tests)-1] != dir {
				tests = append(tests, dir)
			}
		}
		return nil
	})
	return mains, tests, err
}

// CorpusDirs are the lowering corpus: the checker's and lowerer's program
// fixtures and probes, and the benchmarks.
var CorpusDirs = []string{
	"internal/sketch/lower/testdata",
	"internal/sketch/check/testdata/v1",
	"internal/sketch/check/testdata/fixtures/positive",
	"internal/sketch/check/testdata/fixtures/spec",
	"internal/sketch/testdata/probes",
	"benchmarks",
}

func corpusFiles(root string) ([]string, error) {
	var out []string
	for _, d := range CorpusDirs {
		err := filepath.WalkDir(filepath.Join(root, d), func(path string, e os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".ergo") {
				out = append(out, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// StdFuncs are the std functions with an Ergo body, in a fixed order.
func StdFuncs() ([]*check.Func, error) {
	var out []*check.Func
	for _, path := range std.Packages() {
		p, err := check.Std.Import(path)
		if err != nil {
			return nil, err
		}
		for _, f := range p.Funcs {
			if f.Decl != nil && f.Decl.Body != nil {
				out = append(out, f)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Pkg.Path != b.Pkg.Path {
			return a.Pkg.Path < b.Pkg.Path
		}
		return a.Name < b.Name
	})
	return out, nil
}

// TakeCensus lowers every example binary, every example test target, every
// std body and every corpus program of the repository at root with the IR
// builder's census, and records every reason. An example that does not
// load or lower is an error, never skipped.
func TakeCensus(root string, progress io.Writer) (*Census, error) {
	b := &censusBuilder{c: &Census{Targets: map[Domain]int{}}, byKey: map[string]*CensusFunc{}}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	mains, tests, err := ExampleTargets(root)
	if err != nil {
		return nil, err
	}
	for _, dir := range mains {
		u, err := Load(dir)
		if err != nil {
			return nil, fmt.Errorf("example %s: %w", dir, err)
		}
		c, err := lower.CensusIR(u, false)
		if err != nil {
			return nil, fmt.Errorf("example %s: %w", dir, err)
		}
		b.add(Examples, c.Funcs)
		b.c.Targets[Examples]++
		fmt.Fprintf(progress, "census %s\n", dir)
	}
	for _, dir := range tests {
		u, err := Load(dir)
		if err != nil {
			return nil, fmt.Errorf("test target %s: %w", dir, err)
		}
		c, err := lower.CensusIR(u, true)
		if err != nil {
			return nil, fmt.Errorf("test target %s: %w", dir, err)
		}
		b.add(Tests, c.Funcs)
		b.c.Targets[Tests]++
		fmt.Fprintf(progress, "census test %s\n", dir)
	}
	funcs, err := StdFuncs()
	if err != nil {
		return nil, err
	}
	a := sketchfront.Check("func main() {}\n")
	if len(a.Diagnostics) > 0 {
		return nil, fmt.Errorf("the std census program does not check: %s", a.Diagnostics[0].Message)
	}
	u, err := sketchfront.IRSpikeUnit(a, filepath.Join(root, "std-census.ergo"))
	if err != nil {
		return nil, err
	}
	c, err := lower.CensusStd(u, funcs)
	if err != nil {
		return nil, fmt.Errorf("std census: %w", err)
	}
	b.add(Std, c.Funcs)
	b.c.Targets[Std] = len(funcs)
	files, err := corpusFiles(root)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		u, err := Load(file)
		if err != nil {
			b.c.NotPrograms++
			continue
		}
		c, err := lower.CensusIR(u, false)
		if err != nil {
			b.c.NotPrograms++
			continue
		}
		b.add(Corpus, c.Funcs)
		b.c.Targets[Corpus]++
	}
	return b.c, nil
}

// ReasonRow is one reason of the census table.
type ReasonRow struct {
	Reason string
	Unit   string
	// All counts the bodies with the reason, by domain; First those whose
	// first reason it is.
	All, First map[Domain]int
}

// Table aggregates the census by reason, largest first.
func (c *Census) Table() []ReasonRow {
	rows := map[string]*ReasonRow{}
	for _, f := range c.Funcs {
		for i, r := range f.Reasons {
			row := rows[r]
			if row == nil {
				row = &ReasonRow{Reason: r, Unit: UnitOf(r), All: map[Domain]int{}, First: map[Domain]int{}}
				rows[r] = row
			}
			row.All[f.Domain]++
			if i == 0 {
				row.First[f.Domain]++
			}
		}
	}
	var out []ReasonRow
	for _, r := range rows {
		out = append(out, *r)
	}
	total := func(m map[Domain]int) (n int) {
		for _, v := range m {
			n += v
		}
		return n
	}
	sort.Slice(out, func(i, j int) bool {
		if a, b := total(out[i].All), total(out[j].All); a != b {
			return a > b
		}
		return out[i].Reason < out[j].Reason
	})
	return out
}

// Lowered counts the bodies with no reason, and all bodies, by domain.
func (c *Census) Lowered() (ir, all map[Domain]int) {
	ir, all = map[Domain]int{}, map[Domain]int{}
	for _, f := range c.Funcs {
		all[f.Domain]++
		if len(f.Reasons) == 0 {
			ir[f.Domain]++
		}
	}
	return ir, all
}

// WriteTable prints the census as a Markdown table.
func (c *Census) WriteTable(w io.Writer) {
	ir, all := c.Lowered()
	fmt.Fprintf(w, "| domain | targets | bodies | via IR |\n|---|---:|---:|---:|\n")
	for _, d := range Domains {
		fmt.Fprintf(w, "| %s | %d | %d | %d |\n", d, c.Targets[d], all[d], ir[d])
	}
	fmt.Fprintf(w, "\nCorpus files that are not programs (do not check or lower): %d\n\n", c.NotPrograms)
	fmt.Fprintf(w, "| reason | unit |")
	for _, d := range Domains {
		fmt.Fprintf(w, " %s all | %s first |", d, d)
	}
	fmt.Fprintf(w, "\n|---|---|")
	for range Domains {
		fmt.Fprintf(w, "---:|---:|")
	}
	fmt.Fprintln(w)
	for _, r := range c.Table() {
		fmt.Fprintf(w, "| %s | %s |", strings.ReplaceAll(r.Reason, "|", `\|`), r.Unit)
		for _, d := range Domains {
			fmt.Fprintf(w, " %d | %d |", r.All[d], r.First[d])
		}
		fmt.Fprintln(w)
	}
}
