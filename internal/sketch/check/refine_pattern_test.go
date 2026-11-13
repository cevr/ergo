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

// TestRefinementPatternConstants is the seeded property of types.md,
// Refinements 6 and 11 at the where: for a generated pattern of the subset
// and generated length bounds, a string constant enters the refined type
// exactly when Go's regexp `^(?:p)$` matches it and its length is in the
// bounds (the reference model), with no other diagnostic; a where that
// holds an accepted constant is never the empty S108; and the type's zero
// value is "" exactly when "" holds. It kills the mutants of refine.go,
// refine_pattern.go and typeops.go in testdata/mutants.json (the constant
// that skips the pattern, the empty set that ignores it or reads lengths
// as runes, the zero value). ERGO_SEED=<n> replays one seed; a failure
// prints the seed and the source.
func TestRefinementPatternConstants(t *testing.T) {
	seeds := make([]int64, 300)
	if testing.Short() {
		seeds = seeds[:60]
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
		pat := genPattern(rng, 3)
		re := regexp.MustCompile(`^(?:` + pat + `)$`)
		lo, hi := rng.Intn(4), 2+rng.Intn(8)
		// A constant: a string the pattern's own runes make, or one Go's
		// regexp matches when a few draws find it.
		alphabet := patternAlphabet(pat)
		probe := drawString(rng, alphabet)
		for k := 0; k < 40 && !re.MatchString(probe); k++ {
			probe = drawString(rng, alphabet)
		}
		zero := rng.Intn(2) == 0
		body := strconv.Quote(probe)
		if zero {
			body = "let z T\n\tz"
		}
		src := fmt.Sprintf("type T string where matches(it, `%s`) && %d <= len(it) && len(it) <= %d\n\nfunc f() T {\n\t%s\n}\n", pat, lo, hi, body)
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d (replay: ERGO_SEED=%d go test -run TestRefinementPatternConstants ./internal/sketch/check): %s\n%s", seed, seed, fmt.Sprintf(format, args...), src)
		}
		if strings.Contains(pat, "`") {
			fail("a generated pattern holds a backquote")
		}
		value := probe
		if zero {
			value = ""
		}
		want := re.MatchString(value) && lo <= len(value) && len(value) <= hi
		_, ds := CheckSource(src)
		empty := len(ds) == 1 && strings.Contains(ds[0].Message, "satisfies this where")
		switch {
		case want && len(ds) != 0:
			fail("%q holds the where: want no diagnostic, got %s", value, describeDiags(ds))
		case !want && len(ds) == 0:
			fail("%q fails the where: want S108, got none", value)
		case !want && !empty && (len(ds) != 1 || ds[0].Code != CodeType):
			fail("%q fails the where: want one S108, got %s", value, describeDiags(ds))
		}
	}
}

// TestPatternRefusedAtItsOffset is the diagnostic rows of types.md,
// Refinements 11: a pattern outside the subset is S108 at the offending
// rune of the literal, through a raw and an interpreted literal's escapes,
// and a pattern under || or of a base that is no string is S108 at the
// call. Each row is the smallest repro of a mutant in testdata/mutants.json
// that the properties do not reach (the literal's offset map, the
// refusal of \b, the conjunct rule, the string base).
func TestPatternRefusedAtItsOffset(t *testing.T) {
	for _, row := range []struct {
		src     string
		col     int
		message string
	}{
		{"type T string where matches(it, `ab\\bc`)\n", 36, "the escape \\b is outside the pattern subset"},
		{"type T string where matches(it, \"\\t\\\\bc\")\n", 36, "the escape \\b is outside the pattern subset"},
		{"type T string where matches(it, \"é(?i)x\")\n", 36, "a pattern takes no flags"},
		{"type T string where len(it) < 3 || matches(it, `a`)\n", 36, "a pattern stands as a conjunct of the where"},
		{"type T int where matches(it, `a`)\n", 18, "a pattern refines a string"},
	} {
		_, ds := CheckSource(row.src)
		if len(ds) != 1 || ds[0].Code != CodeType || ds[0].Line != 1 || ds[0].Column != row.col || !strings.Contains(ds[0].Message, row.message) {
			t.Errorf("%s: want S108 at 1:%d %q, got %s", row.src, row.col, row.message, describeDiags(ds))
		}
	}
}
