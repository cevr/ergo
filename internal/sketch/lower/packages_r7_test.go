package lower

import (
	"fmt"
	"slices"
	"strings"
)

// Round 7 (196b): a set of the check result (a row's elements, a row
// value's variants, a catch's subjects) is in intern order, which follows
// the order in which the checker met the files. Round 8 makes that order
// canonical (check/canon.go), so output and refusals do not follow how a
// caller listed the files. These tests stay as regressions.

// setProgram is a program of n capability files and one main file. File k
// declares the capability Ck with a provider and a context, the error
// type Ek, and functions whose rows name only them. main.ergo, listed
// last, has rows of every Ck and Ek, a catch whose subjects are every Ek,
// a row value (case err) over the rest, and its match. Which single-member
// row the checker interns first follows the order of the files.
func setProgram(n int, refuse bool) []pkgSource {
	var files []pkgSource
	var caps, errs, ctxs, uses, tries, arms []string
	for k := 0; k < n; k++ {
		files = append(files, pkgSource{fmt.Sprintf("/set/f%d.ergo", k), fmt.Sprintf(`type C%[1]d { Get() int }
type P%[1]d {}
func Get(x P%[1]d) int { %[2]d }
let X%[1]d = context { C%[1]d: P%[1]d }
func use%[1]d() int needs C%[1]d { C%[1]d.Get() }
type E%[1]d { N int }
func fail%[1]d(n int) int fails E%[1]d {
	if n == %[1]d {
		return E%[1]d{N: %[1]d}
	}
	n
}
`, k, k+1)})
		caps = append(caps, fmt.Sprintf("C%d", k))
		errs = append(errs, fmt.Sprintf("E%d", k))
		ctxs = append(ctxs, fmt.Sprintf("X%d", k))
		uses = append(uses, fmt.Sprintf("use%d()", k))
		tries = append(tries, fmt.Sprintf("\tlet _ = try fail%d(n)\n", k))
		if k > 0 {
			arms = append(arms, fmt.Sprintf("\t\tcase E%d:\n\t\t\t\"e%d\"\n", k, k))
		}
	}
	main := fmt.Sprintf(`let W = context { %s }
func all() int needs %s { %s }
func each(n int) string fails %s {
%s	"ok"
}
func name(n int) string {
	let s = each(n) catch {
	case E0 as e:
		"e0:\(e.N)"
	case err:
		match err {
%s		}
	}
	s
}
`, strings.Join(ctxs, ", "), strings.Join(caps, " | "), strings.Join(uses, " + "), strings.Join(errs, " | "), strings.Join(tries, ""), strings.Join(arms, ""))
	var shown []string
	for k := 0; k <= n; k++ {
		shown = append(shown, fmt.Sprintf("\\(name(%d))", k))
	}
	if refuse {
		main += fmt.Sprintf("func main() needs %s { let _ = all() }\n", strings.Join(caps, " | "))
	} else {
		main += "func main() needs Console {\n\tlet s = with W { all() }\n\tConsole.Println(\"\\(s) " + strings.Join(shown, " ") + "\")\n}\n"
	}
	return append(files, pkgSource{"/set/main.ergo", main})
}

// permutations is every order of 0..n-1.
func permutations(n int) [][]int {
	if n == 0 {
		return [][]int{nil}
	}
	var out [][]int
	for _, p := range permutations(n - 1) {
		for i := 0; i <= len(p); i++ {
			q := append(append(slices.Clone(p[:i]), n-1), p[i:]...)
			out = append(out, q)
		}
	}
	return out
}
