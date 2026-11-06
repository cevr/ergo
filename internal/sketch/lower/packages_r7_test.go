package lower

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Round 7 (196b): a set of the check result (a row's elements, a row
// value's variants, a catch's subjects) is in intern order, which follows
// the order in which the checker met the files. Round 8 makes that order
// canonical (check/canon.go), so output and refusals do not follow how a
// caller listed the files. These tests stay as regressions.

// The round 6 verify's probe (R6 M1): main needs A | B, and the host
// provides only Console. Whichever of a.ergo and b.ergo the checker meets
// first, the refusal names the same capability.
func TestVerifyR6RowRefusalCanonical(t *testing.T) {
	files := []pkgSource{
		{"/v6/a.ergo", "type A { Read() int }\nfunc a() needs A { let _ = A.Read() }\n"},
		{"/v6/b.ergo", "type B { Read() int }\nfunc b() needs B { let _ = B.Read() }\n"},
		{"/v6/main.ergo", "func main() needs A | B { a(); b() }\n"},
	}
	_, first := Lower(checkPackage(t, "main", files, nil))
	files[0], files[1] = files[1], files[0]
	_, second := Lower(checkPackage(t, "main", files, nil))
	if first == nil || second == nil {
		t.Fatal("expected host-provider refusals")
	}
	if first.Error() != second.Error() {
		t.Fatalf("same program, different file listing changes the lowering refusal:\n%s\nversus\n%s", first, second)
	}
}

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

// setResult is everything the lowering returns for files: the emitted
// files of both builds, or the refusal text of each build.
func setResult(t *testing.T, files []pkgSource) map[string]string {
	t.Helper()
	u := checkPackage(t, "main", files, nil)
	out := map[string]string{}
	for _, tests := range []bool{false, true} {
		var fs []File
		var err error
		if tests {
			fs, err = LowerTests(u)
		} else {
			fs, err = Lower(u)
		}
		if err != nil {
			out[fmt.Sprintf("tests=%v/error", tests)] = err.Error()
			continue
		}
		for _, f := range fs {
			out[fmt.Sprintf("tests=%v/%s", tests, f.Path)] = string(f.Data)
		}
	}
	return out
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

// TestLoweringSetsAreCanonical: for every order of the capability files
// (main last), a fresh check and lowering of the set program emits the
// same files in both builds, and the refusing variant returns the same
// refusals; the checker's diagnostics of a catch that misses part of a
// row are the same text. The program builds and runs once.
func TestLoweringSetsAreCanonical(t *testing.T) {
	const n = 4
	for _, refuse := range []bool{false, true} {
		files := setProgram(n, refuse)
		var want map[string]string
		for i, order := range permutations(n) {
			var listed []pkgSource
			for _, k := range order {
				listed = append(listed, files[k])
			}
			listed = append(listed, files[n])
			got := setResult(t, listed)
			if refuse != (got["tests=false/error"] != "") {
				t.Fatalf("refuse=%v: lowering result %v", refuse, got["tests=false/error"])
			}
			if i == 0 {
				want = got
				continue
			}
			sameEmission(t, fmt.Sprintf("refuse=%v order %v", refuse, order), want, got)
		}
	}
	u := checkPackage(t, "main", setProgram(n, false), nil)
	buildAndRun(t, []*lowered{mustLowerUnit(t, "sets", u, false)}, map[string]string{"sets": "10 e0:0 e1 e2 e3 ok\n"})
}

// TestCheckSetDiagnosticsAreCanonical: a catch that misses part of a row
// reports the rest of the row as the same text for every order of the
// files that declare its error types.
func TestCheckSetDiagnosticsAreCanonical(t *testing.T) {
	const n = 4
	files := setProgram(n, false)
	files[n].text += "func miss(n int) string { each(n) catch { case E2: \"2\" } }\n"
	want := ""
	for i, order := range permutations(n) {
		var parsed []*syntax.File
		for _, k := range append(order, n) {
			f, err := syntax.Parse(files[k].text)
			if err != nil {
				t.Fatal(err)
			}
			parsed = append(parsed, f)
		}
		_, ds := check.Check("main", parsed, nil)
		var texts []string
		for _, d := range ds {
			texts = append(texts, d.Code+" "+d.Message)
		}
		got := strings.Join(texts, "\n")
		if len(ds) == 0 {
			t.Fatal("expected the catch to be refused")
		}
		if i == 0 {
			want = got
		} else if got != want {
			t.Fatalf("order %v: diagnostics\n%s\nwant\n%s", order, got, want)
		}
	}
}
