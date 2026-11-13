// Package patternbench measures a refinement pattern's lowered matcher
// (types.md, Refinements 11) against Go's regexp.MustCompile on the same
// inputs: allocations and, on Linux, user-space instructions per match,
// counted by the CPU (perf_event_open). The receipt is
// docs/receipts/fkre-patterns.md. Run from the repository root:
//
//	go run ./docs/research/pattern-bench/gen
//	go test -run '^$' -bench . -benchmem ./docs/research/pattern-bench
package patternbench

import (
	"regexp"
	"testing"
)

// inputs are each pattern's values: matching, failing late and failing
// early.
var inputs = map[string][]string{
	"Email": {"ada@example.com", "release-notes+ergo@lists.example.org", "no-at-sign.example.com", "a b@c.d", "x@y"},
	"Label": {"example", "my-domain-42", "-leading", "trailing-", "a", "UPPER"},
	"Date":  {"2026-11-13", "1999-01-01", "2026-1-13", "20261113", "2026-11-1x"},
}

var matchers = map[string]func(string) bool{
	"Email": matchEmail,
	"Label": matchLabel,
	"Date":  matchDate,
}

func BenchmarkMatch(b *testing.B) {
	for _, name := range []string{"Email", "Label", "Date"} {
		re := regexp.MustCompile(`^(?:` + sources[name] + `)$`)
		ergo := matchers[name]
		for _, s := range inputs[name] {
			if ergo(s) != re.MatchString(s) {
				b.Fatalf("%s %q: the matcher says %v, regexp %v", name, s, ergo(s), re.MatchString(s))
			}
		}
		b.Run(name+"/ergo", func(b *testing.B) { run(b, inputs[name], ergo) })
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
