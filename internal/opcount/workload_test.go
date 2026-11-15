package opcount_test

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/opcount"
)

// genWorkload is a valid workload and its file, its lines in a random
// order with comments and blank lines between them.
func genWorkload(r *rand.Rand) (opcount.Workload, []string) {
	w := opcount.Workload{Unit: []string{"op", "row", "block", "msg-1"}[r.IntN(4)], Procs: 1}
	w.Small = 1 + r.IntN(1000)
	w.Large = w.Small + 1 + r.IntN(100000)
	lines := []string{"unit " + w.Unit, fmt.Sprintf("sizes %d %d", w.Small, w.Large)}
	switch r.IntN(3) {
	case 0:
		w.Args = []string{"bench", "{n}"}
	case 1:
		w.InputKind, w.InputArgs = "self", []string{"gen", "-n", "{n}"}
		w.Args = []string{"-"}
		lines = append(lines, "input self gen -n {n}")
	default:
		w.InputKind, w.InputDir, w.InputArgs = "go", "cmd/x/gen", []string{"{n}", "7"}
		w.Args = []string{"{input}", "1"}
		lines = append(lines, "input go cmd/x/gen {n} 7")
	}
	lines = append(lines, "args "+strings.Join(w.Args, " "))
	if r.IntN(2) == 0 {
		w.Procs = 1 + r.IntN(8)
		lines = append(lines, fmt.Sprintf("procs %d", w.Procs))
	}
	if r.IntN(2) == 0 {
		w.Control = "cmd/x/control"
		lines = append(lines, "control cmd/x/control")
	}
	r.Shuffle(len(lines), func(i, j int) { lines[i], lines[j] = lines[j], lines[i] })
	var out []string
	for _, l := range lines {
		if r.IntN(3) == 0 {
			out = append(out, "", "# a comment")
		}
		out = append(out, l)
	}
	return w, out
}

// A workload file reads back as the workload it was written from, and a
// file with one bad line is refused with that line's number (a missing
// required line: with no line). A seeded property over generated files;
// a failing seed prints the file.
//
// Mutants: workload-sizes-unordered, workload-unknown-key-admitted,
// workload-duplicate-admitted, workload-unsized-admitted,
// workload-required-unchecked.
func TestWorkloadFileReads(t *testing.T) {
	type spoil struct {
		name string
		do   func(lines []string, at int, r *rand.Rand) ([]string, int)
	}
	keyAt := func(lines []string, key string) int {
		return slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, key+" ") })
	}
	spoils := []spoil{
		{"unknown key", func(lines []string, at int, _ *rand.Rand) ([]string, int) {
			return slices.Insert(lines, at, "repeat 3"), at + 1
		}},
		{"repeated key", func(lines []string, at int, _ *rand.Rand) ([]string, int) {
			return slices.Insert(lines, len(lines), lines[keyAt(lines, "unit")]), len(lines) + 1
		}},
		{"sizes out of order", func(lines []string, _ int, r *rand.Rand) ([]string, int) {
			i := keyAt(lines, "sizes")
			a := 1 + r.IntN(50)
			lines[i] = fmt.Sprintf("sizes %d %d", a+r.IntN(2)*r.IntN(50), a)
			return lines, i + 1
		}},
		{"unknown placeholder", func(lines []string, _ int, _ *rand.Rand) ([]string, int) {
			i := keyAt(lines, "args")
			lines[i] += " {size}"
			return lines, i + 1
		}},
		{"no size", func(lines []string, _ int, _ *rand.Rand) ([]string, int) {
			out := []string{}
			for _, l := range lines {
				out = append(out, strings.ReplaceAll(l, "{n}", "100"))
			}
			return out, keyAt(out, "args") + 1
		}},
		{"missing unit", func(lines []string, _ int, _ *rand.Rand) ([]string, int) {
			i := keyAt(lines, "unit")
			lines[i] = "# no unit"
			return lines, 0
		}},
	}
	for seed := range uint64(500) {
		r := rand.New(rand.NewPCG(seed, 11))
		want, lines := genWorkload(r)
		got, problems := opcount.ParseWorkload(strings.Join(lines, "\n"))
		if len(problems) > 0 || !equalWorkload(got, want) {
			t.Fatalf("seed %d: %v, %+v; want %+v from\n%s", seed, problems, got, want, strings.Join(lines, "\n"))
		}
		s := spoils[int(seed)%len(spoils)]
		bad, line := s.do(slices.Clone(lines), r.IntN(len(lines)+1), r)
		_, problems = opcount.ParseWorkload(strings.Join(bad, "\n"))
		if !slices.ContainsFunc(problems, func(p opcount.Problem) bool { return p.Line == line }) {
			t.Fatalf("seed %d (%s): problems %v, want one on line %d of\n%s", seed, s.name, problems, line, strings.Join(bad, "\n"))
		}
	}
}

func equalWorkload(a, b opcount.Workload) bool {
	return a.Unit == b.Unit && a.Small == b.Small && a.Large == b.Large && slices.Equal(a.Args, b.Args) &&
		a.Procs == b.Procs && a.InputKind == b.InputKind && a.InputDir == b.InputDir &&
		slices.Equal(a.InputArgs, b.InputArgs) && a.Control == b.Control && a.Binary == b.Binary
}
