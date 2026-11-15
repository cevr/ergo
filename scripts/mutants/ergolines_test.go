package main

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// ergoLinesSeedVariable replays one seed of TestErgoLinesFollowTheDirectives.
const ergoLinesSeedVariable = "ERGO_MUTANTS_ERGO_LINES_SEED"

// A coverage copy's block maps to exactly the .ergo lines its Go lines came
// from (mapGoLines, ergoMapper.blocks): a seeded property over generated
// lowered Go whose positions are known by construction. The generator
// writes top-level funcs (no position until a directive), //line
// directives with and without a column and with an inline tag
// (#ergo-inline-N), statements (each one line on from the last), statements
// with an inline /*line*/ directive (the text after it, and the lines after
// that, take its position), and directives to a file outside the checkout.
// For every line, mapGoLines must give the generator's positions; for
// random blocks named as the cover tool names them (the governing
// directive's base name, or the Go file's own name), blocks must give the
// checkout lines of those positions with that base name (every base name
// for the Go file's own name). A failing seed prints the Go text and the
// replay command.
//
// Mutants (testdata/mutants.json): gt7-no-increment, gt7-func-keeps-position,
// gt7-inline-not-carried, gt7-tag-kept, gt7-any-base, gt7-outside-root,
// gt7-column-as-line.
func TestErgoLinesFollowTheDirectives(t *testing.T) {
	seeds := 300
	if testing.Short() {
		seeds = 60
	}
	first := 0
	if v := os.Getenv(ergoLinesSeedVariable); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("%s=%q: want a seed number", ergoLinesSeedVariable, v)
		}
		first, seeds = n, 1
	}
	for seed := first; seed < first+seeds; seed++ {
		if !checkErgoLinesSeed(t, seed) {
			return
		}
	}
}

func checkErgoLinesSeed(t *testing.T, seed int) bool {
	t.Helper()
	r := rand.New(rand.NewPCG(uint64(seed), 17))
	root := filepath.Join(t.TempDir(), "src")
	files := []string{root + "/a/x.ergo", root + "/a/y.ergo", root + "/b/x2.ergo", "/elsewhere/z.ergo"}
	var lines []string
	var truth [][]ergoPos
	var cur *ergoPos
	stmt := func(text string, extra *ergoPos) {
		var at []ergoPos
		if cur != nil {
			at = append(at, *cur)
		}
		if extra != nil {
			at = append(at, *extra)
			next := ergoPos{extra.file, extra.line + 1}
			cur = &next
		} else if cur != nil {
			next := ergoPos{cur.file, cur.line + 1}
			cur = &next
		}
		lines, truth = append(lines, text), append(truth, at)
	}
	lines, truth = append(lines, "package main", ""), append(truth, nil, nil)
	for range 20 + r.IntN(60) {
		switch k := r.IntN(10); {
		case k == 0:
			lines, truth = append(lines, fmt.Sprintf("func f%d() {", len(lines))), append(truth, nil)
			cur = nil
		case k <= 3:
			p := ergoPos{files[r.IntN(len(files))], 1 + r.IntN(200)}
			spelled := p.file
			if r.IntN(3) == 0 {
				spelled += fmt.Sprintf("#ergo-inline-%d", r.IntN(4))
			}
			if r.IntN(2) == 0 {
				spelled += fmt.Sprintf(":%d:%d", p.line, 1+r.IntN(80))
			} else {
				spelled += fmt.Sprintf(":%d", p.line)
			}
			lines, truth = append(lines, "//line "+spelled), append(truth, nil)
			cur = &p
		case k <= 7:
			stmt(fmt.Sprintf("\tv%d := %d", len(lines), r.IntN(9)), nil)
		default:
			p := ergoPos{files[r.IntN(len(files))], 1 + r.IntN(200)}
			stmt(fmt.Sprintf("\tv%d := a[/*line %s:%d:%d*/i]", len(lines), p.file, p.line, 1+r.IntN(80)), &p)
		}
	}
	text := strings.Join(lines, "\n")
	fail := func(format string, args ...any) bool {
		t.Errorf("seed %d: %s\nGo text:\n%s\nreplay: %s=%d go test -run TestErgoLinesFollowTheDirectives ./scripts/mutants", seed, fmt.Sprintf(format, args...), numbered(text), ergoLinesSeedVariable, seed)
		return false
	}
	got := mapGoLines(text)
	for g := range lines {
		if !slices.Equal(got[g], truth[g]) {
			return fail("line %d %q: positions %v, want %v", g+1, lines[g], got[g], truth[g])
		}
	}
	dir := t.TempDir()
	key := "k" + strconv.Itoa(seed)
	if err := os.MkdirAll(filepath.Join(dir, key), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, key, "main.go"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newErgoMapper(dir, root)
	bases := []string{"main.go", "x.ergo", "y.ergo", "x2.ergo", "z.ergo"}
	for range 20 {
		lo := 1 + r.IntN(len(lines))
		hi := min(len(lines), lo+r.IntN(6))
		base := bases[r.IntN(len(bases))]
		want := map[blockKey]bool{}
		for g := lo; g <= hi; g++ {
			for _, p := range truth[g-1] {
				rel, ok := strings.CutPrefix(p.file, root+"/")
				if ok && (base == "main.go" || path.Base(p.file) == base) {
					want[blockKey{file: rel, start: p.line, end: p.line}] = true
				}
			}
		}
		name := coverGoModule + key + "/" + base
		if r.IntN(4) == 0 && base != "main.go" {
			name += "#ergo-inline-1"
		}
		have := map[blockKey]bool{}
		for _, b := range m.blocks(name, lo, hi) {
			have[b] = true
		}
		if !maps.Equal(have, want) {
			return fail("block %s:%d-%d maps to %v, want %v", name, lo, hi, sortedKeys(have), sortedKeys(want))
		}
	}
	return true
}

func numbered(text string) string {
	var b strings.Builder
	for i, line := range strings.Split(text, "\n") {
		fmt.Fprintf(&b, "%4d %s\n", i+1, line)
	}
	return b.String()
}

func sortedKeys(m map[blockKey]bool) []string {
	var out []string
	for b := range m {
		out = append(out, fmt.Sprintf("%s:%d", b.file, b.start))
	}
	slices.Sort(out)
	return out
}
