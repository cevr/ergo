package check_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestLawDomain is the law rules over their whole domain (spec A3; laws.go
// rules 2, 4 and 5; types.md Laws rule 8): every result type × every exit
// form × every prefix, enumerated, not sampled. A law body is `<prefix>
// <exit>`, and the exit gives the result value as the tail, as a whole-body
// return, or as an early return before the same tail (rows.md, Totality 3:
// `return e` means the same as a tail e). The verdict of each program comes
// from lawOracle, a reference written from D-E4 that reads only the facts
// the tables state, never the checker:
//
//   - S108 at each success exit (a tail or a return) iff the result's
//     underlying type is not bool;
//   - S105 at the tail iff the result is fallible and untried; a return
//     passes a fallible value's failures out of the law (rule 7), so a
//     return is never S105;
//   - S102 at the call iff a service-charging call stands outside a with
//     that discharges it;
//   - S108 at a function literal of the prefix iff it returns a value its
//     own declared result refuses: the literal keeps its own result, not
//     the law's rule;
//   - nothing else.
//
// Each program is checked again in every declaration order lawOrders
// gives, and the verdict must be the same (the declaration-order oracle).

// lawDomainDecls are the declarations every program shares, one per entry
// so that they can be permuted. `$LAW` in the owner is replaced by the law
// under test.
var lawDomainDecls = []string{
	"type Malformed {}",
	"type Codec {\n\tDecode(s string) int fails Malformed,\n\tSame(v int) bool,\n\tCheck(v int) bool fails Malformed,\n\n$LAW}",
	"type Store {\n\tGet() string,\n}",
	"type Mem {}",
	"func Get(_ Mem) string {\n\t\"ok\"\n}",
	"func read() string {\n\tStore.Get()\n}",
	"type Flag bool",
	"type Flag2 Flag",
	"type Count int",
	"type Rec { N int }",
	"type Dot {}",
	"type Line { N int }",
	"type Shape = Dot | Line",
	"type Shower {\n\tShow() string,\n}",
}

// lawTail is one tail: the law's parameters, the tail expression, and the
// facts the oracle reads.
type lawTail struct {
	name, params, tail string
	// underBool: the tail's type has bool as its underlying type.
	underBool bool
	// fallible: the tail can fail; tried: it is written under try.
	fallible, tried bool
}

// lawNotV1Basics are basic types that are not v1 types (bindings.go,
// notPreludeNames): a parameter cannot have them, so they are outside the
// domain of a law's tail.
var lawNotV1Basics = map[string]bool{"uintptr": true, "complex64": true, "complex128": true}

// lawTails: one value of every basic type, then one of every other kind of
// type, then the operation calls.
func lawTails() []lawTail {
	var out []lawTail
	for _, b := range check.Typ[1:] {
		if lawNotV1Basics[b.Name] {
			continue
		}
		out = append(out, lawTail{name: b.Name, params: "v " + b.Name, tail: "v", underBool: b.Name == "bool"})
	}
	return append(out,
		lawTail{name: "a type over bool", params: "v Flag", tail: "v", underBool: true},
		lawTail{name: "a type over a type over bool", params: "v Flag2", tail: "v", underBool: true},
		lawTail{name: "a type over int", params: "v Count", tail: "v"},
		lawTail{name: "a record", params: "v Rec", tail: "v"},
		lawTail{name: "a union", params: "v Shape", tail: "v"},
		lawTail{name: "a tuple", params: "v (int, bool)", tail: "v"},
		lawTail{name: "a slice", params: "v []bool", tail: "v"},
		lawTail{name: "a Map", params: "v Map[string, bool]", tail: "v"},
		lawTail{name: "a function value", tail: "func(n int) bool { n == 0 }"},
		lawTail{name: "an operation-type value", params: "v Shower", tail: "v"},
		lawTail{name: "an operation call", params: "v int", tail: "Same(v)", underBool: true},
		lawTail{name: "a fallible operation call, tried", params: "v int", tail: "try Check(v)", underBool: true, fallible: true, tried: true},
		lawTail{name: "a fallible operation call, untried", params: "v int", tail: "Check(v)", underBool: true, fallible: true},
	)
}

// lawPrefix is one prefix: the statements before the tail, and the facts
// the oracle reads.
type lawPrefix struct {
	name, stmts string
	// charges: a call that charges a service; discharged: a with around it
	// discharges that service.
	charges, discharged bool
	// litRefused: a function literal in the prefix returns a value that its
	// own declared result refuses by assignment (a Flag as a bool).
	litRefused bool
}

var lawPrefixes = []lawPrefix{
	{name: "nothing"},
	{name: "a fallible operation call", stmts: "let _ = try Decode(\"1\")"},
	{name: "a charging package function, bare", stmts: "let _ = read()", charges: true},
	{name: "a charging package function, discharged", stmts: "with context { Store: Mem } {\n\t\t\tlet _ = read()\n\t\t}", charges: true, discharged: true},
	{name: "a capability operation, bare", stmts: "let _ = Store.Get()", charges: true},
	{name: "a literal returning its declared Flag", stmts: "let _ = func(x Flag) Flag { if x { return x }; x }"},
	{name: "a literal returning a Flag as its declared bool", stmts: "let _ = func(x Flag) bool { return x }", litRefused: true},
}

// lawExit is how the law gives its result: as the tail, as a whole-body
// return, or as an early return under a condition before the same tail.
type lawExit struct {
	name string
	// ret: the result is returned; tail: the result is also the tail.
	ret, tail bool
}

var lawExits = []lawExit{
	{name: "the tail", tail: true},
	{name: "a return", ret: true},
	{name: "an early return", ret: true, tail: true},
}

// lawProgram assembles the declarations in order with the law in its owner,
// and returns the program with the lines of the prefix's first statement,
// of the return (0 if none) and of the tail (0 if none).
func lawProgram(decls []string, p lawPrefix, tl lawTail, ex lawExit) (src string, prefixLine, retLine, tailLine int) {
	body := ""
	if p.stmts != "" {
		body += "\t\t$P" + p.stmts + "\n"
	}
	params := tl.params
	switch {
	case ex.ret && ex.tail:
		if params != "" {
			params += ", "
		}
		params += "b bool"
		body += "\t\t$Rif b { return " + tl.tail + " }\n"
	case ex.ret:
		body += "\t\t$Rreturn " + tl.tail + "\n"
	}
	if ex.tail {
		body += "\t\t$T" + tl.tail + "\n"
	}
	law := "\tlaw L(" + params + ") {\n" + body + "\t},\n"
	var b strings.Builder
	for _, d := range decls {
		b.WriteString(strings.Replace(d, "$LAW", law, 1))
		b.WriteString("\n\n")
	}
	src = b.String()
	for _, m := range []struct {
		marker string
		line   *int
	}{{"$P", &prefixLine}, {"$R", &retLine}, {"$T", &tailLine}} {
		if i := strings.Index(src, m.marker); i >= 0 {
			*m.line = strings.Count(src[:i], "\n") + 1
			src = src[:i] + src[i+len(m.marker):]
		}
	}
	return src, prefixLine, retLine, tailLine
}

// lawOracle is the reference verdict of D-E4 for one program, as CODE@LINE.
func lawOracle(p lawPrefix, tl lawTail, ex lawExit, prefixLine, retLine, tailLine int) []string {
	var want []string
	if p.charges && !p.discharged {
		want = append(want, fmt.Sprintf("S102@%d", prefixLine))
	}
	if p.litRefused {
		want = append(want, fmt.Sprintf("S108@%d", prefixLine))
	}
	for _, exit := range []struct {
		is   bool
		line int
	}{{ex.ret, retLine}, {ex.tail, tailLine}} {
		if exit.is && !tl.underBool {
			want = append(want, fmt.Sprintf("S108@%d", exit.line))
		}
	}
	if ex.tail && tl.fallible && !tl.tried {
		want = append(want, fmt.Sprintf("S105@%d", tailLine))
	}
	sort.Strings(want)
	return want
}

// lawOrders are the declaration orders a program checks in, the written
// order first: every rotation of the written order and of its reverse, so
// every declaration stands before and after every other.
func lawOrders(decls []string) [][]string {
	n := len(decls)
	rev := make([]string, n)
	for i, d := range decls {
		rev[n-1-i] = d
	}
	var out [][]string
	for _, base := range [][]string{decls, rev} {
		for k := 0; k < n; k++ {
			rot := make([]string, n)
			for i := range base {
				rot[i] = base[(i+k)%n]
			}
			out = append(out, rot)
		}
	}
	return out
}

func TestLawDomain(t *testing.T) {
	tails := lawTails()
	orders := lawOrders(lawDomainDecls)
	programs := 0
	verdicts := map[string]int{}
	for _, p := range lawPrefixes {
		for _, tl := range tails {
			for _, ex := range lawExits {
				programs++
				name := p.name + " then " + tl.name + " as " + ex.name
				var first []string
				for oi, decls := range orders {
					src, pl, rl, tll := lawProgram(decls, p, tl, ex)
					want := lawOracle(p, tl, ex, pl, rl, tll)
					if oi == 0 {
						first = want
						verdicts[strings.Join(lawCodes(want), " ")]++
					}
					_, diags := check.CheckSource(src)
					have := got(diags)
					if strings.Join(have, " ") != strings.Join(want, " ") {
						t.Errorf("%s (order %d): want %v, got %v\n\t%s\n%s", name, oi, want, have, describe(diags), src)
						continue
					}
					// The order changes the lines, never the verdict.
					if strings.Join(lawCodes(want), " ") != strings.Join(lawCodes(first), " ") {
						t.Errorf("%s (order %d): the oracle's codes %v differ from the written order's %v", name, oi, want, first)
					}
					// An exit's S108 is the law's rule; the literal's is its
					// own result's.
					for _, d := range diags {
						law := strings.Contains(d.Message, "a law's body ends in a bool, not ")
						switch {
						case d.Code != "S108":
						case d.Line == pl && law:
							t.Errorf("%s (order %d): the literal's S108 is the law's rule: %s", name, oi, d)
						case d.Line != pl && !law:
							t.Errorf("%s (order %d): the exit's S108 is not the law's rule: %s", name, oi, d)
						}
					}
				}
			}
		}
	}
	// The domain meets every verdict the oracle can give.
	for _, v := range []string{"", "S102", "S108", "S105", "S102 S108", "S102 S105", "S108 S108", "S108 S108 S108", "S105 S108"} {
		if verdicts[v] == 0 {
			t.Errorf("no program has the verdict %q", v)
		}
	}
	t.Logf("%d programs: %d prefixes × %d result types × %d exits, %d declaration orders each; verdicts %v", programs, len(lawPrefixes), len(tails), len(lawExits), len(orders), verdicts)
}

// lawCodes drops the lines of CODE@LINE entries.
func lawCodes(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i], _, _ = strings.Cut(x, "@")
	}
	sort.Strings(out)
	return out
}
