package lint_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lint"
)

// TestDefaultRulesMeetTheBar is the residue rule of the precision table
// (LINT-HYGIENE §[R2] 1): a rule of the base preset, on by default, has a
// published precision of at least 0.80. A rule with no row or a row under
// the bar is opt-in. Mutant: lhp-justifier-back-in-base.
func TestDefaultRulesMeetTheBar(t *testing.T) {
	base := lint.Builtin().BasePreset()
	for name := range base.Rules {
		p := lint.PrecisionOf(name)
		if !p.Known || p.Hundredths < lint.Bar {
			t.Errorf("rule %s is on by default in %s with precision %q; the bar is 0.%02d: move it to an opt-in preset", name, base.Name, p, lint.Bar)
		}
	}
}

// TestPrecisionTableIsItsReading holds each row of the precision table to
// the read sample it cites: its true and read counts and its misses by
// class are the reading's, and every rule the reading names has a row. The
// reading is the evidence; the table is what the linter prints. Mutant:
// lhp-table-count-edited.
func TestPrecisionTableIsItsReading(t *testing.T) {
	type tally struct {
		truth, read int
		misses      map[string]int
	}
	readings := map[string]map[string]*tally{}
	for _, m := range lint.Measures() {
		if readings[m.Reading] != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("testdata", "precision", m.Reading))
		if err != nil {
			t.Fatal(err)
		}
		byRule := map[string]*tally{}
		for i, line := range strings.Split(strings.TrimSpace(string(data)), "\n")[1:] {
			f := strings.Split(line, "\t")
			if len(f) != 8 || (f[5] != "T" && f[5] != "F") {
				t.Fatalf("%s row %d: want rule, example, commit, file, line, label T or F, class, what: %q", m.Reading, i+2, line)
			}
			c := byRule[f[0]]
			if c == nil {
				c = &tally{misses: map[string]int{}}
				byRule[f[0]] = c
			}
			c.read++
			if f[5] == "T" {
				c.truth++
			} else {
				c.misses[f[6]]++
			}
		}
		readings[m.Reading] = byRule
	}
	for reading, byRule := range readings {
		for rule, c := range byRule {
			m, ok := lint.MeasureOf(rule)
			if !ok || m.Reading != reading {
				t.Errorf("%s reads rule %s, which has no row citing it", reading, rule)
				continue
			}
			misses := map[string]int{}
			for _, miss := range m.Misses {
				misses[miss.Class] += miss.Count
			}
			if m.True != c.truth || m.Read != c.read || fmt.Sprint(misses) != fmt.Sprint(c.misses) {
				t.Errorf("%s: the table says %d of %d true, misses %v; %s says %d of %d, misses %v", rule, m.True, m.Read, misses, reading, c.truth, c.read, c.misses)
			}
		}
	}
}

// TestPublishedPrecisionIsTheLowerCapped is a seeded property of
// Measure.Precision against a model in exact integers: the published
// number is the largest h of hundredths with h <= 95, h/100 <= the read
// sample's precision and h/100 <= the corpus's (when it has one); a row
// with no read sample publishes none. The printed form is p0.NN. Mutants:
// lhp-no-cap, lhp-corpus-ignored, lhp-rounds-up.
func TestPublishedPrecisionIsTheLowerCapped(t *testing.T) {
	r := rand.New(rand.NewPCG(11, 17))
	for i := 0; i < 2000; i++ {
		read := r.IntN(60)
		corpusRead := r.IntN(3) * r.IntN(40)
		m := lint.Measure{Read: read, True: r.IntN(read + 1), CorpusRead: corpusRead, CorpusTrue: r.IntN(corpusRead + 1)}
		want := -1
		if read > 0 {
			for h := 0; h <= 95; h++ {
				if h*m.Read <= 100*m.True && (m.CorpusRead == 0 || h*m.CorpusRead <= 100*m.CorpusTrue) {
					want = h
				}
			}
		}
		got := m.Precision()
		if (want < 0) != !got.Known || (got.Known && got.Hundredths != want) {
			t.Fatalf("%+v: got %+v, want %d hundredths (-1: none)", m, got, want)
		}
		if got.Known && got.String() != fmt.Sprintf("p0.%02d", want) {
			t.Fatalf("%+v prints %q", m, got.String())
		}
	}
}

// TestGateAgreesWithModel is a seeded property of --gate against a model:
// a run fails the gate P exactly when a finding in scope has a published
// precision of at least P. P is written as the command line takes it (a
// decimal of two or three places, parsed as a float) and the model
// compares in integers, so a precision equal to P fails the gate. A
// finding with no precision (an unmeasured or pack rule, L000, L001) never
// does. Mutants: lhp-gate-strict, lhp-gate-unknown-counts.
func TestGateAgreesWithModel(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 23))
	for i := 0; i < 3000; i++ {
		places := 2 + r.IntN(2)
		scale := 100
		if places == 3 {
			scale = 1000
		}
		k := r.IntN(scale + 1)
		text := strconv.FormatFloat(float64(k)/float64(scale), 'f', places, 64)
		p, err := strconv.ParseFloat(text, 64)
		if err != nil {
			t.Fatal(err)
		}
		var findings []lint.Finding
		want := false
		for j := r.IntN(5); j > 0; j-- {
			prec := lint.Precision{Hundredths: r.IntN(101), Known: r.IntN(3) > 0}
			if r.IntN(4) == 0 {
				prec.Hundredths = k * 100 / scale // on the gate's value
			}
			findings = append(findings, lint.Finding{Rule: "r", Precision: prec})
			if prec.Known && prec.Hundredths*scale >= k*100 {
				want = true
			}
		}
		if got := lint.GateFails(findings, p); got != want {
			t.Fatalf("--gate %s over %+v: fails %v, want %v", text, findings, got, want)
		}
	}
}
