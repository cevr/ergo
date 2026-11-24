package lint_test

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketchfront"
)

func testSupportConf(t *testing.T, pkgs []string, seams map[string][]string) *lint.Configuration {
	t.Helper()
	settings := map[string]lint.Setting{}
	for _, r := range lint.Builtin().Rules() {
		settings[r.Name] = lint.Setting{Severity: lint.Off, HasSeverity: true}
	}
	s := lint.Setting{}
	if len(pkgs) > 0 || len(seams) > 0 {
		body := map[string]any{}
		if len(pkgs) > 0 {
			body["packages"] = pkgs
		}
		if len(seams) > 0 {
			body["seams"] = seams
		}
		s.Options, _ = json.Marshal(body)
	}
	settings["production-uses-test-support"] = s
	conf, err := lint.Builtin().Configure(lint.Config{Version: lint.ConfigVersion, Rules: settings}, []string{lint.Layout})
	if err != nil {
		t.Fatal(err)
	}
	return conf
}

// TestProductionUsesTestSupportAgreesWithReference puts test Demo, law
// Check, and ordinary Poll in the same file; tests and laws using
// fixture.MakeClock are legal, while ordinary Poll calling it is
// reported. Also verifies seam-scoped support-to-support rules and
// inactive default. Mutant: layout-test-support-file-wide-exemption
// (treats every declaration in a test-holding file as exempt).
func TestProductionUsesTestSupportAgreesWithReference(t *testing.T) {
	hits, clean := 0, 0
	for _, seed := range propertySeeds(16) {
		r := rand.New(rand.NewPCG(seed, 0x7e57))
		pollCallsFixture := r.IntN(2) == 0
		crossSeamSupport := r.IntN(2) == 0
		if seed%4 == 0 {
			pollCallsFixture, crossSeamSupport = false, false
		}
		dir := t.TempDir()

		pollBody := "1"
		if pollCallsFixture {
			pollBody = "fixture.MakeClock()"
		}
		otherSupportBody := "2"
		otherImport := ""
		if crossSeamSupport {
			otherImport = "import app/fixture\n\n"
			otherSupportBody = "fixture.MakeClock()"
		}
		files := map[string]string{
			"ergo.mod":             "module app\n",
			"fixture/clock.ergo":   "func MakeClock() int { 42 }\n",
			"samefix/helper.ergo":  "import app/fixture\n\nfunc SameSeam() int { fixture.MakeClock() }\n",
			"otherfix/helper.ergo": otherImport + "func OtherSeam() int { " + otherSupportBody + " }\n",
			"watch/watch.ergo": strings.Join([]string{
				"import app/fixture",
				"import app/samefix",
				"import app/otherfix",
				"",
				"func Poll() int { " + pollBody + " }",
				"",
				"func Total() int { Poll() + samefix.SameSeam() + otherfix.OtherSeam() }",
				"",
				"test Demo {",
				"\tassert fixture.MakeClock() == 42",
				"}",
				"",
				"law Check(x int) { fixture.MakeClock() + x - x == 42 }",
			}, "\n") + "\n",
			"cmd/app/main.ergo": "import app/watch\n\nfunc main() needs Console {\n\tConsole.Println(\"\\(watch.Total())\")\n}\n",
		}
		for name, text := range files {
			path := filepath.Join(dir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		a, err := sketchfront.CheckDir(filepath.Join(dir, "cmd", "app"))
		if err != nil {
			t.Fatal(err)
		}
		in := lint.InputOf(a)
		if !in.Checked {
			t.Fatalf("seed %d: does not check: %v", seed, a.Diagnostics)
		}
		replay := fmt.Sprintf("replay: go test -run TestProductionUsesTestSupportAgreesWithReference ./internal/sketch/lint -lint.seed=%d", seed)
		if res := testSupportConf(t, nil, nil).Run(in); len(res.Findings) != 0 {
			t.Fatalf("seed %d (%s): inactive config reported %v", seed, replay, res.Findings)
		}
		seams := map[string][]string{
			"clock": {"app/fixture", "app/samefix"},
			"other": {"app/otherfix"},
		}
		res := testSupportConf(t, nil, seams).Run(in)
		var got []string
		for _, f := range res.Findings {
			got = append(got, f.File+":"+f.Message)
		}
		slices.Sort(got)
		var want []string
		// watch.Total uses samefix (seam clock) and otherfix (seam other).
		want = append(want,
			"watch/watch.ergo:app/watch.Total uses app/otherfix.OtherSeam from configured test support app/otherfix",
			"watch/watch.ergo:app/watch.Total uses app/samefix.SameSeam from configured test support app/samefix",
		)
		if pollCallsFixture {
			want = append(want, "watch/watch.ergo:app/watch.Poll uses app/fixture.MakeClock from configured test support app/fixture")
			hits++
		} else {
			clean++
		}
		if crossSeamSupport {
			want = append(want, "otherfix/helper.ergo:app/otherfix.OtherSeam uses app/fixture.MakeClock from configured test support app/fixture")
		}
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("seed %d (%s):\ngot  %q\nwant %q", seed, replay, got, want)
		}
		if !res.PolicyPassed(true) || lint.GateFails(res.Findings, 0) {
			t.Fatalf("seed %d (%s): advisory finding fails a gate", seed, replay)
		}
	}
	if len(propertySeeds(16)) > 1 && (hits == 0 || clean == 0) {
		t.Fatalf("vacuous: hits=%d clean=%d", hits, clean)
	}
}
