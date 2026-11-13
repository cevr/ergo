package main

import (
	"bytes"
	"fmt"
	"html"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/proclimit"
)

// TestMdhtmlAgreesWithControl is the correctness property of
// examples/mdhtml (a CommonMark 0.31.2 converter written in Ergo): the
// Ergo program and the hand Go control (testdata/mdhtml/control) write
// Markdown as the specification says.
//
//   - The specification's examples (pinned in the example's testdata):
//     with -unsafe, each prints the example's HTML byte for byte; in the
//     default safe mode, both binaries print the same.
//   - Seeded documents spliced from the examples' lines, some lines
//     indented, quoted or made list items, so blocks meet that no example
//     puts together: both binaries print the same in each mode, with and
//     without -highlight.
//   - The workload of mdhtml -gen at two sizes, and its -blocks count.
//   - The entity table both programs hold is Go's html.UnescapeString of
//     each name.
//
// Every run's standard output, standard error and status must be the
// same on both binaries. A failing seed prints the document and its
// replay command.
//
// It builds Go because the programs under test are the linked binaries:
// the Ergo program reads its file through the host, and the control is
// Go; no in-process check runs either. Each run is bounded by proclimit,
// and the test waits for its exit.
//
// It kills the example's mutants that no law sees (examples/mdhtml/
// testdata/mutants.json).
func TestMdhtmlAgreesWithControl(t *testing.T) {
	needsBuild(t)
	mdTableIsHTML5(t)
	ergoBin, work := buildCorpusExample(t, "mdhtml")
	control := buildHandControl(t, "mdhtml", work)
	bins := []string{ergoBin, control}
	dir := t.TempDir()
	path := filepath.Join(dir, "p.md")

	roots, err := filepath.Glob(filepath.Join("..", "..", "examples", "mdhtml", "testdata", "commonmark-spec-*"))
	if err != nil || len(roots) != 1 {
		t.Fatalf("the CommonMark spec is not in examples/mdhtml/testdata: %v %v", roots, err)
	}
	spec, err := os.ReadFile(filepath.Join(roots[0], "spec.txt"))
	if err != nil {
		t.Fatal(err)
	}
	examples := mdExamples(string(spec))
	if len(examples) == 0 {
		t.Fatal("spec.txt holds no example")
	}
	for _, e := range examples {
		writeFile(t, path, e.md)
		what := fmt.Sprintf("example %d (%s)", e.n, e.section)
		if got, want := mdBoth(t, bins, path, what, "", "-unsafe"), "stdout:\n"+e.html+"\nstderr:\n\nstatus 0\n"; got != want {
			t.Fatalf("%s: mdhtml -unsafe printed\n%s\nthe specification wants\n%s\nthe Markdown:\n%q", what, clip(got), clip(want), e.md)
		}
		mdBoth(t, bins, path, what, "")
	}

	seeds := 300
	first := uint64(1)
	if s := os.Getenv("MDHTML_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("MDHTML_SEED=%q is not a seed", s)
		}
		first, seeds = n, 1
	}
	for seed := first; seed < first+uint64(seeds); seed++ {
		r := rand.New(rand.NewPCG(seed, 0x3d47))
		replay := fmt.Sprintf("replay: MDHTML_SEED=%d go test -count=1 -run '^TestMdhtmlAgreesWithControl$' ./cmd/ergo", seed)
		what := "seed " + strconv.FormatUint(seed, 10)
		writeFile(t, path, mdSplice(r, examples))
		mdBoth(t, bins, path, what, replay, "-unsafe")
		mdBoth(t, bins, path, what, replay)
		mdBoth(t, bins, path, what, replay, "-highlight", "-title", "T & <t>")
	}

	for _, n := range []string{"2", "200"} {
		command := exec.Command(ergoBin, "-seed", strconv.FormatUint(first, 10), "-gen", n)
		var out bytes.Buffer
		command.Stdout = &out
		if err := proclimit.Run(command, 1<<30); err != nil {
			t.Fatalf("mdhtml -gen: %v", err)
		}
		writeFile(t, path, out.String())
		what := "the workload of " + n + " sections"
		mdBoth(t, bins, path, what, "", "-unsafe")
		mdBoth(t, bins, path, what, "")
		if got := runCorpus(t, ergoBin, "-blocks", path); !regexp.MustCompile(`^stdout:\n[1-9][0-9]*\n\nstderr:\n\nstatus 0\n$`).MatchString(got) {
			t.Fatalf("%s: mdhtml -blocks printed\n%s", what, clip(got))
		}
	}
}

// mdBoth runs args and path with each of bins, fails unless both print
// the same, and is what they print.
func mdBoth(t *testing.T, bins []string, path, what, replay string, args ...string) string {
	t.Helper()
	run := append(append([]string{}, args...), path)
	first := runCorpus(t, bins[0], run...)
	if second := runCorpus(t, bins[1], run...); second != first {
		src, _ := os.ReadFile(path)
		t.Fatalf("%s: mdhtml %s printed\n%s\nthe control printed\n%s\nthe Markdown:\n%q\n%s", what, strings.Join(args, " "), clip(first), clip(second), clip(string(src)), replay)
	}
	return first
}

// mdExample is one of the specification's examples.
type mdExample struct {
	n        int
	section  string
	md, html string
}

// mdExamples reads the examples of spec.txt: a fence of 32 backticks and
// "example", the Markdown, a line ".", the HTML, the fence; → is a tab.
func mdExamples(spec string) []mdExample {
	fence := strings.Repeat("`", 32)
	lines := strings.Split(spec, "\n")
	var out []mdExample
	section := ""
	for i := 0; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "#") {
			section = strings.TrimLeft(lines[i], "# ")
		}
		if lines[i] != fence+" example" {
			continue
		}
		var md, out2 strings.Builder
		i++
		for ; i < len(lines) && lines[i] != "."; i++ {
			md.WriteString(lines[i] + "\n")
		}
		for i++; i < len(lines) && lines[i] != fence; i++ {
			out2.WriteString(lines[i] + "\n")
		}
		tab := func(s string) string { return strings.ReplaceAll(s, "→", "\t") }
		out = append(out, mdExample{n: len(out) + 1, section: section, md: tab(md.String()), html: tab(out2.String())})
	}
	return out
}

// mdSplice is a document of runs of lines from random examples, each line
// kept, indented, quoted or made a list item, with blank lines between
// some runs.
func mdSplice(r *rand.Rand, examples []mdExample) string {
	var b strings.Builder
	for range 1 + r.IntN(6) {
		lines := strings.Split(strings.TrimSuffix(examples[r.IntN(len(examples))].md, "\n"), "\n")
		from := r.IntN(len(lines))
		to := from + 1 + r.IntN(len(lines)-from)
		prefix := []string{"", "", "", " ", "  ", "    ", "> ", "- ", "1. ", "\t"}[r.IntN(10)]
		for _, l := range lines[from:to] {
			if r.IntN(4) == 0 {
				b.WriteString(prefix)
			}
			b.WriteString(l)
			b.WriteString([]string{"\n", "\n", "\n", "\r\n", "\r"}[r.IntN(5)])
		}
		if r.IntN(2) == 0 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// mdTableIsHTML5 checks that the entity table of the Ergo program and of
// the control are the same, and that each name is what Go's
// html.UnescapeString reads (but nLt; and nGt;, which Go leaves out).
func mdTableIsHTML5(t *testing.T) {
	t.Helper()
	read := func(path string) string {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for _, m := range regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(string(src), -1) {
			b.WriteString(m[1])
		}
		return b.String()
	}
	ergo := read(filepath.Join("..", "..", "examples", "mdhtml", "markdown", "entities.ergo"))
	if goTable := read(filepath.Join("testdata", "mdhtml", "control", "entities.go")); goTable != ergo {
		t.Fatal("the entity tables of examples/mdhtml and of its control differ")
	}
	names := 0
	for _, entry := range strings.Split(ergo, ",") {
		name, codes, ok := strings.Cut(entry, ":")
		if !ok {
			continue
		}
		names++
		var want strings.Builder
		for _, h := range strings.Split(codes, "+") {
			r, err := strconv.ParseUint(h, 16, 32)
			if err != nil {
				t.Fatalf("entity %s: %q is not hex", name, h)
			}
			want.WriteRune(rune(r))
		}
		if name == "nLt" || name == "nGt" {
			continue
		}
		if got := html.UnescapeString("&" + name + ";"); got != want.String() {
			t.Fatalf("entity %s: the table holds %q, Go's html reads %q", name, want.String(), got)
		}
	}
	if names == 0 {
		t.Fatal("the entity table is empty")
	}
}
