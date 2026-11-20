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
// familyPlanOf is planOf's series, three to six of them, on Planned.
func familyPlanOf(seed uint64) Plan {
	let mut all = []Series{}
	for i in 0..(3 + draw(seed, 20, 4)) {
		let claims = []Claim{Change{Bound: 0.02}, Change{Bound: 0.5}, NoWorseThan{Margin: 0.0}, NoWorseThan{Margin: 0.1}}
		all = all.Append(Series{Name: "s", Kind: kindOf(draw(seed, 21+i, 3)), Claim: claims[draw(seed, 30+i, 4)]})
	}
	Plan{Primary: all[0], Secondaries: all[1:], Design: Planned}
}

// machineAgrees is law MachineIsJudge's body: the Referee machine, fed
// the blocks Next asks for, ends with Judge's result and rejects a signal
// for any other block.
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

test BenchJudgeMatchesReference(seed uint64, size int, pick int) {
	let p = planOf(seed, pick)
	let m = scriptOf(seed, size, p)
	let got = with context { Meter: m } { Judge(p) }
	assert got == reference(p, m)
}

test BenchPlannedFamilyMatchesReference(seed uint64, size int) {
	let p = familyPlanOf(seed)
	let m = scriptOf(seed, size, p)
	let got = with context { Meter: m } { Judge(p) }
	assert got == reference(p, m)
}

test BenchMachineIsJudgeOnEveryPlan(seed uint64, size int, pick int) {
	let p = planOf(seed, pick)
	assert machineAgrees(p, scriptOf(seed, size, p))
}

test BenchPlannedFamilyMachineIsJudge(seed uint64, size int) {
	let p = familyPlanOf(seed)
	assert machineAgrees(p, scriptOf(seed, size, p))
}

test BenchSecondariesAreOneFamily(seed uint64, size int) {
	assert oneFamily(seed, within(size, 1, 5))
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
		if !regexp.MustCompile(`(?m)^ok ` + m[1] + ` \(\d+ cases\)$`).MatchString(stdout) {
			t.Errorf("std bench's referee differs from its reference: no ok line for %s:\n%s\n%s", m[1], stdout, firstLines(stderr, 20))
		}
	}
}
