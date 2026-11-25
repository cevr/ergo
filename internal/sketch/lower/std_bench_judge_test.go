package lower

import (
	"regexp"
	"testing"
)

// stdBenchJudgeProgram holds std bench's referee to its reference model
// over the plans the referee laws do not draw: the laws take lawPlanOf
// (Fixed designs of at most 6 pairs, at most one secondary) so that each
// decides inside check time's budget, and these properties take planOf's
// whole domain (Planned's looks at 8 to 40 pairs, Fixed up to 30 and 401,
// up to five secondaries), a Planned plan of three to six series, and
// oneFamily at one to five secondaries, where law LoneSecondaryKeepsItsWord
// takes one (JUDGE-PROP). It is std bench's own source as one test
// build followed by the properties, so they reach reference, planOf,
// scriptOf and oneFamily as the laws do.
var stdBenchJudgeProgram = stdBenchProgram + `
func familyPlanOf() Plan needs gen.Draws {
	let primary = drawSeries()
	let secondaries = gen.ListOf(2, 5, drawSeries)
	Plan{Primary: primary, Secondaries: secondaries, Design: Planned}
}

func machineAgrees(p Plan, m Scripted) bool {
	let want = with context { Meter: m } { Judge(p) }
	let mut s = Start(p)
	for _ in 0..(Bound(p.Design) + 1) {
		match Next(s) {
		case Finished as f:
			return f.Result == want
		case Run as r:
			let wrong = Measured{Block: Block{..r.Block, Number: r.Block.Number + 1}, Samples: []Sample{}}
			match RefereeStep(s, wrong) {
			case Reject:
			case _:
				return false
			}
			match RefereeStep(s, Measured{Block: r.Block, Samples: Measure(m, r.Block, seriesOf(p))}) {
			case Stay[Judging] as st:
				s = st.Next
			case Done[Result] as d:
				return d.Output == want
			case _:
				return false
			}
		}
	}
	false
}

test BenchJudgeMatchesReference {
	let p = gen.Named("plan", showPlan, planOf)
	let m = gen.Named("meter", showScript, func() Scripted needs gen.Draws { scriptOf(p) })
	let got = with context { Meter: m } { Judge(p) }
	assert got == reference(p, m)
}

test BenchPlannedFamilyMatchesReference {
	let p = gen.Named("plan", showPlan, familyPlanOf)
	let m = gen.Named("meter", showScript, func() Scripted needs gen.Draws { scriptOf(p) })
	let got = with context { Meter: m } { Judge(p) }
	assert got == reference(p, m)
}

test BenchMachineIsJudgeOnEveryPlan {
	let p = gen.Named("plan", showPlan, planOf)
	let m = gen.Named("meter", showScript, func() Scripted needs gen.Draws { scriptOf(p) })
	assert machineAgrees(p, m)
}

test BenchPlannedFamilyMachineIsJudge {
	let p = gen.Named("plan", showPlan, familyPlanOf)
	let m = gen.Named("meter", showScript, func() Scripted needs gen.Draws { scriptOf(p) })
	assert machineAgrees(p, m)
}

test BenchSecondariesAreOneFamily {
	let m = gen.Named("secondaries", func(v int) string { "\(v)" }, func() int needs gen.Draws { gen.IntIn(1, 5) })
	assert oneFamily(m)
}
`

// TestStdBenchJudgeMatchesReference runs stdBenchJudgeProgram's properties
// with --seed 1, as ergo test --seed 1 does; a refuted case prints its
// arguments and its replay line (runtime.md, Tests 5). It reads only the
// properties' lines: the program's bench laws run too, and are
// TestStdBenchLawsRun's, so a refuted law is not this test's failure.
//
// It builds Go because a Planned run of six series costs about 300k
// check-time steps a case, far past check time's budget; only the built
// program runs them.
//
// Mutants (internal/sketch/stdlib/testdata/mutants.json): the bn2 rows
// whose killer it is, and jdp-holm-first-two.
func TestStdBenchJudgeMatchesReference(t *testing.T) {
	needsBuild(t)
	stdout, stderr, _ := runExeArgs(t, tableExe(t, "stdbenchjudge"), "--seed", "1")
	for _, m := range regexp.MustCompile(`(?m)^test (\w+)`).FindAllStringSubmatch(stdBenchJudgeProgram, -1) {
		if !regexp.MustCompile(`(?m)^ok ` + m[1] + `( \(\d+ cases\))?$`).MatchString(stdout) {
			t.Errorf("std bench's referee differs from its reference: no ok line for %s:\n%s\n%s", m[1], stdout, firstLines(stderr, 20))
		}
	}
}
