package lower

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
)

// TestRefutedLawSitesAgree is the smallest repro of EV6's declaration-site
// drift: a false law must name the failing expression, even in a nested
// tail or an explicit return, with its declaration as related information.
// It builds Go because source positions in a recovered native assertion
// cannot be established by go/types or the interpreter being compared.
// Mutant: law-refutes-at-declaration (testdata/mutants.json).
func TestRefutedLawSitesAgree(t *testing.T) {
	needsBuild(t)
	const src = `law Tail() {
	false
}
law Returned() {
	return false
}
law Nested() {
	if true {
		false
	} else {
		true
	}
}
type Probe {
	Read() int,
	law Member() {
		Read() == 1
	},
}
type Zero {}
func Read(z Zero) int { 0 }
law Choice(b bool) {
	return match b {
	case true:
		false
	case false:
		false
	}
}
type Fault {}
func bad() bool fails Fault { Fault }
law Caught() {
	bad() catch {
	case Fault:
		false
	}
}
law Compared() {
	(if true { false } else { true }) == true
}
law Tried() {
	try bad() catch {
	case Fault:
		false
	}
}
law Paren() {
	(false)
}
`
	rep, err := evaluate(src)
	if err != nil {
		t.Fatal(err)
	}
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "law_sites", src)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["law_sites"])
	if code != 1 {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
	want := map[string][4]int{
		"law Tail": {2, 2, 1, 1}, "law Returned": {5, 9, 4, 1},
		"law Nested": {9, 3, 7, 1}, "law Probe.Member for Zero": {17, 3, 16, 2},
		"law Choice": {27, 3, 22, 1}, "law Caught": {35, 3, 32, 1},
		"law Compared": {39, 2, 38, 1},
		"law Tried":    {44, 3, 41, 1},
		"law Paren":    {48, 3, 47, 1},
	}
	for _, res := range rep.Results {
		pos, ok := want[res.Name]
		if !ok || res.Status != eval.Refuted || res.Site.Line != pos[0] || res.Site.Column != pos[1] {
			t.Errorf("%s: %+v; want %v", res.Name, res, pos)
			continue
		}
		if d := res.Declaration; d == nil || d.Line != pos[2] || d.Column != pos[3] {
			t.Errorf("%s: declaration %+v, want %d:%d", res.Name, d, pos[2], pos[3])
		}
		failure := fmt.Sprintf("law refuted at main.ergo:%d:%d: the law is false", pos[0], pos[1])
		related := fmt.Sprintf("\tlaw declared at main.ergo:%d:%d", pos[2], pos[3])
		if !strings.Contains(stdout, failure) || !strings.Contains(stdout, related) {
			t.Errorf("%s: want %q and %q\n%s", res.Name, failure, related, stdout)
		}
		delete(want, res.Name)
	}
	if len(want) > 0 {
		t.Errorf("missing obligations: %v", want)
	}
}
