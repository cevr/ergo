package main

import (
	"bufio"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/scripts/mutants/covfile"
)

// The coverage format lives in covfile, which scripts/gate reads too
// (CI-SELECT CS-ROUND); these are its names here.
type (
	coverage = covfile.Coverage
	covTest  = covfile.Test
	covBlock = covfile.Block
)

func saveCoverage(path string, c *coverage) error { return covfile.Save(path, c) }

func loadCoverage(path string) (*coverage, error) { return covfile.Load(path) }

// blockKey is a block as go tool covdata textfmt names it.
type blockKey struct {
	file               string
	start, end, sc, ec int
}

// collector merges per-test profiles into a coverage.
type collector struct {
	module string
	blocks map[blockKey][]int
	// ergo maps the blocks of lowered programs' coverage builds to .ergo
	// lines; nil maps none.
	ergo *ergoMapper
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
		if count == 0 {
			continue
		}
		rel, ok := strings.CutPrefix(key.file, k.module+"/")
		if !ok {
			// A block of a lowered program's coverage build stands for
			// the .ergo lines it came from (ergolines.go).
			for _, b := range k.ergo.blocks(key.file, key.start, key.end) {
				k.record(b, test)
			}
			continue
		}
		key.file = rel
		k.record(key, test)
	}
	return scanner.Err()
}

func (k *collector) record(key blockKey, test int) {
	if tests := k.blocks[key]; len(tests) == 0 || tests[len(tests)-1] != test {
		k.blocks[key] = append(tests, test)
	}
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
