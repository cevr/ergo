package lint_test

import (
	"bufio"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The advisory preset (LH1): narration-comment and commented-code read
// only the syntax tree and the source, so their tests lint parsed files
// that need not check, in the package of an empty checked program.

// syntaxInput is the lint input of one parsed file, rel, whose package
// is an empty checked program's.
func syntaxInput(t *testing.T, rel, src string) lint.Input {
	t.Helper()
	in := lintInput("func main() {\n}\n")
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("%s does not parse: %v\n%s", rel, err, numbered(src))
	}
	in.Units[0].Files = []lint.File{{Rel: rel, Syntax: f}}
	return in
}

// Word pools of the narration generator, disjoint: words of code
// identifiers, words of string literals, and words only comments use.
var (
	narrCode   = []string{"alpha", "bravo", "cargo", "delta", "ember", "fable", "gamma", "harbor", "index", "jolly", "kettle", "lemon"}
	narrString = []string{"quiet", "rapid", "solid", "tidal"}
	narrExtra  = []string{"now", "then", "value", "step", "result", "store", "give", "back", "next", "first"}
)

// Where the generator puts a comment.
const (
	narrAbove    = iota // alone, in a body, directly above a code line
	narrSecond          // the second line of a two-line comment
	narrBlank           // above a blank line
	narrTrailing        // after code on its line
	narrFuncDoc         // the doc comment of a function
	narrMember          // the doc comment of an operation in an operation type
	narrPlaces
)

func title(w string) string { return strings.ToUpper(w[:1]) + w[1:] }

// TestNarrationCommentAgreesWithModel plants comments above generated code
// lines, built from k words of the line and e words it lacks (some from
// the line's string literal), in six places, and checks that
// narration-comment fires exactly where the comment stands alone in a
// body directly above the line and k >= 2 and 2k >= k+e. Mutants:
// lh1-narr-doc-skip, lh1-narr-shared-off, lh1-narr-half-off,
// lh1-narr-prev-comment, lh1-narr-alone, lh1-narr-string-words.
func TestNarrationCommentAgreesWithModel(t *testing.T) {
	for _, seed := range propertySeeds(40) {
		r := rand.New(rand.NewPCG(seed, 0x1a1))
		p := newPlanted()
		p.write("func main() {\n}", false, 0)
		for i := range 2 + r.IntN(4) {
			code := slices.Clone(narrCode)
			r.Shuffle(len(code), func(a, b int) { code[a], code[b] = code[b], code[a] })
			w := code[:4]
			str := narrString[r.IntN(len(narrString))]
			withString := r.IntN(2) == 0
			call := fmt.Sprintf("%s%s(%s)", w[2], title(w[3]), "n")
			if withString {
				call = fmt.Sprintf("%s%s(%q)", w[2], title(w[3]), str)
			}
			line := fmt.Sprintf("\tlet %s%s = %s", w[0], title(w[1]), call)
			place := r.IntN(2 * narrPlaces)
			if place >= narrPlaces {
				place = narrAbove
			}
			from := w
			if place == narrFuncDoc || place == narrMember {
				from = w[:2]
			}
			k := r.IntN(min(4, len(from)) + 1)
			words := slices.Clone(from[:k])
			extras := slices.Clone(narrExtra)
			if withString {
				extras = append(extras, str)
			}
			r.Shuffle(len(extras), func(a, b int) { extras[a], extras[b] = extras[b], extras[a] })
			// e near k, so the boundary 2k == k+e comes up often.
			e := max(0, k-1+r.IntN(3))
			words = append(words, extras[:e]...)
			if len(words) == 0 {
				words = append(words, extras[0])
				e++
			}
			r.Shuffle(len(words), func(a, b int) { words[a], words[b] = words[b], words[a] })
			words[0] = title(words[0])
			comment := "// " + strings.Join(words, " ")
			narration := k >= 2 && 2*k >= k+e
			switch place {
			case narrFuncDoc:
				p.write(fmt.Sprintf("\n%s\nfunc %s%s() {\n}", comment, title(w[0]), title(w[1])), false, 0)
				continue
			case narrMember:
				p.write(fmt.Sprintf("\ntype Ops%d {\n\t%s\n\t%s%s(n int) int,\n}", i, comment, title(w[0]), title(w[1])), false, 0)
				continue
			}
			p.write(fmt.Sprintf("\nfunc f%d(n int) {", i), false, 0)
			switch place {
			case narrAbove:
				p.write("\t"+comment, narration, 0)
				p.write(line, false, 0)
			case narrSecond:
				p.write("\t// See the spec", false, 0)
				p.write("\t"+comment, false, 0)
				p.write(line, false, 0)
			case narrBlank:
				p.write("\t"+comment, false, 0)
				p.write("", false, 0)
				p.write(line, false, 0)
			case narrTrailing:
				p.write(line+" "+comment, false, 0)
				p.write(line, false, 0)
			}
			p.write("}", false, 0)
		}
		src := p.b.String()
		got := linesOf(t, "narration-comment", syntaxInput(t, "main.ergo", src))
		want := slices.Clone(p.want)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("seed %d: narration-comment fired on lines %v, the model says %v\n%s\nreplay: go test ./internal/sketch/lint -run TestNarrationCommentAgreesWithModel -lint.seed=%d", seed, got, want, numbered(src), seed)
		}
	}
}

// TestNarrationCommentOnReadCorpora runs narration-comment on the Fable
// suite (24 example files, 120 planted narration comments) and its
// hold-out (12 other files, 60 planted with other verbs and cues), and on
// the 20 member doc comments of real code the exact control flagged in
// the 2026-11-17 read (testdata/narration/members.tsv). On each corpus
// recall and precision reach 0.95, and no member doc comment fires.
// Mutant: lh1-narr-short-words (words of two letters count).
func TestNarrationCommentOnReadCorpora(t *testing.T) {
	for _, corpus := range []string{"suite", "holdout"} {
		dir := filepath.Join("testdata", "narration", corpus)
		want := map[string]bool{}
		for _, row := range readTSV(t, filepath.Join(dir, "expected.tsv")) {
			want[row[0]+":"+row[1]] = true
		}
		files, err := os.ReadDir(filepath.Join(dir, "files"))
		if err != nil {
			t.Fatal(err)
		}
		hit, found := 0, 0
		var wrong []string
		for _, f := range files {
			src, err := os.ReadFile(filepath.Join(dir, "files", f.Name()))
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range linesOf(t, "narration-comment", syntaxInput(t, f.Name(), string(src))) {
				found++
				if key := f.Name() + ":" + strconv.Itoa(line); want[key] {
					hit++
				} else {
					wrong = append(wrong, key)
				}
			}
		}
		recall, precision := float64(hit)/float64(len(want)), float64(hit)/float64(max(found, 1))
		t.Logf("%s: recall %.3f, precision %.3f", corpus, recall, precision)
		if recall < 0.95 || precision < 0.95 {
			t.Errorf("%s: recall %.3f (%d of %d), precision %.3f (%d of %d); false findings %v", corpus, recall, hit, len(want), precision, hit, found, wrong)
		}
	}
	root := filepath.Join("..", "..", "..")
	for _, row := range readTSV(t, filepath.Join("testdata", "narration", "members.tsv")) {
		file := row[0][:strings.LastIndexByte(row[0], ':')]
		src, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		at := 0
		for i, l := range strings.Split(string(src), "\n") {
			if strings.TrimSpace(l) == row[1] {
				at = i + 1
				break
			}
		}
		if at == 0 {
			t.Fatalf("%s no longer holds %q: update testdata/narration/members.tsv", file, row[1])
		}
		if slices.Contains(linesOf(t, "narration-comment", syntaxInput(t, file, string(src))), at) {
			t.Errorf("%s:%d: narration-comment fired on a member's doc comment %q", file, at, row[1])
		}
	}
}

func readTSV(t *testing.T, path string) [][]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out [][]string
	s := bufio.NewScanner(f)
	for s.Scan() {
		if s.Text() != "" {
			out = append(out, strings.Split(s.Text(), "\t"))
		}
	}
	return out
}

// A comment of the commented-code generator: its text (whole lines), and
// whether it is code the rule names.
var commentKinds = []planKind{
	{true, 0, "\t// let total = count(items)"},
	{true, 0, "\t// if ready {\n\t// \treturn total\n\t// }"},
	{true, 0, "\t// total = total + step"},
	{false, 0, "\t// the cache keeps one entry per key."},
	{false, 0, "\t// done"},
	{false, 0, "\t// strings.Builder"},
	{true, 0, "\t/* let total = count(items) */"},
	{false, 0, "\t// Step 2: count the items, then return the total."},
}

// TestCommentedCodeParses writes comments in function bodies (statements,
// a block, an assignment, a block comment of code, prose, one word, one
// selector) and doc comments that are code, and checks that
// commented-code fires on exactly the comments that parse as statements
// that are more than a lone name, never on a doc comment. Mutants:
// lh1-code-doc-skip, lh1-code-lone-operand, lh1-code-block-marks.
func TestCommentedCodeParses(t *testing.T) {
	for _, seed := range propertySeeds(30) {
		r := rand.New(rand.NewPCG(seed, 0xc0de))
		p := newPlanted()
		p.write("func main() {\n}", false, 0)
		for i := range 1 + r.IntN(5) {
			if r.IntN(4) == 0 {
				p.write(fmt.Sprintf("\n// let d = double%d(2)\nfunc double%d(n int) int {\n\tn * 2\n}", i, i), false, 0)
				continue
			}
			k := commentKinds[r.IntN(len(commentKinds))]
			p.write(fmt.Sprintf("\nfunc f%d(n int) int {\n\tlet m = n + 1", i), false, 0)
			p.write(k.text, k.fires, k.at)
			p.write("\tm\n}", false, 0)
		}
		src := p.b.String()
		got := linesOf(t, "commented-code", syntaxInput(t, "main.ergo", src))
		want := slices.Clone(p.want)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("seed %d: commented-code fired on lines %v, the model says %v\n%s\nreplay: go test ./internal/sketch/lint -run TestCommentedCodeParses -lint.seed=%d", seed, got, want, numbered(src), seed)
		}
	}
}
