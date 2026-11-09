package lower

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// TestAssertShowsValues is the acceptance check of T3 (tl-assert-values,
// grammar.md Declarations 11): a failed assert of a comparison prints both
// operands as Ergo literals of their types (the property printer), each
// operand evaluated once; two strings of more than one line add a line
// diff; a comparison of two constants, and one whose operand has no
// literal, print the source text alone, as before.
//
// It builds Go because it runs the emitted test harness under go test: the
// harness's verdicts and the values it prints are the output; no in-process
// check observes that, and Ergo has no reference interpreter yet (test-speed
// T8).
func TestAssertShowsValues(t *testing.T) {
	needsBuild(t)
	src := `func double(n int) int { n + n + 1 }

test Doubles {
	assert double(2) == 4
}

test Once {
	let mut n = 0
	let next = func() int {
		n = n + 1
		n
	}
	assert next() == 0
}

test Order {
	assert double(1) >= double(2)
}

test Lines {
	let a = "a\nb\nc"
	assert a == "a\nB\nc\nd"
}

test OneLine {
	let s = "x"
	assert s == "y"
}

test Constants {
	assert 1 == 2
}

type Point { X int, Y string }

test Records {
	let p = Point{X: 1, Y: "a"}
	assert p == Point{X: 2, Y: "a"}
}

test Logic {
	assert double(1) == 3 && double(2) == 4
}
`
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "assertvalues", src)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["assertvalues"])
	want := []string{
		"FAIL Doubles: assert failed at main.ergo:4:2: double(2) == 4: left 5, right 4",
		"FAIL Once: assert failed at main.ergo:13:2: next() == 0: left 1, right 0",
		"FAIL Order: assert failed at main.ergo:17:2: double(1) >= double(2): left 3, right 5",
		"FAIL Lines: assert failed at main.ergo:22:2: a == \"a\\nB\\nc\\nd\": left \"a\\nb\\nc\", right \"a\\nB\\nc\\nd\"",
		"--- left",
		"+++ right",
		"  a",
		"- b",
		"+ B",
		"  c",
		"+ d",
		"FAIL OneLine: assert failed at main.ergo:27:2: s == \"y\": left \"x\", right \"y\"",
		"FAIL Constants: assert failed at main.ergo:31:2: 1 == 2",
		"FAIL Records: assert failed at main.ergo:38:2: p == Point{X: 2, Y: \"a\"}: left Point{X: 1, Y: \"a\"}, right Point{X: 2, Y: \"a\"}",
		"FAIL Logic: assert failed at main.ergo:42:2: double(1) == 3 && double(2) == 4",
		"FAIL (8 of 8 tests)",
	}
	if code != 1 || strings.Join(lines(stdout), "\n") != strings.Join(want, "\n") {
		t.Fatalf("exit %d, stdout:\n%s\nwant:\n%s\nstderr:\n%s", code, stdout, strings.Join(want, "\n"), firstLines(stderr, 8))
	}
}

// printKinds are the type kinds of TestAssertLiteralsReadBack: a type, and
// the declarations it needs.
var printKinds = []struct{ name, typ string }{
	{"Int", "int"}, {"Int8", "int8"}, {"Uint16", "uint16"}, {"Int64", "int64"}, {"Rune", "rune"}, {"Byte", "byte"},
	{"Float64", "float64"}, {"Float32", "float32"}, {"String", "string"}, {"Bool", "bool"}, {"Duration", "duration"},
	{"Tuple", "(int, string)"}, {"Point", "Point"}, {"Shape", "Shape"}, {"Celsius", "Celsius"}, {"Name", "Name"},
	{"Holder", "Holder"}, {"Box", "Box[int]"}, {"Pair", "Pair"},
}

const printDecls = `type Point { X int, Y string }

type Shape = Circle{R float64} | Square{S int} | Empty{}

type Celsius float64

type Name string

type Holder { Label Name, Shape Shape, At Point }

type Box[T] { V T }

type Pair (Name, Celsius)
`

// TestAssertLiteralsReadBack is T3's property: for generated values of each
// type kind (five seeds), a failed `x != x` shows left and right as the
// literal the property printer gives the argument, and that literal, read
// back by `let v T = LIT` in a second program, prints the same literal: the
// text a failed assert shows parses back to the same value.
//
// It builds Go because it runs the emitted test harness under go test: the
// harness's verdicts and the values it prints are the output; no in-process
// check observes that, and Ergo has no reference interpreter yet (test-speed
// T8).
func TestAssertLiteralsReadBack(t *testing.T) {
	needsBuild(t)
	var show strings.Builder
	show.WriteString(printDecls)
	for _, k := range printKinds {
		fmt.Fprintf(&show, "\ntest Show%s(x %s) {\n\tassert x != x\n}\n", k.name, k.typ)
	}
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "printshow", show.String())}, true)
	if exes == nil {
		return
	}
	line := regexp.MustCompile(`^FAIL Show(\w+) \(case 1 of 100, seed \d+, size 0: x = (.*)\): assert failed at main\.ergo:\d+:\d+: x != x: left (.*), right (.*)$`)
	type shown struct{ kind, typ, lit string }
	var seen []shown
	for seed := 1; seed <= 5; seed++ {
		stdout, stderr, code := runExeArgs(t, exes["printshow"], "--seed", fmt.Sprint(seed))
		if code != 1 {
			t.Fatalf("seed %d: exit %d\n%s\n%s", seed, code, stdout, stderr)
		}
		got := lines(stdout)
		if len(got) != len(printKinds)+1 {
			t.Fatalf("seed %d: %d lines, want %d FAIL lines and the count:\n%s", seed, len(got), len(printKinds), stdout)
		}
		for i, k := range printKinds {
			m := line.FindStringSubmatch(got[i])
			if m == nil || m[1] != k.name || m[2] != m[3] || m[3] != m[4] {
				t.Fatalf("seed %d, %s: %q; want the argument's literal on both sides", seed, k.name, got[i])
			}
			seen = append(seen, shown{k.name, k.typ, m[2]})
		}
	}
	var back strings.Builder
	back.WriteString(printDecls)
	for i, s := range seen {
		fmt.Fprintf(&back, "\ntest Back%d {\n\tlet v %s = %s\n\tassert v != v\n}\n", i, s.typ, s.lit)
	}
	exes = buildAll(t, []*lowered{lowerTestBuild(t, "printback", back.String())}, true)
	if exes == nil {
		return
	}
	stdout, stderr, _ := runExe(t, exes["printback"])
	got := lines(stdout)
	if len(got) != len(seen)+1 {
		t.Fatalf("%d lines, want %d:\n%s\n%s", len(got), len(seen)+1, stdout, stderr)
	}
	for i, s := range seen {
		want := fmt.Sprintf("FAIL Back%d: assert failed at main.ergo:", i)
		suffix := fmt.Sprintf(": v != v: left %s, right %s", s.lit, s.lit)
		if !strings.HasPrefix(got[i], want) || !strings.HasSuffix(got[i], suffix) {
			t.Errorf("%s literal %s read back as %q", s.kind, s.lit, got[i])
		}
	}
}
