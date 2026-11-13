package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"
)

// The seeded property of a refinement's lowered matcher (types.md,
// Refinements 11; pattern.go). Each seed's patterns of the subset refine
// T0, T1, …; one program reads lines "index\x1fvalue" and prints 1 when
// ParseTi takes the value, else 0. Go's regexp `^(?:p)$` is the model.
// A failing seed prints the Ergo source, the line, both answers and the
// replay command.

var patternSeed = flag.Uint64("pattern.seed", 0, "run TestPatternMatchersAgree on this seed alone")

// TestPatternMatchersAgree is the seeded property (see above). It kills
// the mutants of pattern.go in testdata/mutants.json (a span's test, the
// accepting states' return, the straight-line switch's targets).
//
// It builds Go because the question is the run of the lowered matcher:
// the check package's property covers the automaton, and only a run of
// the Go text this lowering writes says that text matches as it does.
func TestPatternMatchersAgree(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4}
	if *patternSeed != 0 {
		seeds = []uint64{*patternSeed}
	}
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0))
		var decls, cases strings.Builder
		var patterns []*regexp.Regexp
		var input, want []string
		for i := range 12 {
			p := patternOf(r, 3)
			patterns = append(patterns, regexp.MustCompile(`^(?:`+p+`)$`))
			fmt.Fprintf(&decls, "type T%d string where matches(it, `%s`)\n", i, p)
			fmt.Fprintf(&decls, "func p%d(s string) string {\n\tlet v = ParseT%d(s) catch {\n\tcase Unsatisfied:\n\t\treturn \"0\"\n\t}\n\tif string(v) == s { \"1\" } else { \"?\" }\n}\n", i, i)
			fmt.Fprintf(&cases, "if i == %d { p%d(s) } else ", i, i)
			alphabet := patternRunes(p)
			for range 40 {
				var b strings.Builder
				for n := r.IntN(7); n > 0; n-- {
					b.WriteString(alphabet[r.IntN(len(alphabet))])
				}
				s := b.String()
				input = append(input, fmt.Sprintf("%d\x1f%s", i, s))
				want = append(want, map[bool]string{true: "1", false: "0"}[patterns[i].MatchString(s)])
			}
		}
		src := "import io\nimport strconv\nimport strings\n\n" + decls.String() +
			"\nfunc parsed(i int, s string) string {\n\t" + cases.String() + "{ \"?\" }\n}\n" +
			"\nfunc main() needs Ambient {\n\twith context { io.Stdout: io.NewHost(), io.Stdin: io.NewStdin() } {\n\t\tfor line in io.Stdin.Lines() {\n\t\t\tlet fields = strings.Split(line, \"\\x1f\")\n\t\t\tlet i = strconv.Atoi(fields[0]) catch { case _: Unreachable(\"test input index\") }\n\t\t\tio.Stdout.Println(parsed(i, fields[1]))\n\t\t}\n\t}\n}\n"
		name := fmt.Sprintf("pat%d", seed)
		exes := buildAll(t, []*lowered{mustLower(t, name, src)}, true)
		if exes == nil {
			t.Fatalf("seed %d: the program does not build\n%s", seed, src)
		}
		out, stderr, code := runExeInput(t, exes[name], strings.Join(input, "\n")+"\n")
		got := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
		if code != 0 || len(got) != len(want) {
			t.Fatalf("seed %d: exit %d, %d answers for %d lines\n%s\n%s", seed, code, len(got), len(want), stderr, src)
		}
		for k := range want {
			if got[k] != want[k] {
				t.Errorf("seed %d (replay: go test -run '^TestPatternMatchersAgree$' ./internal/sketch/lower -args -pattern.seed=%d): line %q: the matcher says %s, Go's regexp %s\n%s", seed, seed, input[k], got[k], want[k], src)
				break
			}
		}
	}
}

// patternOf is a pattern of the subset, of depth d.
func patternOf(r *rand.Rand, d int) string {
	atoms := []string{"a", "b", "@", ".", `\.`, `\d`, `\D`, `\w`, `\W`, `\s`, `\S`, "é", "[a-c]", "[^a@]", `[^\s@]`, "[ab-]", `[\d.]`, "[]a]", "[^]]", `\{`}
	if d <= 0 || r.IntN(3) == 0 {
		return atoms[r.IntN(len(atoms))]
	}
	reps := []string{"*", "+", "?", "{2}", "{1,3}", "{2,}", "*?"}
	switch r.IntN(5) {
	case 0:
		return "(" + patternOf(r, d-1) + ")" + reps[r.IntN(len(reps))]
	case 1:
		return "(?:" + patternOf(r, d-1) + "|" + patternOf(r, d-1) + ")"
	case 2:
		return patternOf(r, d-1) + "|" + patternOf(r, d-1)
	case 3:
		return atoms[r.IntN(len(atoms))] + reps[r.IntN(len(reps))]
	}
	return patternOf(r, d-1) + patternOf(r, d-1)
}

// patternRunes are the runes a value draws from: the pattern's own, and
// runes on each side of its classes, with a tab, a rune past ASCII and
// one past the BMP. A value holds no newline: it is one line.
func patternRunes(p string) []string {
	var out []string
	seen := map[rune]bool{}
	for _, c := range p + "az09_ \t.@-]Zé€😀" {
		if !seen[c] {
			seen[c] = true
			out = append(out, string(c))
		}
	}
	return out
}
