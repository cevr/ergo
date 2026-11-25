package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runGit(t *testing.T, dir string, argv ...string) string {
	t.Helper()
	cmd := exec.Command("git", argv...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", argv, err, out)
	}
	return strings.TrimSpace(string(out))
}

func initGitRepo(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "base")
	return runGit(t, dir, "rev-parse", "HEAD")
}

func TestEditedKillersGoMapping(t *testing.T) {
	baseSrc := []byte(`package pkg

import "testing"

var sharedLimit = 10

func assertEq(t *testing.T, got, want int) {
	if got != want {
		t.Fatalf("got %d want %d", got, want)
	}
}

func TestMain(m *testing.M) {
	_ = m.Run()
}

func TestAlpha(t *testing.T) {
	assertEq(t, 1+1, 2)
}

func TestBeta(t *testing.T) {
	assertEq(t, 2+2, 4)
}

func TestFamPart1(t *testing.T) {
	assertEq(t, 3+3, 6)
}
`)

	// 1. Editing only TestAlpha (even with an added import) matches only pkg:TestAlpha and tagged pkg[tag]:TestAlpha.
	onlyAlphaSrc := []byte(`package pkg

import (
	"fmt"
	"testing"
)

var sharedLimit = 10

func assertEq(t *testing.T, got, want int) {
	if got != want {
		t.Fatalf("got %d want %d", got, want)
	}
}

func TestMain(m *testing.M) {
	_ = m.Run()
}

func TestAlpha(t *testing.T) {
	_ = fmt.Sprint(1)
	assertEq(t, 1+1, 2)
}

func TestBeta(t *testing.T) {
	assertEq(t, 2+2, 4)
}

func TestFamPart1(t *testing.T) {
	assertEq(t, 3+3, 6)
}
`)
	editedTests, wholePkg := analyzeGoTestEdits(baseSrc, onlyAlphaSrc, []lineRange{{Start: 3, End: 3}}, []lineRange{{Start: 3, End: 6}, {Start: 21, End: 21}})
	if wholePkg || !editedTests["TestAlpha"] || editedTests["TestBeta"] {
		t.Fatalf("only-TestAlpha edit: editedTests=%v wholePkg=%v, want only TestAlpha and wholePkg=false", editedTests, wholePkg)
	}
	efAlpha := editedTestFile{File: "pkg/a_test.go", PkgDir: "pkg", Kind: editedGo, WholePackage: wholePkg, EditedTests: editedTests}
	if hits := matchKiller("pkg:TestAlpha", []editedTestFile{efAlpha}); len(hits) != 1 {
		t.Errorf("expected pkg:TestAlpha to match, got %v", hits)
	}
	if hits := matchKiller("pkg[slow]:TestAlpha", []editedTestFile{efAlpha}); len(hits) != 1 {
		t.Errorf("expected tagged pkg[slow]:TestAlpha to match, got %v", hits)
	}
	if hits := matchKiller("pkg:TestBeta", []editedTestFile{efAlpha}); len(hits) != 0 {
		t.Errorf("expected pkg:TestBeta NOT to match, got %v", hits)
	}
	if hits := matchKiller("other:TestAlpha", []editedTestFile{efAlpha}); len(hits) != 0 {
		t.Errorf("expected other:TestAlpha NOT to match, got %v", hits)
	}

	// 2. Editing TestFamPart1 matches family killer pkg:TestFam.
	famTests, famWhole := analyzeGoTestEdits(baseSrc, baseSrc, nil, []lineRange{{Start: 26, End: 26}})
	if famWhole || !famTests["TestFamPart1"] {
		t.Fatalf("family part edit: editedTests=%v wholePkg=%v", famTests, famWhole)
	}
	efFam := editedTestFile{File: "pkg/a_test.go", PkgDir: "pkg", Kind: editedGo, EditedTests: famTests}
	if hits := matchKiller("pkg:TestFam", []editedTestFile{efFam}); len(hits) != 1 {
		t.Errorf("expected family killer pkg:TestFam to match TestFamPart1 edit, got %v", hits)
	}

	// 3. Editing shared helper assertEq sets wholePackage=true and matches all tests in pkg (not other).
	helperTests, helperWhole := analyzeGoTestEdits(baseSrc, baseSrc, []lineRange{{Start: 8, End: 8}}, []lineRange{{Start: 8, End: 8}})
	if !helperWhole {
		t.Fatalf("helper edit: expected wholePackage=true, got false (editedTests=%v)", helperTests)
	}
	efHelper := editedTestFile{File: "pkg/a_test.go", PkgDir: "pkg", Kind: editedGo, WholePackage: helperWhole, EditedTests: helperTests}
	for _, id := range []string{"pkg:TestAlpha", "pkg:TestBeta", "pkg[slow]:TestGamma"} {
		if hits := matchKiller(id, []editedTestFile{efHelper}); len(hits) != 1 {
			t.Errorf("helper edit should match %q, got %v", id, hits)
		}
	}
	if hits := matchKiller("other:TestAlpha", []editedTestFile{efHelper}); len(hits) != 0 {
		t.Errorf("helper edit in pkg must not match other package, got %v", hits)
	}

	// 4. Editing TestMain or package var sets wholePackage=true.
	if _, w := analyzeGoTestEdits(baseSrc, baseSrc, nil, []lineRange{{Start: 5, End: 5}}); !w {
		t.Errorf("package var edit should set wholePackage=true")
	}
	if _, w := analyzeGoTestEdits(baseSrc, baseSrc, nil, []lineRange{{Start: 14, End: 14}}); !w {
		t.Errorf("TestMain edit should set wholePackage=true")
	}
}

func TestEditedKillersErgoMapping(t *testing.T) {
	repo := t.TempDir()
	base := initGitRepo(t, repo, map[string]string{
		"pkg/a/lib.ergo":               "func Add(a int, b int) int {\n\ta + b\n}\n\nlaw AddLaw(a int, b int) {\n\tAdd(a, b) == a + b\n}\n",
		"pkg/a/pure.ergo":              "func Sub(a int, b int) int {\n\ta - b\n}\n",
		"pkg/b/check_test.ergo":        "test Smoke {\n\t1 == 1\n}\n",
		"examples/demo/demo_laws.ergo": "law DemoLaw(x int) {\n\tx == x\n}\n",
		"modules/work/work_tests.ergo": "test WorkSmoke {\n\ttrue\n}\n",
	})
	for rel, next := range map[string]string{
		"pkg/a/lib.ergo":               "func Add(a int, b int) int {\n\ta + b\n}\n\nlaw AddLaw(a int, b int) {\n\tAdd(a, b) == Add(a, b)\n}\n",
		"pkg/a/pure.ergo":              "func Sub(a int, b int) int {\n\ta - b + 0\n}\n",
		"pkg/b/check_test.ergo":        "test Smoke {\n\t2 == 2\n}\n",
		"examples/demo/demo_laws.ergo": "law DemoLaw(x int) {\n\ttrue\n}\n",
		"modules/work/work_tests.ergo": "test WorkSmoke {\n\t1 == 1\n}\n",
	} {
		if err := os.WriteFile(filepath.Join(repo, filepath.FromSlash(rel)), []byte(next), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	changed, err := gitChangedFiles(repo, base)
	if err != nil {
		t.Fatal(err)
	}
	edited, err := classifyEditedTestFiles(repo, base, changed)
	if err != nil {
		t.Fatal(err)
	}
	editedByFile := map[string]editedTestFile{}
	for _, ef := range edited {
		editedByFile[ef.File] = ef
	}
	for _, want := range []string{"pkg/a/lib.ergo", "pkg/b/check_test.ergo", "examples/demo/demo_laws.ergo", "modules/work/work_tests.ergo"} {
		if _, ok := editedByFile[want]; !ok {
			t.Errorf("expected %q to be classified as edited test/law file, got %v", want, editedByFile)
		}
	}
	if _, ok := editedByFile["pkg/a/pure.ergo"]; ok {
		t.Errorf("pkg/a/pure.ergo has no test/law declarations and should not be classified as a test/law file")
	}

	if hits := matchKiller("pkg/a:law:AddLaw", edited); len(hits) != 1 || hits[0].EditedFile != "pkg/a/lib.ergo" {
		t.Errorf("expected pkg/a:law:AddLaw to match pkg/a/lib.ergo, got %v", hits)
	}
	if hits := matchKiller("pkg/b:Smoke", edited); len(hits) != 1 || hits[0].EditedFile != "pkg/b/check_test.ergo" {
		t.Errorf("expected pkg/b:Smoke to match pkg/b/check_test.ergo, got %v", hits)
	}
	if hits := matchKiller("examples:demo", edited); len(hits) != 1 || hits[0].EditedFile != "examples/demo/demo_laws.ergo" {
		t.Errorf("expected examples:demo to match examples/demo/demo_laws.ergo, got %v", hits)
	}
	if hits := matchKiller("examples:modules/work", edited); len(hits) != 1 || hits[0].EditedFile != "modules/work/work_tests.ergo" {
		t.Errorf("expected examples:modules/work to match modules/work/work_tests.ergo, got %v", hits)
	}
	if hits := matchKiller("pkg/unrelated:law:Other", edited); len(hits) != 0 {
		t.Errorf("expected unrelated package not to match, got %v", hits)
	}
}

func TestEditedKillersSkipsDeletedTargetFile(t *testing.T) {
	repo := t.TempDir()
	base := initGitRepo(t, repo, map[string]string{
		"go.mod":          "module test.local/deleted\n\ngo 1.27\n",
		"pkg/old.go":      "package pkg\n\nfunc Old() int { return 1 }\n",
		"pkg/old_test.go": "package pkg\n\nimport \"testing\"\n\nfunc TestOld(t *testing.T) {\n\tif Old() != 1 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n",
		"pkg/testdata/mutants.json": `{
  "about": "deleted target fixture",
  "mutants": [
    {
      "name": "old-ret",
      "origin": "test",
      "file": "pkg/old.go",
      "from": "return 1",
      "to": "return 2",
      "rule": "Old returns 1",
      "killers": ["pkg:TestOld"]
    }
  ]
}
`,
	})
	if err := os.Remove(filepath.Join(repo, "pkg", "old.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "pkg", "old_test.go"), []byte("package pkg\n\nimport \"testing\"\n\nfunc TestOld(t *testing.T) {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"-root", repo, "-check-edited-killers", "-base", base, "-dry-run"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("dry-run exit %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "skipped\tpkg/testdata/mutants.json\told-ret\tpkg/old.go") ||
		!strings.Contains(stdout.String(), "edited-killers: 0 affected mutant rows") {
		t.Errorf("dry-run expected skipped old-ret and 0 affected mutant rows, got:\n%s", stdout.String())
	}

	var liveOut, liveErr bytes.Buffer
	liveCode := run([]string{"-root", repo, "-check-edited-killers", "-base", base, "-workers", "1", "-tests", "1", "-procs", "2", "-no-native-ergo"}, &liveOut, &liveErr)
	if liveCode != 0 {
		t.Fatalf("live mode exit %d, want 0 when only affected mutant's target file was deleted\nstdout:\n%s\nstderr:\n%s", liveCode, liveOut.String(), liveErr.String())
	}
	if !strings.Contains(liveOut.String(), "skipped\tpkg/testdata/mutants.json\told-ret\tpkg/old.go") ||
		!strings.Contains(liveOut.String(), "edited-killers: 0 affected mutant rows") {
		t.Errorf("live mode expected skipped old-ret and 0 affected mutant rows, got:\n%s", liveOut.String())
	}
}

func TestEditedKillersReportStatuses(t *testing.T) {
	makeAffected := func(name, gap string) []affectedMutant {
		return []affectedMutant{{
			LedgerPath: "pkg/testdata/mutants.json",
			LedgerRel:  "pkg/testdata/mutants.json",
			Mutant:     &Mutant{Name: name, File: "pkg/a.go", Rule: "must hold", Gap: gap, Killers: []string{"pkg:TestA"}},
			Hits:       []editedKillerHit{{EditedFile: "pkg/a_test.go", KillerID: "pkg:TestA"}},
		}}
	}

	for _, status := range []string{Survived, Uncovered, Stale, Invalid, Hung, Unjudged} {
		var out strings.Builder
		code := reportEditedKillers(&out, makeAffected("m-"+status, ""), nil, []Result{{
			Ledger: "pkg/testdata/mutants.json",
			Name:   "m-" + status,
			Status: status,
			Detail: "reason detail",
		}})
		if code != 1 {
			t.Errorf("status %s: exit %d, want 1\n%s", status, code, out.String())
		}
		for _, want := range []string{"m-" + status, "must hold", "pkg/a.go", "pkg/a_test.go"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("status %s: output missing %q:\n%s", status, want, out.String())
			}
		}
	}

	var okOut strings.Builder
	if code := reportEditedKillers(&okOut, makeAffected("m-killed", ""), nil, []Result{{
		Ledger: "pkg/testdata/mutants.json",
		Name:   "m-killed",
		Status: Killed,
		Killer: "pkg:TestA",
		Reason: "fail",
	}}); code != 0 {
		t.Errorf("killed status: exit %d, want 0\n%s", code, okOut.String())
	}
}

func TestEditedKillersEndToEndControls(t *testing.T) {
	repo := t.TempDir()
	base := initGitRepo(t, repo, map[string]string{
		"go.mod": "module test.local/guard\n\ngo 1.27\n",
		"calc/calc.go": `package calc

func Add(a, b int) int {
	return a + b
}

func Mul(a, b int) int {
	return a * b
}
`,
		"calc/calc_test.go": `package calc

import "testing"

func TestAdd(t *testing.T) {
	if Add(2, 3) != 5 {
		t.Fatal("Add(2, 3) != 5")
	}
}

func TestMul(t *testing.T) {
	if Mul(2, 3) != 6 {
		t.Fatal("Mul(2, 3) != 6")
	}
}
`,
		"calc/testdata/mutants.json": `{
  "about": "guard end-to-end fixture",
  "mutants": [
    {
      "name": "calc-add-sign",
      "origin": "test",
      "file": "calc/calc.go",
      "decl": "Add",
      "from": "return a + b",
      "to": "return a - b",
      "rule": "Add returns the sum of its arguments",
      "killers": ["calc:TestAdd"]
    },
    {
      "name": "calc-mul-sign",
      "origin": "test",
      "file": "calc/calc.go",
      "decl": "Mul",
      "from": "return a * b",
      "to": "return a + b",
      "rule": "Mul returns the product of its arguments",
      "killers": ["calc:TestMul"]
    }
  ]
}
`,
	})

	// 0. Unchanged tree: 0 affected mutant rows, exit 0.
	var zeroOut, zeroErr bytes.Buffer
	if code := run([]string{"-root", repo, "-check-edited-killers", "-base", base, "-no-native-ergo"}, &zeroOut, &zeroErr); code != 0 {
		t.Fatalf("clean tree exit %d, want 0\nstdout:\n%s\nstderr:\n%s", code, zeroOut.String(), zeroErr.String())
	}
	if !strings.Contains(zeroOut.String(), "edited-killers: 0 affected mutant rows") {
		t.Errorf("clean tree output missing 0 affected summary:\n%s", zeroOut.String())
	}

	// 1. Negative control: weaken assertion in TestAdd so calc-add-sign survives.
	weakenedTest := `package calc

import "testing"

func TestAdd(t *testing.T) {
	_ = Add(2, 3)
}

func TestMul(t *testing.T) {
	if Mul(2, 3) != 6 {
		t.Fatal("Mul(2, 3) != 6")
	}
}
`
	if err := os.WriteFile(filepath.Join(repo, "calc", "calc_test.go"), []byte(weakenedTest), 0o644); err != nil {
		t.Fatal(err)
	}

	// Dry-run lists only calc-add-sign (not unrelated calc-mul-sign) and exits 0.
	var dryOut, dryErr bytes.Buffer
	if code := run([]string{"-root", repo, "-check-edited-killers", "-base", base, "-dry-run", "-no-native-ergo"}, &dryOut, &dryErr); code != 0 {
		t.Fatalf("dry-run exit %d, want 0\nstdout:\n%s\nstderr:\n%s", code, dryOut.String(), dryErr.String())
	}
	if !strings.Contains(dryOut.String(), "calc-add-sign") || !strings.Contains(dryOut.String(), "edited=calc/calc_test.go") || !strings.Contains(dryOut.String(), "killer=calc:TestAdd") {
		t.Errorf("dry-run output missing expected row:\n%s", dryOut.String())
	}
	if strings.Contains(dryOut.String(), "calc-mul-sign") {
		t.Errorf("dry-run unexpectedly selected unrelated calc-mul-sign:\n%s", dryOut.String())
	}

	// Live negative control MUST fail (exit 1) and name surviving mutant, rule, file, and edited test file.
	var negOut, negErr bytes.Buffer
	negCode := run([]string{"-root", repo, "-check-edited-killers", "-base", base, "-workers", "1", "-tests", "1", "-procs", "2", "-no-native-ergo"}, &negOut, &negErr)
	if negCode != 1 {
		t.Fatalf("negative control (weakened assertion) exit %d, want 1\nstdout:\n%s\nstderr:\n%s", negCode, negOut.String(), negErr.String())
	}
	for _, want := range []string{"calc-add-sign", "Add returns the sum of its arguments", "calc/calc.go", "calc/calc_test.go"} {
		if !strings.Contains(negOut.String(), want) {
			t.Errorf("negative control output missing %q:\n%s", want, negOut.String())
		}
	}

	// 2. Positive control: strengthen TestAdd while preserving coverage -> MUST pass (exit 0).
	strongTest := `package calc

import "testing"

func TestAdd(t *testing.T) {
	if Add(2, 3) != 5 || Add(10, -4) != 6 {
		t.Fatal("Add failed")
	}
}

func TestMul(t *testing.T) {
	if Mul(2, 3) != 6 {
		t.Fatal("Mul(2, 3) != 6")
	}
}
`
	if err := os.WriteFile(filepath.Join(repo, "calc", "calc_test.go"), []byte(strongTest), 0o644); err != nil {
		t.Fatal(err)
	}
	var posOut, posErr bytes.Buffer
	posCode := run([]string{"-root", repo, "-check-edited-killers", "-base", base, "-workers", "1", "-tests", "1", "-procs", "2", "-no-native-ergo"}, &posOut, &posErr)
	if posCode != 0 {
		t.Fatalf("positive control (strengthened assertion) exit %d, want 0\nstdout:\n%s\nstderr:\n%s", posCode, posOut.String(), posErr.String())
	}
	if !strings.Contains(posOut.String(), "1 killed, 0 failed") {
		t.Errorf("positive control output missing killed summary:\n%s", posOut.String())
	}
}
