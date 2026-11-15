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
// writes top-level funcs (no .ergo position until a directive or mark; the
// Go position goes on), //line directives with and without a column and
// with an inline tag (#ergo-inline-N), in the copy's root or a package
// below it (KEY/p/NAME, as lower's dispatcher links), cover marks of std files (indented,
// as go/format leaves them in a body; they move no Go position),
// statements (each one line on from the last), statements with an inline
// /*line*/ directive (the text after it, and the lines after that, take
// its position), and directives to a file outside the checkout. For every
// line, mapGoLines must give the generator's positions and the names of
// its Go position; for random blocks named as the cover tool names them
// (go1.27: the Go position's base name at the block's start; go1.26: the
// Go file's own name), blocks must give the checkout lines (a std mark's
// in internal/sketch/std) of every line of the block, and nothing for a
// name the block's start does not have. A failing seed prints the Go text
// and the replay command.
//
// Mutants (testdata/mutants.json): gt7-no-increment, gt7-func-keeps-position,
// gt7-inline-not-carried, gt7-tag-kept, gt7-any-base, gt7-outside-root,
// gt7-column-as-line, gt7b-mark-ignored, gt7b-mark-moves-go, gt7b-std-tree,
// gt7b-sub-dir.
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
	stdFiles := []string{"std/flag/parse.ergo", "std/prelude.ergo", "std/x/y.ergo"}
	var lines []string
	var truth []goLine
	var cur *ergoPos
	goName := "main.go"
	stmt := func(text string, extra *ergoPos) {
		l := goLine{names: []string{goName}}
		if cur != nil {
			l.at = append(l.at, *cur)
		}
		if extra != nil {
			l.at = append(l.at, *extra)
			goName = path.Base(extra.file)
			if !slices.Contains(l.names, goName) {
				l.names = append(l.names, goName)
			}
			next := ergoPos{extra.file, extra.line + 1}
			cur = &next
		} else if cur != nil {
			next := ergoPos{cur.file, cur.line + 1}
			cur = &next
		}
		lines, truth = append(lines, text), append(truth, l)
	}
	lines, truth = append(lines, "package main", ""), append(truth, goLine{names: []string{goName}}, goLine{names: []string{goName}})
	for range 20 + r.IntN(60) {
		switch k := r.IntN(12); {
		case k == 0:
			cur = nil
			lines, truth = append(lines, fmt.Sprintf("func f%d() {", len(lines))), append(truth, goLine{names: []string{goName}})
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
			lines, truth = append(lines, "//line "+spelled), append(truth, goLine{})
			cur, goName = &p, path.Base(p.file)
		case k <= 5:
			p := ergoPos{stdFiles[r.IntN(len(stdFiles))], 1 + r.IntN(200)}
			lines, truth = append(lines, fmt.Sprintf("%s%s%s:%d:%d", strings.Repeat("\t", r.IntN(3)), coverMark, p.file, p.line, 1+r.IntN(80))), append(truth, goLine{})
			cur = &p
		case k <= 9:
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
	got := mapGoLines("main.go", text)
	for g := range lines {
		if !slices.Equal(got[g].at, truth[g].at) || !slices.Equal(got[g].names, truth[g].names) {
			return fail("line %d %q: positions %v names %v, want %v names %v", g+1, lines[g], got[g].at, got[g].names, truth[g].at, truth[g].names)
		}
	}
	dir := t.TempDir()
	key := "k" + strconv.Itoa(seed)
	if r.IntN(2) == 0 {
		// A package below the copy's root (lower's dispatcher: p/NAME).
		key += "/p/prog" + strconv.Itoa(r.IntN(3))
	}
	if err := os.MkdirAll(filepath.Join(dir, key), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, key, "main.go"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newErgoMapper(dir, root)
	bases := []string{"main.go", "x.ergo", "y.ergo", "x2.ergo", "z.ergo", "parse.ergo"}
	for range 20 {
		lo := 1 + r.IntN(len(lines))
		hi := min(len(lines), lo+r.IntN(6))
		base := bases[r.IntN(len(bases))]
		if r.IntN(2) == 0 && len(truth[lo-1].names) > 0 {
			base = truth[lo-1].names[r.IntN(len(truth[lo-1].names))]
		}
		want := map[blockKey]bool{}
		if base == "main.go" || slices.Contains(truth[lo-1].names, base) {
			for g := lo; g <= hi; g++ {
				for _, p := range truth[g-1].at {
					rel, ok := strings.CutPrefix(p.file, root+"/")
					if std, isStd := strings.CutPrefix(p.file, "std/"); isStd {
						rel, ok = "internal/sketch/std/"+std, true
					}
					if ok {
						want[blockKey{file: rel, start: p.line, end: p.line}] = true
					}
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
