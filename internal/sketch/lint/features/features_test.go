package features_test

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	format "ergo.local/prototype/internal/hostsyntax"
	"ergo.local/prototype/internal/sketch/lint/features"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/testinputs"
)

func TestMain(m *testing.M) { os.Exit(testinputs.Main(m)) }

const root = "../../../.."

const vectors = root + "/modules/tn1/testdata/vectors"

// TestVectorRecordsRegenerate: the records of the conformance vectors are
// what the parser makes of their sources, byte for byte. The vectors'
// findings are checked by the Ergo engine (modules/tn1, ergo test); this
// is the half that holds the record's filler. To write them again: go run
// ./scripts/tn1record -out modules/tn1/testdata/vectors/records
// modules/tn1/testdata/vectors/src/*.ergo.txt.
func TestVectorRecordsRegenerate(t *testing.T) {
	sources, err := filepath.Glob(vectors + "/src/*.ergo.txt")
	if err != nil || len(sources) == 0 {
		t.Fatalf("no vector sources under %s (%v): an empty set checks nothing", vectors, err)
	}
	for _, path := range sources {
		name := strings.TrimSuffix(filepath.Base(path), ".ergo.txt")
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := syntax.Parse(string(src))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		want, err := os.ReadFile(filepath.Join(vectors, "records", name+".record"))
		if err != nil {
			t.Fatal(err)
		}
		if got := features.Encode(features.FromErgo(file)); got != string(want) {
			t.Errorf("%s: the record differs from records/%s.record; first difference at line %d", name, name, firstDiff(got, string(want)))
		}
	}
}

func firstDiff(a, b string) int {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := range min(len(al), len(bl)) {
		if al[i] != bl[i] {
			return i + 1
		}
	}
	return min(len(al), len(bl)) + 1
}

// TestRecordSurvivesWhitespace: a reformat that keeps the lines keeps the
// record (invariance row 1: re-indenting, tabs for spaces, trailing
// spaces). Each case perturbs a corpus file drawn by seed, checks that
// ergo fmt gives the original's text back (so the perturbation is one a
// reformat undoes), and requires the same record but for the byte places
// of its nodes (offset, length, column), which move with the bytes; a
// node's line stays. A failure prints the
// seed, the file and the first line that differs.
func TestRecordSurvivesWhitespace(t *testing.T) {
	var corpus []string
	err := filepath.WalkDir(root+"/examples", func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".ergo") && !strings.Contains(path, "/testdata/") {
			corpus = append(corpus, path)
		}
		return err
	})
	if err != nil || len(corpus) == 0 {
		t.Fatalf("no corpus under examples/ (%v)", err)
	}
	sort.Strings(corpus)
	const seed, cases = 20261117, 40
	r := rand.New(rand.NewPCG(seed, 0))
	ran := 0
	for range cases {
		path := corpus[r.IntN(len(corpus))]
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		src, err := format.Source(string(data))
		if err != nil {
			continue // not formattable on its own (a fragment of a larger module)
		}
		file, err := syntax.Parse(src)
		if err != nil {
			continue
		}
		perturbed := perturb(src, file, r)
		again, err := format.Source(perturbed)
		if err != nil || again != src {
			continue // the perturbation reached a place a reformat does not undo
		}
		pfile, err := syntax.Parse(perturbed)
		if err != nil {
			t.Fatalf("seed %d, %s: the perturbed text does not parse: %v", seed, path, err)
		}
		want := features.Encode(placeless(features.FromErgo(file)))
		got := features.Encode(placeless(features.FromErgo(pfile)))
		if got != want {
			gl, wl := strings.Split(got, "\n"), strings.Split(want, "\n")
			n := firstDiff(got, want)
			t.Fatalf("seed %d, %s: the record changed under whitespace at record line %d\n got  %q\n want %q", seed, path, n, at(gl, n), at(wl, n))
		}
		ran++
	}
	if ran < cases/2 {
		t.Fatalf("seed %d: only %d of %d cases ran: the perturbation is not one ergo fmt undoes", seed, ran, cases)
	}
}

// placeless is f with its nodes' byte offsets, lengths and columns at 0.
func placeless(f features.Features) features.Features {
	nodes := make([]features.Node, len(f.Nodes))
	for i, n := range f.Nodes {
		n.Offset, n.Length, n.Column = 0, 0, 0
		nodes[i] = n
	}
	f.Nodes = nodes
	return f
}

func at(lines []string, n int) string {
	if n-1 < len(lines) {
		return lines[n-1]
	}
	return ""
}

// perturb adds indentation (tabs or spaces) to some lines and trailing
// spaces or tabs to others, never inside a literal.
func perturb(src string, file *syntax.File, r *rand.Rand) string {
	inLiteral := make([]bool, len(src)+1)
	syntax.Inspect(file, func(n syntax.Node) bool {
		if l, ok := n.(*syntax.BasicLit); ok {
			for p := int(l.Pos()); p < int(l.End()) && p <= len(src); p++ {
				inLiteral[p] = true
			}
		}
		return true
	})
	var b strings.Builder
	at := 0
	for _, line := range strings.SplitAfter(src, "\n") {
		body := strings.TrimSuffix(line, "\n")
		start, end := at, at+len(body)
		at += len(line)
		if strings.TrimSpace(body) != "" && !inLiteral[start] && r.IntN(3) == 0 {
			b.WriteString([]string{"\t", "  ", " \t"}[r.IntN(3)])
		}
		b.WriteString(body)
		if !inLiteral[end] && end > start && r.IntN(3) == 0 {
			b.WriteString([]string{" ", "\t", "   "}[r.IntN(3)])
		}
		if strings.HasSuffix(line, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String()
}
