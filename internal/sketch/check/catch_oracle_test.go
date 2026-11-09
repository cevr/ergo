package check_test

import (
	"fmt"
	"iter"
	"sort"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/sketch/check"
)

// The catch oracle of the stage C round 2 review (its test-strength
// finding), adopted: cumulative catch coverage over a row of two error
// types (rows.md, Catch 2: one matrix per error type), with alternatives
// inside an arm, a `case _` rest arm, and both a total catch and a partial
// `try … catch`. The oracle tracks the four values (E or F, B true or
// false) directly. A partial catch that handles the whole row is a
// redundant try, S108 at the try (review O3). It checks a fixed set of
// 12,262 programs and fails rather than run past its time bound.
//
// The programs are a split (gate-fast T1): part k checks every fourth
// program from program k, as a top-level test of its own, each part under
// the time bound. Under -short a part keeps every eighth program of its
// share.
var catchOracleTwoTypes = check.NewSplit(check.Split[catchProgram]{
	Name: "TestCatchOracleTwoTypes", Parts: 4, Short: 8,
	Cases: catchPrograms,
	Key:   func(p catchProgram) string { return fmt.Sprintf("partial=%v\n%s", p.partial, p.source()) },
	Run:   runCatchOracle,
})

func TestCatchOracleTwoTypesPart0(t *testing.T) { catchOracleTwoTypes.Part(t, 0) }
func TestCatchOracleTwoTypesPart1(t *testing.T) { catchOracleTwoTypes.Part(t, 1) }
func TestCatchOracleTwoTypesPart2(t *testing.T) { catchOracleTwoTypes.Part(t, 2) }
func TestCatchOracleTwoTypesPart3(t *testing.T) { catchOracleTwoTypes.Part(t, 3) }

type catchPat struct {
	text string
	ty   int // 0 E, 1 F, -1 rest
	b    int // 0 false, 1 true, 2 any
}

type catchArm []catchPat

// catchProgram is one program: its arms, in a total or a partial catch.
type catchProgram struct {
	arms    []catchArm
	partial bool
}

// catchPrograms yields the 12,262 programs: for each kind of catch, every
// arm alone and before every arm, then every three single-pattern arms.
func catchPrograms(yield func(catchProgram) bool) {
	var pool []catchPat
	for ty, name := range []string{"E", "F"} {
		pool = append(pool,
			catchPat{name + "{B: true}", ty, 1},
			catchPat{name + "{B: false}", ty, 0},
			catchPat{name + "{B: _}", ty, 2},
			catchPat{name, ty, 2})
	}
	rest := catchPat{"_", -1, 2}
	var singles, all []catchArm
	for _, p := range pool {
		singles = append(singles, catchArm{p})
	}
	singles = append(singles, catchArm{rest})
	all = append(all, singles...)
	for _, p := range pool {
		for _, q := range pool {
			all = append(all, catchArm{p, q})
		}
	}
	for _, partial := range []bool{false, true} {
		for _, a := range all {
			if !yield(catchProgram{[]catchArm{a}, partial}) {
				return
			}
			for _, c := range all {
				if !yield(catchProgram{[]catchArm{a, c}, partial}) {
					return
				}
			}
		}
		for _, a := range singles {
			for _, c := range singles {
				for _, d := range singles {
					if !yield(catchProgram{[]catchArm{a, c, d}, partial}) {
						return
					}
				}
			}
		}
	}
}

func (p catchProgram) source() string {
	var b strings.Builder
	b.WriteString("type E { B bool }\ntype F { B bool }\nfunc g() int fails E | F { 1 }\n")
	if p.partial {
		b.WriteString("func f() int fails E | F {\n\ttry g() catch {\n")
	} else {
		b.WriteString("func f() int {\n\tg() catch {\n")
	}
	for i, a := range p.arms {
		var ps []string
		for _, q := range a {
			ps = append(ps, q.text)
		}
		fmt.Fprintf(&b, "\tcase %s: %d\n", strings.Join(ps, ", "), i)
	}
	b.WriteString("\t}\n}\n")
	return b.String()
}

// want is the oracle's verdict, as CODE:LINE.
func (p catchProgram) want() string {
	const first = 6
	var covered [2][2]bool
	rested := false
	var want []string
	for i, a := range p.arms {
		for _, q := range a {
			if rested {
				want = append(want, fmt.Sprintf("S108:%d", first+i))
				continue
			}
			if q.ty < 0 {
				all := covered[0][0] && covered[0][1] && covered[1][0] && covered[1][1]
				if all {
					want = append(want, fmt.Sprintf("S108:%d", first+i))
				}
				rested = true
				continue
			}
			useful := false
			for v := 0; v < 2; v++ {
				if (q.b == 2 || q.b == v) && !covered[q.ty][v] {
					useful = true
				}
			}
			if !useful {
				want = append(want, fmt.Sprintf("S108:%d", first+i))
				continue
			}
			for v := 0; v < 2; v++ {
				if q.b == 2 || q.b == v {
					covered[q.ty][v] = true
				}
			}
		}
	}
	total := rested || (covered[0][0] && covered[0][1] && covered[1][0] && covered[1][1])
	switch {
	case !p.partial && !total:
		want = append(want, "S103:5")
	case p.partial && total:
		want = append(want, "S108:5")
	}
	sort.Strings(want)
	return strings.Join(want, " ")
}

func runCatchOracle(t *testing.T, programs iter.Seq[catchProgram]) {
	const bound = 60 * time.Second
	start := time.Now()
	n := 0
	for p := range programs {
		if time.Since(start) > bound {
			t.Fatalf("over the time bound %v after %d programs", bound, n)
		}
		src := p.source()
		got, d := codeLines(src)
		if want := p.want(); got != want {
			t.Errorf("%s\nwant [%s], got [%s] %v", src, want, got, d)
		}
		n++
	}
}
