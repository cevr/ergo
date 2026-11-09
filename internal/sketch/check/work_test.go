package check_test

import (
	"math/rand"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// workCase is one generated Work literal of the work rule (work.go;
// runtime.md, Machines and actors 12): its form, the type of Run's result,
// the error row of its function, and the maps it gives.
type workCase struct {
	listen    bool
	result    string // Sig (the signal), V (a variant of it) or int
	failures  []string
	onSuccess bool
	onFailure bool
}

// workRuleSource is a package with the signal Sig = V | A | Other, the error
// types A (a signal) and B (not one), and the Work literal of c.
func workRuleSource(c workCase) string {
	row := ""
	body := ""
	if len(c.failures) > 0 {
		row = " fails " + strings.Join(c.failures, " | ")
		body = "\tlet _ = try g" + strings.Join(c.failures, "") + "()\n"
	}
	var keys []string
	if c.listen {
		keys = append(keys, "Listen: func(send func(e Sig))"+row+" {\n"+body+"\tsend(Other)\n}")
	} else {
		value := map[string]string{"Sig": "Other", "V": "V{N: 1}", "int": "1"}[c.result]
		keys = append(keys, "Run: func() "+c.result+row+" {\n"+body+"\t"+value+"\n}")
	}
	if c.onSuccess {
		t := c.result
		if c.listen {
			t = "int"
		}
		keys = append(keys, "OnSuccess: func(t "+t+") Sig { Other }")
	}
	if c.onFailure {
		switch len(c.failures) {
		case 0:
			keys = append(keys, "OnFailure: func(f A) Sig { Other }")
		case 1:
			keys = append(keys, "OnFailure: func(f "+c.failures[0]+") Sig { Other }")
		default:
			keys = append(keys, "OnFailure: func(f) { Other }")
		}
	}
	return `type V { N int }
type A {}
type B {}
type Other {}
type Sig = V | A | Other

func gA() int fails A { return A }
func gB() int fails B { return B }
func gAB() int fails A | B { return B }

func w() Option[Work[Sig]] {
	Some{Work{` + strings.Join(keys, ", ") + `}}
}

func main() {
	match w() {
	case Some[Work[Sig]]:
	case None:
	}
}
`
}

// workRuleVerdict is the rule's table: "" to accept, else the text the
// one diagnostic holds.
func workRuleVerdict(c workCase) []string {
	var want []string
	if c.listen && c.onSuccess {
		return []string{"a Listen work has no result"}
	}
	if !c.listen && c.result == "int" && !c.onSuccess {
		want = append(want, "write OnSuccess: func(t int) Sig")
	}
	outside := false
	for _, f := range c.failures {
		if f == "B" {
			outside = true
		}
	}
	switch {
	case outside && !c.onFailure:
		want = append(want, "can fail with B, which is not a signal Sig: write OnFailure")
	case len(c.failures) == 0 && c.onFailure:
		want = append(want, "fails with nothing, so OnFailure is never called")
	}
	return want
}

// TestWorkLiteralRule is the acceptance property of the work rule (MU4):
// over every (form, result type, failure row, given maps), the checker
// accepts the Work literal exactly when the table says so, and otherwise
// reports the diagnostics that name each missing or needless map, as
// effect-machine's task-handler.property.ts holds its overload table. A
// failure prints the source.
func TestWorkLiteralRule(t *testing.T) {
	rows := [][]string{{}, {"A"}, {"B"}, {"A", "B"}}
	r := rand.New(rand.NewSource(1))
	var cases []workCase
	for _, listen := range []bool{false, true} {
		for _, result := range []string{"Sig", "V", "int"} {
			if listen && result != "Sig" {
				continue
			}
			for _, row := range rows {
				for _, s := range []bool{false, true} {
					for _, f := range []bool{false, true} {
						cases = append(cases, workCase{listen: listen, result: result, failures: row, onSuccess: s, onFailure: f})
					}
				}
			}
		}
	}
	r.Shuffle(len(cases), func(i, j int) { cases[i], cases[j] = cases[j], cases[i] })
	for _, c := range cases {
		src := workRuleSource(c)
		_, diags := check.CheckSource(src)
		want := workRuleVerdict(c)
		var got []string
		for _, d := range diags {
			got = append(got, d.Code+" "+d.Message)
		}
		// Each wanted text in one S108, in any order (two at one position
		// sort by text).
		fail := len(got) != len(want)
		for _, w := range want {
			found := false
			for _, g := range got {
				found = found || strings.HasPrefix(g, "S108 ") && strings.Contains(g, w)
			}
			fail = fail || !found
		}
		if fail {
			t.Fatalf("case %+v: diagnostics %q; want %q\n%s", c, got, want, src)
		}
	}
	t.Logf("%d cases", len(cases))
}
