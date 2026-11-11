package main

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
)

// coverage is the per-test coverage of the checkout: which top-level test
// (or example program) runs which block of the covered packages, measured
// once, each test in its own process.
type coverage struct {
	// Key is the digest of the checkout it was measured on (treeKey).
	Key   string     `json:"key"`
	Tests []*covTest `json:"tests"`
	// Files maps a file, relative to the module root, to its blocks that
	// some test runs.
	Files map[string][]covBlock `json:"files"`
	// Builds is how long each build of the coverage pass took, in seconds
	// (suite or tool name): the base of the mutant pass's build deadlines.
	Builds map[string]float64 `json:"builds"`

	index map[string]int
}

// covTest is one test of a suite: its SUITE:NAME, its wall time in the
// coverage pass (the base of its deadline and of the cheapest-first
// order), and whether it failed there. A test that fails on the unmutated
// tree kills nothing.
type covTest struct {
	ID      string  `json:"id"`
	Seconds float64 `json:"seconds"`
	Failed  bool    `json:"failed,omitempty"`
	Detail  string  `json:"detail,omitempty"`
}

// covBlock is a block's first and last line and the tests (indexes into
// Tests) that run it.
type covBlock struct {
	Start int   `json:"s"`
	End   int   `json:"e"`
	Tests []int `json:"t"`
}

func (c *coverage) reindex() {
	c.index = map[string]int{}
	for i, t := range c.Tests {
		c.index[t.ID] = i
	}
}

func (c *coverage) test(id string) *covTest {
	if i, ok := c.index[id]; ok {
		return c.Tests[i]
	}
	return nil
}

// covering returns the tests that run a block of file between lines lo and
// hi, and the extra tests that exist, without the tests that fail on the
// unmutated tree. A header line (headerLine: a case or an else if) that is
// in no block takes the tests of the block that ends last before it.
func (c *coverage) covering(file string, lo, hi int, header bool, extra []string) map[string]bool {
	out := map[string]bool{}
	var before *covBlock
	inBlock := false
	for k, b := range c.Files[file] {
		if b.Start <= hi && b.End >= lo {
			inBlock = true
			for _, i := range b.Tests {
				out[c.Tests[i].ID] = true
			}
		}
		if b.End < lo && (before == nil || b.End >= before.End) {
			before = &c.Files[file][k]
		}
	}
	// A line in no block is a header the cover tool counts with no block
	// of its own (a case of a tagless switch, an else if): the code that
	// reaches it runs it, so the block that ends last before it stands
	// for it (M1: runtime/v1's case errors.Is(err, ECONNREFUSED) was
	// uncovered).
	if header && !inBlock && before != nil {
		for _, i := range before.Tests {
			out[c.Tests[i].ID] = true
		}
	}
	for _, id := range extra {
		if c.test(id) != nil {
			out[id] = true
		}
	}
	for id := range out {
		if c.test(id).Failed {
			delete(out, id)
		}
	}
	return out
}

// order puts the known killers first, in the ledger's order, then the other
// tests cheapest first.
func (c *coverage) order(covering map[string]bool, known []string) []string {
	var out []string
	for _, id := range known {
		if covering[id] && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	var rest []string
	for id := range covering {
		if !slices.Contains(out, id) {
			rest = append(rest, id)
		}
	}
	slices.SortFunc(rest, func(a, b string) int {
		if sa, sb := c.test(a).Seconds, c.test(b).Seconds; sa != sb {
			if sa < sb {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	})
	return append(out, rest...)
}

// blockKey is a block as go tool covdata textfmt names it.
type blockKey struct {
	file               string
	start, end, sc, ec int
}

// collector merges per-test profiles into a coverage.
type collector struct {
	module string
	blocks map[blockKey][]int
}

// add reads one test's profile (go tool covdata textfmt) and records the
// blocks it ran. A file outside module is not a file of the checkout.
func (k *collector) add(test int, profile io.Reader) error {
	scanner := bufio.NewScanner(profile)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		key, count, err := parseBlock(line)
		if err != nil {
			return err
		}
		rel, ok := strings.CutPrefix(key.file, k.module+"/")
		if !ok || count == 0 {
			continue
		}
		key.file = rel
		if tests := k.blocks[key]; len(tests) == 0 || tests[len(tests)-1] != test {
			k.blocks[key] = append(tests, test)
		}
	}
	return scanner.Err()
}

// parseBlock reads "FILE:L.C,L.C STATEMENTS COUNT".
func parseBlock(line string) (blockKey, int, error) {
	bad := fmt.Errorf("coverage profile: bad line %q", line)
	colon := strings.LastIndexByte(line, ':')
	if colon < 0 {
		return blockKey{}, 0, bad
	}
	fields := strings.Fields(line[colon+1:])
	if len(fields) != 3 {
		return blockKey{}, 0, bad
	}
	from, to, ok := strings.Cut(fields[0], ",")
	if !ok {
		return blockKey{}, 0, bad
	}
	var n [4]int
	for i, part := range []string{from, to} {
		l, c, ok := strings.Cut(part, ".")
		if !ok {
			return blockKey{}, 0, bad
		}
		var err1, err2 error
		n[2*i], err1 = strconv.Atoi(l)
		n[2*i+1], err2 = strconv.Atoi(c)
		if err1 != nil || err2 != nil {
			return blockKey{}, 0, bad
		}
	}
	count, err := strconv.Atoi(fields[2])
	if err != nil {
		return blockKey{}, 0, bad
	}
	return blockKey{file: line[:colon], start: n[0], sc: n[1], end: n[2], ec: n[3]}, count, nil
}

func (k *collector) files() map[string][]covBlock {
	out := map[string][]covBlock{}
	for key, tests := range k.blocks {
		slices.Sort(tests)
		out[key.file] = append(out[key.file], covBlock{Start: key.start, End: key.end, Tests: slices.Compact(tests)})
	}
	for _, blocks := range out {
		slices.SortFunc(blocks, func(a, b covBlock) int {
			if a.Start != b.Start {
				return a.Start - b.Start
			}
			return a.End - b.End
		})
	}
	return out
}

func saveCoverage(path string, c *coverage) error {
	f, err := os.Create(path + ".partial")
	if err != nil {
		return err
	}
	z := gzip.NewWriter(f)
	err = json.NewEncoder(z).Encode(c)
	if closeErr := z.Close(); err == nil {
		err = closeErr
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(path+".partial", path)
}

func loadCoverage(path string) (*coverage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var c coverage
	if err := json.NewDecoder(z).Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.reindex()
	return &c, nil
}
