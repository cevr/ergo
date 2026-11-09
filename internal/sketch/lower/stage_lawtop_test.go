package lower

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/sketchtest"
)

// law-top (spec A5): a top-level law is checked and not in the run build, as
// a member law is (types.md, Laws 11); the test build runs both (#162
// law-run-1). The law corpus is the checker's: the
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
// the law corpus, the run build is byte for byte the run build of the
// program with every law removed (buildsOmitLaws, no exception), no //line
// directive maps into a law, and the test build, which lowers each law,
// lowers.
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
