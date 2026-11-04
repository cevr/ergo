package lower

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/sketchtest"
	"ergo.local/prototype/internal/sketch/syntax"
)

// law-top (spec A5): a top-level law is checked and in neither build, as a
// member law is (types.md, Laws 11). The law corpus is the checker's: the
// clean programs of sketchtest.LawTopCases (A2) and the clean top-level
// programs of sketchtest.GenLaws (A3). TestBuildsOmitLaws and
// TestLawFactsStayOut (stage_e3b_test.go) cover testdata/e3b and both
// placements of each lawFact.

// lawTopCorpus is every clean program of the law corpus, with a main when
// it has none, so ergo run has an entry point.
func lawTopCorpus(t *testing.T) []struct{ name, src string } {
	t.Helper()
	want := regexp.MustCompile(`// want S\d{3}`)
	var out []struct{ name, src string }
	add := func(name, src string) {
		if !strings.Contains(src, "func main(") {
			src += "\nfunc main() {}\n"
		}
		out = append(out, struct{ name, src string }{name, src})
	}
	for i, tc := range sketchtest.LawTopCases {
		if !want.MatchString(tc.Src) {
			add(fmt.Sprintf("lawtop_a2_%02d", i), tc.Src)
		}
	}
	for i, l := range sketchtest.GenLaws(sketchtest.LawDomainSeed, sketchtest.LawDomainSize) {
		src, _ := sketchtest.PlaceLaw(false, l)
		if _, diags := check.CheckSource(src); len(diags) == 0 {
			add(fmt.Sprintf("lawtop_a3_%04d", i), src)
		}
	}
	return out
}

// TestTopLevelLawsLeaveNoGo: spec A5 and Design B2. For every program of
// the law corpus, the run build and the test build are byte for byte the
// builds of the program with every law removed (buildsOmitLaws, no
// exception), and no //line directive maps into a law.
func TestTopLevelLawsLeaveNoGo(t *testing.T) {
	corpus := lawTopCorpus(t)
	a2, a3 := 0, 0
	for _, p := range corpus {
		if strings.HasPrefix(p.name, "lawtop_a2_") {
			a2++
		} else {
			a3++
		}
	}
	if a2 < 12 || a3 < 300 {
		t.Fatalf("the law corpus holds %d A2 and %d A3 clean programs; want 12 and 300 or more", a2, a3)
	}
	for _, p := range corpus {
		t.Run(p.name, func(t *testing.T) {
			run, _ := buildsOmitLaws(t, p.name, p.src)
			top := 0
			for _, l := range run.pkg.Laws {
				if l.Owner == nil {
					top++
				}
			}
			if top == 0 {
				t.Errorf("%s declares no top-level law", p.name)
			}
		})
	}
}

// TestTopLevelLawNamesAbsent: spec A5, the static leg. In both builds of
// every clean A2 program, no identifier of a law appears: each law name
// that the source spells only as a law's name is grepped for in the Go.
func TestTopLevelLawNamesAbsent(t *testing.T) {
	want := regexp.MustCompile(`// want S\d{3}`)
	checked := 0
	for _, tc := range sketchtest.LawTopCases {
		if want.MatchString(tc.Src) {
			continue
		}
		src := tc.Src
		if !strings.Contains(src, "func main(") {
			src += "\nfunc main() {}\n"
		}
		name := "lawnames_" + strings.NewReplacer(" ", "_", "-", "_", ",", "", "'", "").Replace(tc.Name)
		run := mustLower(t, name, src)
		test := lowerTestBuild(t, name+"_test", src)
		decls := map[string]int{}
		for _, l := range run.pkg.Laws {
			decls[l.Name]++
		}
		for law, n := range decls {
			word := regexp.MustCompile(`\b` + regexp.QuoteMeta(law) + `\b`)
			if len(word.FindAllStringIndex(src, -1)) != n {
				continue // the name is also another declaration's or a use's
			}
			checked++
			for _, b := range []*lowered{run, test} {
				if word.MatchString(b.mainGo()) {
					t.Errorf("%s: the Go of %s names the law %s", tc.Name, b.name, law)
				}
			}
		}
	}
	if checked < 10 {
		t.Fatalf("%d law names grepped for; want 10 or more", checked)
	}
}

// TestErgoRunTopLevelLaw: spec A5. A program with top-level laws, tests and
// main runs under ergo run with the output of the same program without its
// laws, and its test build runs its tests and prints no law line.
func TestErgoRunTopLevelLaw(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "e3b", "top_laws.ergo"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	run := mustLower(t, "lawtop_run", src)
	var names []string
	for _, l := range run.pkg.Laws {
		if l.Owner != nil {
			t.Fatalf("law %s is a member; the program holds top-level laws only", l.Name)
		}
		names = append(names, l.Name)
	}
	if len(names) < 2 || len(run.pkg.Tests) < 2 {
		t.Fatalf("the program declares %d laws and %d tests; want 2 of each", len(names), len(run.pkg.Tests))
	}
	blanked := blankLaws(t, "lawtop", src)
	if f, err := syntax.Parse(blanked); err != nil || hasLaw(f) {
		t.Fatalf("the source without its laws: %v", err)
	}
	builds := []*lowered{
		run,
		mustLower(t, "lawtop_blank_run", blanked),
		lowerTestBuild(t, "lawtop_test", src),
		lowerTestBuild(t, "lawtop_blank_test", blanked),
	}
	exes := buildAll(t, builds, true)
	if exes == nil {
		return
	}
	outs := map[string]string{}
	for _, b := range builds {
		stdout, stderr, code := runExe(t, exes[b.name])
		if code != 0 {
			t.Fatalf("%s: exit %d, stderr %q", b.name, code, firstLines(stderr, 8))
		}
		outs[b.name] = stdout
	}
	if outs["lawtop_run"] != outs["lawtop_blank_run"] || outs["lawtop_run"] != "42\n" {
		t.Errorf("ergo run printed %q; without its laws %q; want \"42\\n\" both", outs["lawtop_run"], outs["lawtop_blank_run"])
	}
	if outs["lawtop_test"] != outs["lawtop_blank_test"] {
		t.Errorf("the test build printed %q; without its laws %q", outs["lawtop_test"], outs["lawtop_blank_test"])
	}
	for _, line := range lines(outs["lawtop_test"]) {
		for _, n := range names {
			if strings.Contains(line, n) {
				t.Errorf("the test build printed a law line: %q", line)
			}
		}
	}
	if !strings.Contains(outs["lawtop_test"], "PASS (2 tests)") {
		t.Errorf("the test build printed %q, want its two tests to pass", outs["lawtop_test"])
	}
}
