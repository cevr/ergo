package check

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// TestLawPlacement pins S115's text and position (diagnostics.md, S115;
// types.md, Laws 15), each row a package of named files. Mutants
// (testdata/mutants.json): lawloc-subject-drops-call (a dot-called
// function is no subject, so the placed row reports), lawloc-any-file
// (the file check accepts every file, so the misplaced rows pass) and
// lawloc-helper-is-subject (a helper pins the law to its own file) and
// lawloc-reports-over-errors (S115 beside another diagnostic). It checks
// with S115 on (checkWith), whatever lawPlacementReported says.
func TestLawPlacement(t *testing.T) {
	ints := "type Ints []int\n\nfunc New(n int) Ints { Ints{n} }\n"
	sum := "func Sum(xs Ints) int {\n\tlet mut s = 0\n\tfor x in xs {\n\t\ts = s + x\n\t}\n\ts\n}\n"
	for _, row := range []struct {
		name  string
		files [][2]string
		want  string
	}{
		{
			// The law names Ints (a.ergo) by its parameter and Sum (b.ergo)
			// only by a dot call: b.ergo holds a subject.
			name:  "placed by a dot call",
			files: [][2]string{{"a.ergo", ints}, {"b.ergo", sum + "\nlaw SumOfOne(n int) { New(n).Sum() == n }\n"}},
		},
		{
			name:  "misplaced",
			files: [][2]string{{"a.ergo", ints}, {"b.ergo", sum}, {"laws.ergo", "law SumOfOne(xs Ints) { xs.Sum() == xs.Sum() }\n"}},
			want:  `laws.ergo:1:5+8 S115 law SumOfOne is about Ints (a.ergo) and Sum (b.ergo); state it in one of those files`,
		},
		{
			// A helper beside the law is no subject: what it names is.
			name:  "helper",
			files: [][2]string{{"a.ergo", ints + sum}, {"laws.ergo", "law SumOfOne(n int) { one(n) == n }\n\nfunc one(n int) int { New(n).Sum() }\n"}},
			want:  `laws.ergo:1:5+8 S115 law SumOfOne is about Sum (a.ergo); state it in that file`,
		},
		{
			name:  "nothing",
			files: [][2]string{{"a.ergo", ints}, {"b.ergo", "law AddCommutes(a int, b int) { a + b == b + a }\n"}},
			want:  `b.ergo:1:5+11 S115 law AddCommutes names nothing in package main; state it in the package it is about`,
		},
		{
			// Another diagnostic first: no S115 beside it.
			name:  "over an error",
			files: [][2]string{{"a.ergo", ints}, {"b.ergo", "law AddCommutes(a int, b int) { a + b == b + a }\n\nfunc bad() int { \"s\" }\n"}},
			want:  `b.ergo:3:18+3 S108 a string literal takes only a string type, not int`,
		},
		{
			// A test placed in the file of a dot-called subject (no laws in package).
			name:  "test placed by a dot call",
			files: [][2]string{{"a.ergo", ints}, {"b.ergo", sum + "\ntest SumOfOneWorks {\n\tassert New(1).Sum() == 1\n}\n"}},
		},
		{
			// A misplaced test in a package with no top-level laws.
			name:  "test misplaced",
			files: [][2]string{{"a.ergo", ints}, {"b.ergo", sum}, {"tests.ergo", "test SumOfOneWorks(xs Ints) {\n\tassert xs.Sum() == xs.Sum()\n}\n"}},
			want:  `tests.ergo:1:6+13 S115 test SumOfOneWorks is about Ints (a.ergo) and Sum (b.ergo); state it in one of those files`,
		},
		{
			// A helper beside a test is no subject: what it names is.
			name:  "test helper",
			files: [][2]string{{"a.ergo", ints + sum}, {"tests.ergo", "test SumOfOneWorks {\n\tassert one(1) == 1\n}\n\nfunc one(n int) int { New(n).Sum() }\n"}},
			want:  `tests.ergo:1:6+13 S115 test SumOfOneWorks is about Sum (a.ergo); state it in that file`,
		},
		{
			// A test that names nothing in its package.
			name:  "test nothing",
			files: [][2]string{{"a.ergo", ints}, {"b.ergo", "test AddWorks(a int, b int) {\n\tassert a + b == b + a\n}\n"}},
			want:  `b.ergo:1:6+8 S115 test AddWorks names nothing in package main; state it in the package it is about`,
		},
		{
			// Another diagnostic beside a misplaced test suppresses S115.
			name:  "test over an error",
			files: [][2]string{{"a.ergo", ints}, {"b.ergo", "test AddWorks(a int, b int) {\n\tassert a + b == b + a\n}\n\nfunc bad() int { \"s\" }\n"}},
			want:  `b.ergo:5:18+3 S108 a string literal takes only a string type, not int`,
		},
		{
			// A local test harness wrapper does not inject its plumbing types/function as subjects.
			name: "test local harness",
			files: [][2]string{
				{"harness.ergo", "type Run { Name string, Seed uint64, Site string, Source string }\ntype Fail { Message string }\nfunc Harness(r Run, body func() fails Fail needs Draws) fails Fail {\n\tlet _ = r\n\tlet _ = body\n}\n"},
				{"draws.ergo", "type Draws { Step() int, test Harness }\n"},
				{"target.ergo", "func Compute(n int) int { n + 1 }\n"},
				{"tests.ergo", "test ComputeWorks {\n\tassert Compute(Draws.Step()) > 0\n}\n"},
			},
			want: `tests.ergo:1:6+12 S115 test ComputeWorks is about Compute (target.ergo) and Step (draws.ergo); state it in one of those files`,
		},
	} {
		var files []*syntax.File
		for _, nf := range row.files {
			f, err := syntax.Parse(nf[1])
			if err != nil {
				t.Fatalf("%s: %s: %v", row.name, nf[0], err)
			}
			f.Name = nf[0]
			files = append(files, f)
		}
		_, ds := checkWith("main", files, nil, true)
		var got []string
		for _, d := range ds {
			got = append(got, fmt.Sprintf("%s:%d:%d+%d %s %s", files[d.File].Name, d.Line, d.Column, d.Length, d.Code, d.Message))
		}
		if strings.Join(got, "\n") != row.want {
			t.Errorf("%s:\n\tgot  %s\n\twant %s", row.name, strings.Join(got, "\n\t     "), row.want)
		}
	}
}
