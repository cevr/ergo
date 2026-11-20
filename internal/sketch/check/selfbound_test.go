package check_test

import (
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestSelfBoundRows holds self-bounded operation types and the constraint
// reasons of CONSTRAINTS C2 (types.md, Generics 2; decision 6). Each row is
// the smallest program for one rule, with the one diagnostic it gets, or
// none: a diagnostic's text and position are the rule here.
// Mutants it alone catches (testdata/mutants.json): c2-self-bound-not-seen,
// c2-clause-bound-unchecked, c2-cycle-unreported,
// c2-group-note-this-expression, c2-predeclared-reason-old,
// c2-tie-other-type (through Law.Ties).
func TestSelfBoundRows(t *testing.T) {
	const ranked = "type Ranked[T Ranked[T]] {\n\tCompare(other T) int,\n\n\tlaw Reflexive(a T) { a.Compare(a) == 0 },\n}\n\n"
	for _, row := range []struct {
		name, src string
		want      string // "CODE line:column message prefix", or "" for none
	}{
		{"a self bound's law calls its operation", ranked + "type Level int implements Ranked[Level]\n\nfunc Compare(a Level, b Level) int { int(a) - int(b) }\n\nfunc main() {}\n", ""},
		{"an operation the bound lacks", "type Ranked[T Ranked[T]] {\n\tCompare(other T) int,\n\n\tlaw L(a T) { a.Missing() == 0 },\n}\n\nfunc main() {}\n", "S104 4:17 T has no field or method Missing"},
		{"a clause whose instance breaks the bound", ranked + "type Other int\n\ntype Tag int implements Ranked[Other]\n\nfunc Compare(a Tag, b Other) int { 0 }\n\nfunc main() {}\n",
			"S106 9:25 Tag implements Ranked[Other], and Ranked's T must satisfy Ranked[T]: Other does not declare that it implements Ranked[Other]"},
		{"a bound that grows its argument", "type R[T R[[]T]] {\n\tM(x T) int,\n}\n\nfunc main() {}\n", "S108 1:10 instantiation cycle: R is instantiated with []T"},
		{"the group note spells the constraint", "type Order[T] {\n\tCompare(a T, b T) int,\n}\n\nfunc Least[T, O Order[T]](a T, o O) T { a }\n\ntype Tag { N int }\n\ntype ByN {} implements Order[Tag]\n\nfunc Compare(_ ByN, a Tag, b Tag) int { 0 }\n\nfunc main() {\n\tlet _ = Least(Tag{N: 1}, ByN)\n}\n",
			"S108 14:10 Tag does not satisfy the constraint Order[Tag] of Least's T: Tag does not declare that it implements Order[Tag]: write implements Order[Tag] after Tag's body (types.md, Interface assignability 1); [T, O Order[T]] constrains T as it does O: write [T any, O Order[T]] to leave T unconstrained"},
		{"a predeclared type declares nothing", ranked + "func Top[T Ranked[T]](a T) T { a }\n\nfunc main() {\n\tlet _ = Top(1)\n}\n",
			"S108 10:10 int does not satisfy the constraint Ranked[int] of Top's T: int is a predeclared type and declares nothing; define a type over it that declares the constraint: type N int implements Ranked[N]"},
		{"a slice declares nothing", ranked + "func Top[T Ranked[T]](a T) T { a }\n\nfunc main() {\n\tlet _ = Top([]int{1})\n}\n",
			"S108 10:10 []int does not satisfy the constraint Ranked[[]int] of Top's T: []int is not a declared type and declares nothing; define a type over it that declares the constraint: type N []int implements Ranked[N]"},
		{"two parameters of different types are no tie", "type Pair { X int }\n\nlaw Mixed(a Pair, n int) { n + 1 != n }\n\nfunc main() {}\n", ""},
	} {
		pkg, ds := check.CheckSource(row.src)
		var got []string
		for _, d := range ds {
			got = append(got, d.Code+" "+strconv.Itoa(d.Line)+":"+strconv.Itoa(d.Column)+" "+d.Message)
		}
		switch {
		case row.want == "" && len(got) > 0:
			t.Errorf("%s: want no diagnostic, got %q\n%s", row.name, got, row.src)
		case row.want != "" && (len(got) != 1 || !strings.HasPrefix(got[0], row.want)):
			t.Errorf("%s: want one diagnostic %q…, got %q\n%s", row.name, row.want, got, row.src)
		}
		if row.name == "two parameters of different types are no tie" && pkg != nil {
			for _, l := range pkg.Laws {
				if l.Name == "Mixed" && l.Ties != nil {
					t.Errorf("%s: Mixed ties %v; a Pair never stands for an int", row.name, l.Ties)
				}
			}
		}
	}
}
