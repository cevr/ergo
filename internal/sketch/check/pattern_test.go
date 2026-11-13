package check

import (
	"fmt"
	"math/rand"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestPatternMatchesGoRegexp is the seeded property of types.md,
// Refinements 11 against Go's regexp, the reference model: for generated
// patterns of the subset, the compiled automaton matches exactly the
// strings that regexp.MustCompile(`^(?:p)$`) matches, over strings of the
// pattern's own runes, a newline, a rune outside ASCII and an invalid
// byte; and every value Generate draws for a length the automaton reaches
// is that long and matches. It kills the mutants of pattern.go in
// testdata/mutants.json (the escapes' classes, negation, ranges, repeat
// bounds, the subset construction's split, minimisation, Reach and the
// walk). ERGO_SEED=<n> replays one seed; a failure prints the seed, the
// pattern and the string.
func TestPatternMatchesGoRegexp(t *testing.T) {
	seeds := make([]int64, 400)
	if testing.Short() {
		seeds = seeds[:80]
	}
	for i := range seeds {
		seeds[i] = int64(i + 1)
	}
	if s := os.Getenv("ERGO_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_SEED: %v", err)
		}
		seeds = []int64{n}
	}
	for _, seed := range seeds {
		rng := rand.New(rand.NewSource(seed))
		src := genPattern(rng, 3)
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d (replay: ERGO_SEED=%d go test -run TestPatternMatchesGoRegexp ./internal/sketch/check): pattern %q: %s", seed, seed, src, fmt.Sprintf(format, args...))
		}
		p, err := CompilePattern(src)
		if err != nil {
			fail("refused: %v", err)
		}
		re := regexp.MustCompile(`^(?:` + src + `)$`)
		alphabet := patternAlphabet(src)
		for k := 0; k < 300; k++ {
			s := drawString(rng, alphabet)
			if got, want := p.Match(s), re.MatchString(s); got != want {
				fail("%q: the automaton says %v, Go's regexp %v", s, got, want)
			}
		}
		reach := p.Reach(12)
		for n := 0; n <= 12; n++ {
			s, ok := p.Generate(n, rng.Intn)
			if ok != reach[n][0] {
				fail("length %d: Generate says %v, Reach %v", n, ok, reach[n][0])
			}
			if ok && (len(s) != n || !re.MatchString(s)) {
				fail("length %d: Generate drew %q, which Go's regexp says is %v", n, s, re.MatchString(s))
			}
		}
		// A string reaches an accepting state in exactly its length.
		for k := 0; k < 100; k++ {
			s := drawString(rng, alphabet)
			if n := len(s); n <= 12 && re.MatchString(s) && !reach[n][0] {
				fail("%q matches but Reach(%d) has no accepting walk from the start", s, n)
			}
		}
	}
}

// genPattern is a pattern of the subset, of depth d.
func genPattern(rng *rand.Rand, d int) string {
	atoms := []string{"a", "b", "c", "@", ".", `\.`, `\d`, `\D`, `\w`, `\W`, `\s`, `\S`, `\t`, "é", "[a-c]", "[^a@]", "[^\\s@]", "[ab-]", "[-.]", "[\\d.]", "[^\\d]", "[]a]", "[^]]", `\-`, `\{`, "()"}
	if d <= 0 || rng.Intn(3) == 0 {
		return atoms[rng.Intn(len(atoms))]
	}
	reps := []string{"*", "+", "?", "{2}", "{1,3}", "{0,}", "{2,}", "*?", "{0,2}?"}
	switch rng.Intn(5) {
	case 0:
		return "(" + genPattern(rng, d-1) + ")" + reps[rng.Intn(len(reps))]
	case 1:
		return "(?:" + genPattern(rng, d-1) + "|" + genPattern(rng, d-1) + ")"
	case 2:
		return genPattern(rng, d-1) + "|" + genPattern(rng, d-1)
	case 3:
		return atoms[rng.Intn(len(atoms))] + reps[rng.Intn(len(reps))]
	}
	return genPattern(rng, d-1) + genPattern(rng, d-1)
}

// patternAlphabet is the runes a test string draws from: the pattern's
// own, and runes on each side of the classes it may use.
func patternAlphabet(src string) []string {
	var out []string
	seen := map[rune]bool{}
	for _, r := range src + "az09_ \t\v\n.@-]Zé€" {
		if !seen[r] {
			seen[r] = true
			out = append(out, string(r))
		}
	}
	return append(out, "\xff", "\U0001F600")
}

func drawString(rng *rand.Rand, alphabet []string) string {
	var b strings.Builder
	for n := rng.Intn(8); n > 0; n-- {
		b.WriteString(alphabet[rng.Intn(len(alphabet))])
	}
	return b.String()
}
