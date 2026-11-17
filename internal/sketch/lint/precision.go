package lint

import "fmt"

// Published precision (LINT-HYGIENE §[R2] 1, nits' method). Every rule
// carries a measured precision, and a finding prints it (p0.95). The
// number is the lower of two: the precision on the rule's labelled corpus
// (in sample, written with the rule) and the precision on a held-out read
// sample (findings on code never used while writing the rule, each read
// against the source, with the seed and the commit, every miss kept). It
// is capped at 0.95: a rule with no observed false finding is
// under-measured, not perfect. A rule is on by default only at Bar or
// above; below it, or with no read sample, it is opt-in. A fix of a rule
// re-reads its sample; the findings the fix removed stay on record in the
// earlier reading, and the rule's next sample must be held out from the
// fix.

// Bar is the precision, in hundredths, a rule needs to be on by default
// (in the base preset).
const Bar = 80

// Cap is the highest precision, in hundredths, a rule can publish.
const Cap = 95

// Precision is a published precision in hundredths. Known is false for a
// rule with no measure (a pack rule, or a rule with no read sample), and
// for the runner's own codes.
type Precision struct {
	Hundredths int
	Known      bool
}

// String is the form a finding prints: p0.95, or "" with no measure.
func (p Precision) String() string {
	if !p.Known {
		return ""
	}
	return fmt.Sprintf("p%d.%02d", p.Hundredths/100, p.Hundredths%100)
}

// Value is the precision as a fraction, for the JSON report; nil with no
// measure.
func (p Precision) Value() *float64 {
	if !p.Known {
		return nil
	}
	v := float64(p.Hundredths) / 100
	return &v
}

// Miss is one class of false finding a read sample found: its class (the
// class column of the sample's reading), what it was, and how many.
type Miss struct {
	Class string
	What  string
	Count int
}

// Measure is one row of the precision table: a rule, the language of the
// code it decides on, and its decider ("exact" for a rule on checker
// facts). CorpusTrue of CorpusRead is the labelled corpus; both zero means
// the rule's seeded reference property is its corpus, which agrees with
// the rule on every generated case by construction (precision 1). True of
// Read is the held-out read sample, drawn by Seed ("all" when every
// finding of the population was read) at Commit, from Source; Reading is
// the sample's file under testdata/precision.
type Measure struct {
	Rule       string
	Language   string
	Decider    string
	CorpusTrue int
	CorpusRead int
	True       int
	Read       int
	Seed       string
	Commit     string
	Source     string
	Reading    string
	Misses     []Miss
}

// Precision is the row's published number: the lower of the corpus and
// the read sample, rounded down to hundredths, capped at Cap. A row with
// no read sample has none.
func (m Measure) Precision() Precision {
	if m.Read == 0 {
		return Precision{}
	}
	p := m.True * 100 / m.Read
	if m.CorpusRead > 0 {
		p = min(p, m.CorpusTrue*100/m.CorpusRead)
	}
	return Precision{Hundredths: min(p, Cap), Known: true}
}

// firstVersions is the held-out source of the 2026-11-17 sample: the first
// committed version of each examples/ program, linted at the lane's
// compiler (64d838f6d) with every built-in preset; 77 of the 100 programs
// still check. Every finding was read.
const firstVersions = "first committed version of each examples/ program (77 of 100 check)"

// reading1117 is the 2026-11-17 sample re-read after LHP2 fixed the two
// families of false findings it found (silent-catch missed Unreachable,
// assert and an outer write as traces; the complexity metrics counted
// each arm of a match over a union): the same 103 findings at 64d838f6d,
// linted again at the fixed rules (1f3454ed9). A finding still reported
// keeps its first label; none was new; the 29 no longer reported are
// read-2026-11-17.tsv's rows the fix removed (5 of them true). The fix
// was written against this sample, so it is no longer held out for
// silent-catch and the complexity rules: their next sample must be.
const reading1117 = "read-2026-11-17-lhp2.tsv"

// measure1117 is a row of the 2026-11-17 sample.
func measure1117(rule string, truth, read int, misses ...Miss) Measure {
	return Measure{Rule: rule, Language: "ergo", Decider: "exact", True: truth, Read: read,
		Seed: "all", Commit: "1f3454ed9", Source: firstVersions, Reading: reading1117, Misses: misses}
}

var (
	missOneMatch = func(n int) Miss {
		return Miss{"one match", "one match over a union: a machine Step, a classifier, a transition or action table, a VM's dispatch", n}
	}
	missHotPath = func(n int) Miss { return Miss{"hot path", "a hand-tuned hot path whose shape is the speed", n} }
)

// precisionTable is every measured built-in rule's row. A rule of the base
// preset must have a row at Bar or above (TestDefaultRulesMeetTheBar), and
// each row's counts are its reading's (TestPrecisionTableIsItsReading).
// Rules with no finding on the held-out source have no row, so no number:
// concept-file-names, deprecated, foreign-error-public, foreign-where-std,
// race-for-timeout, with-at-entry.
var precisionTable = []Measure{
	measure1117("banned-words", 2, 6, Miss{"domain", "Shape is the domain's geometric primitive, not a filler word", 4}),
	measure1117("cognitive-complexity", 9, 14, missHotPath(3), Miss{"just over", "one job just over the cap", 2}),
	measure1117("cyclomatic-complexity", 1, 4, missHotPath(2), Miss{"one match", "a match on byte literals: one classifier", 1}),
	measure1117("halstead-difficulty", 0, 1, missOneMatch(1)),
	measure1117("known-value-widening", 5, 5),
	measure1117("silent-catch", 7, 43,
		Miss{"designed", "the fallback is the designed meaning: retry on Stale, a refused command yields no events, U+FFFD, a default answer", 22},
		Miss{"generator", "a law's or a test's generator skips an input the code refuses", 12},
		Miss{"observation", "the test reads the sentinel the arm yields", 2}),
	measure1117("unbounded-collect", 0, 1, Miss{"rule miss", "the Seq's source is ended before the loop", 1}),
}

// PrecisionOf is the published precision of a built-in rule in Ergo code,
// by its exact decider; unknown for any other name.
func PrecisionOf(rule string) Precision {
	if m, ok := MeasureOf(rule); ok {
		return m.Precision()
	}
	return Precision{}
}

// MeasureOf is the precision table's row of a built-in rule.
func MeasureOf(rule string) (Measure, bool) {
	for _, m := range precisionTable {
		if m.Rule == rule && m.Language == "ergo" && m.Decider == "exact" {
			return m, true
		}
	}
	return Measure{}, false
}

// Measures are the precision table's rows.
func Measures() []Measure { return append([]Measure(nil), precisionTable...) }

// GateFails reports whether --gate p fails the run: a finding in scope
// comes from a rule whose published precision is at least p (a fraction).
// A finding with no published precision (an unmeasured rule, a pack rule,
// the runner's L000 and L001) is outside every gate; the severity policy
// covers the runner's codes.
func GateFails(findings []Finding, p float64) bool {
	for _, f := range findings {
		if f.Precision.Known && float64(f.Precision.Hundredths)/100 >= p {
			return true
		}
	}
	return false
}
