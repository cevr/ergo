// Package patternbench measures a refinement pattern's lowered matcher
// (types.md, Refinements 11), in each of its forms (straight-line and
// table), against Go's regexp.MustCompile on the same inputs: allocations
// and, on Linux, user-space instructions per match, counted by the CPU
// (perf_event_open). Command size reports each form's bytes in the binary.
// The receipt is docs/receipts/fkre-patterns.md. Run from the repository
// root:
//
//	go run ./docs/research/pattern-bench/gen
//	go test -run '^$' -bench . -benchmem ./docs/research/pattern-bench
//	go run ./docs/research/pattern-bench/size
package patternbench

import (
	"regexp"
	"testing"
)

// names are the patterns, in the order of their automaton's size.
var names = []string{"Email", "Date", "Word", "Word20", "Ident", "UUID", "SHA1", "Label"}

// inputs are each pattern's values: matching, failing late and failing
// early.
var inputs = map[string][]string{
	"Email":  {"ada@example.com", "release-notes+ergo@lists.example.org", "no-at-sign.example.com", "a b@c.d", "x@y"},
	"Date":   {"2026-11-13", "1999-01-01", "2026-1-13", "20261113", "2026-11-1x"},
	"Word":   {"refinement", "a", "thirteenletter", "Upper", "two words"},
	"Word20": {"refinement", "a", "twentyoneletterswordx", "Upper", "twentyletterwordabcd"},
	"Ident":  {"matchLabel", "_x", "9lives", "aVeryLongIdentifierName", "with-dash"},
	"UUID":   {"123e4567-e89b-12d3-a456-426614174000", "123e4567-e89b-12d3-a456-42661417400", "123E4567-e89b-12d3-a456-426614174000", "not-a-uuid"},
	"SHA1":   {"da39a3ee5e6b4b0d3255bfef95601890afd80709", "da39a3ee5e6b4b0d3255bfef95601890afd8070", "DA39A3EE5E6B4B0D3255BFEF95601890AFD80709", "zz"},
	"Label":  {"example", "my-domain-42", "-leading", "trailing-", "a", "UPPER", "abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz0"},
}

var forms = map[string][2]func(string) bool{
	"Email":  {matchEmail, matchEmailTable},
	"Date":   {matchDate, matchDateTable},
	"Word":   {matchWord, matchWordTable},
	"Word20": {matchWord20, matchWord20Table},
	"Ident":  {matchIdent, matchIdentTable},
	"UUID":   {matchUUID, matchUUIDTable},
	"SHA1":   {matchSHA1, matchSHA1Table},
	"Label":  {matchLabel, matchLabelTable},
}

// TestFormsAgreeWithRegexp: each pattern's two lowered forms answer as
// Go's regexp does on every input, so the bench compares matchers that
// agree. It is the package's test leg (a leg that runs no test is red).
func TestFormsAgreeWithRegexp(t *testing.T) {
	for _, name := range names {
		re := regexp.MustCompile(`^(?:` + sources[name] + `)$`)
		f := forms[name]
		for _, s := range inputs[name] {
			if f[0](s) != re.MatchString(s) || f[1](s) != re.MatchString(s) {
				t.Errorf("%s %q: the forms say %v and %v, regexp %v", name, s, f[0](s), f[1](s), re.MatchString(s))
			}
		}
	}
}

func BenchmarkMatch(b *testing.B) {
	for _, name := range names {
		re := regexp.MustCompile(`^(?:` + sources[name] + `)$`)
		f := forms[name]
		b.Run(name+"/switch", func(b *testing.B) { run(b, inputs[name], f[0]) })
		b.Run(name+"/table", func(b *testing.B) { run(b, inputs[name], f[1]) })
		b.Run(name+"/regexp", func(b *testing.B) { run(b, inputs[name], re.MatchString) })
		// MustCompile when the program starts: the cost the lowering removes.
		b.Run(name+"/compile", func(b *testing.B) {
			b.ReportAllocs()
			c := startCount(b)
			for i := 0; i < b.N; i++ {
				_ = regexp.MustCompile(`^(?:` + sources[name] + `)$`)
			}
			c.report(b, 1)
		})
	}
}

// run matches every input b.N times; a op is one match.
func run(b *testing.B, in []string, match func(string) bool) {
	b.ReportAllocs()
	hits := 0
	c := startCount(b)
	for i := 0; i < b.N; i++ {
		for _, s := range in {
			if match(s) {
				hits++
			}
		}
	}
	c.report(b, len(in))
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(in)), "ns/match")
	if hits < 0 {
		b.Fatal(hits)
	}
}
