package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runMutantsInProc(t *testing.T, args ...string) (mutantReport, int) {
	t.Helper()
	opts, err := parseFileOptions("test", append([]string{"--mutants", "--json"}, args...))
	if err != nil {
		t.Fatalf("parseFileOptions(%v): %v", args, err)
	}
	var stdout, stderr bytes.Buffer
	runErr := mutantsTestCommand(opts, &stdout, &stderr)
	code := exitCode(runErr)
	var rep mutantReport
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("unmarshal report (exit %d, stderr=%q, stdout=%q): %v", code, stderr.String(), stdout.String(), err)
	}
	return rep, code
}

func verdictByName(t *testing.T, rep mutantReport, name string) mutantVerdict {
	t.Helper()
	for _, v := range rep.Results {
		if v.Name == name {
			return v
		}
	}
	t.Fatalf("mutant %q not found in report %+v", name, rep.Results)
	return mutantVerdict{}
}

func writeMutantFixtureModule(t *testing.T, dir string) {
	t.Helper()
	smokeWrite(t, dir, "ergo.mod", "module sample\n")
	smokeWrite(t, dir, "lib.ergo", `func Add(a int, b int) int {
	a + b
}

func Sub(a int, b int) int {
	a - b
}

func Unused(x int) int {
	x + 1
}

law AddCommutes(a int, b int) {
	Add(a, b) == Add(b, a)
}

test TestAdd {
	assert Add(2, 3) == 5
}

test TestSub {
	assert Sub(7, 2) == 5
}
`)
	smokeWrite(t, dir, "unrelated.ergo", `func Unrelated(x int) int {
	x + 10
}
`)
	ledger := `{
  "about": "sample fixture ledger",
  "mutants": [
    {
      "name": "m-add-killed",
      "file": "lib.ergo",
      "decl": "Add",
      "from": "a + b",
      "to": "a - b",
      "rule": "Add sums its arguments",
      "killers": []
    },
    {
      "name": "m-sub-killed",
      "file": "lib.ergo",
      "decl": "Sub",
      "from": "a - b",
      "to": "a + b",
      "rule": "Sub subtracts b from a",
      "killers": [".:TestSub"]
    },
    {
      "name": "m-unused-survived",
      "file": "lib.ergo",
      "decl": "Unused",
      "from": "x + 1",
      "to": "x + 2",
      "rule": "Unused adds one",
      "killers": []
    },
    {
      "name": "m-invalid-type",
      "file": "lib.ergo",
      "decl": "Add",
      "from": "a + b",
      "to": "\"wrong\"",
      "rule": "type error makes row invalid",
      "killers": []
    },
    {
      "name": "m-invalid-apply",
      "file": "lib.ergo",
      "decl": "Add",
      "from": "not present",
      "to": "x",
      "rule": "missing from makes row invalid",
      "killers": []
    },
    {
      "name": "m-go-skipped",
      "file": "other.go",
      "from": "x",
      "to": "y",
      "rule": "Go row skipped by native Ergo runner",
      "killers": []
    }
  ]
}
`
	smokeWrite(t, dir, "testdata/mutants.json", ledger)
}

func TestMutantsRunnerEndToEndAndCache(t *testing.T) {
	t.Setenv("ERGO_CACHE", t.TempDir())
	t.Setenv("ERGO_LAWCACHE", "")
	dir := t.TempDir()
	writeMutantFixtureModule(t, dir)

	libPath := filepath.Join(dir, "lib.ergo")
	origLib, err := os.ReadFile(libPath)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Cold run of the two killable rows.
	rep, code := runMutantsInProc(t, "--fresh", "--run", "^m-(add|sub)-killed$", dir)
	if code != 0 || rep.Killed != 2 || rep.Cached != 0 || rep.Survived != 0 || rep.Invalid != 0 {
		t.Fatalf("cold run: exit %d rep %+v", code, rep)
	}
	if v := verdictByName(t, rep, "m-add-killed"); v.Status != mutantKilled || v.Cached || v.Runner != "eval" {
		t.Fatalf("m-add-killed cold: %+v", v)
	}
	if v := verdictByName(t, rep, "m-sub-killed"); v.Status != mutantKilled || v.Cached {
		t.Fatalf("m-sub-killed cold: %+v", v)
	}
	// Verify ZERO disk writes to source files.
	afterLib, err := os.ReadFile(libPath)
	if err != nil || !bytes.Equal(origLib, afterLib) {
		t.Fatalf("in-memory overlay modified disk file!")
	}

	// 2. Warm run: both rows must hit the reach-digest cache.
	rep, code = runMutantsInProc(t, "--run", "^m-(add|sub)-killed$", dir)
	if code != 0 || rep.Killed != 2 || rep.Cached != 2 {
		t.Fatalf("warm run: exit %d rep %+v; want 2 killed, 2 cached", code, rep)
	}

	// 3. Edit an UNRELATED .ergo file by 1 line: 0 cached rows may re-run!
	smokeWrite(t, dir, "unrelated.ergo", "func Unrelated(x int) int {\n\tx + 99\n}\n")
	rep, code = runMutantsInProc(t, "--run", "^m-(add|sub)-killed$", dir)
	if code != 0 || rep.Killed != 2 || rep.Cached != 2 {
		t.Fatalf("after unrelated 1-line edit: exit %d rep %+v; want 2 killed, 2 cached (0 re-runs)", code, rep)
	}

	// 4. Edit the mutated declaration Add (keeping from valid): only m-add-killed invalidates!
	editedAdd := strings.Replace(string(origLib), "func Add(a int, b int) int {\n\ta + b\n}", "func Add(a int, b int) int {\n\tlet z = 0\n\tz + a + b\n}", 1)
	smokeWrite(t, dir, "lib.ergo", editedAdd)
	rep, code = runMutantsInProc(t, "--run", "^m-(add|sub)-killed$", dir)
	if code != 0 || rep.Killed != 2 || rep.Cached != 1 {
		t.Fatalf("after editing Add: exit %d rep %+v; want 2 killed, 1 cached", code, rep)
	}
	if v := verdictByName(t, rep, "m-add-killed"); v.Cached {
		t.Fatalf("m-add-killed should have invalidated after editing Add: %+v", v)
	}
	if v := verdictByName(t, rep, "m-sub-killed"); !v.Cached {
		t.Fatalf("m-sub-killed should stay cached after editing Add: %+v", v)
	}

	// 5. Edit killer TestSub: only m-sub-killed invalidates!
	editedSubTest := strings.Replace(editedAdd, "assert Sub(7, 2) == 5", "assert Sub(9, 4) == 5", 1)
	smokeWrite(t, dir, "lib.ergo", editedSubTest)
	rep, code = runMutantsInProc(t, "--run", "^m-(add|sub)-killed$", dir)
	if code != 0 || rep.Killed != 2 || rep.Cached != 1 {
		t.Fatalf("after editing TestSub: exit %d rep %+v; want 2 killed, 1 cached", code, rep)
	}
	if v := verdictByName(t, rep, "m-add-killed"); !v.Cached {
		t.Fatalf("m-add-killed should stay cached after editing TestSub: %+v", v)
	}
	if v := verdictByName(t, rep, "m-sub-killed"); v.Cached {
		t.Fatalf("m-sub-killed should invalidate after editing TestSub: %+v", v)
	}

	// 6. Changing a row's edit (to) must invalidate its cache key.
	ledgerPath := filepath.Join(dir, "testdata", "mutants.json")
	ledgerBytes, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	changedLedger := strings.Replace(string(ledgerBytes), `"to": "a + b"`, `"to": "a + b + 1"`, 1)
	smokeWrite(t, dir, "testdata/mutants.json", changedLedger)
	rep, code = runMutantsInProc(t, "--run", "^m-sub-killed$", dir)
	if code != 0 || rep.Killed != 1 || rep.Cached != 0 {
		t.Fatalf("after changing row edit to: exit %d rep %+v; want 1 killed, 0 cached", code, rep)
	}

	// 7. --fresh bypasses stored verdicts.
	rep, code = runMutantsInProc(t, "--fresh", "--run", "^m-sub-killed$", dir)
	if code != 0 || rep.Killed != 1 || rep.Cached != 0 {
		t.Fatalf("--fresh run: exit %d rep %+v; want 1 killed, 0 cached", code, rep)
	}

	// 8. Full ledger: invalid, survived, and skipped rows classified accurately and never cached.
	smokeWrite(t, dir, "lib.ergo", string(origLib))
	smokeWrite(t, dir, "testdata/mutants.json", string(ledgerBytes))
	rep, code = runMutantsInProc(t, dir)
	if code != 1 {
		t.Fatalf("full ledger with survived+invalid: exit %d, want 1", code)
	}
	if v := verdictByName(t, rep, "m-invalid-type"); v.Status != mutantInvalid {
		t.Fatalf("m-invalid-type: got %+v, want invalid", v)
	}
	if v := verdictByName(t, rep, "m-invalid-apply"); v.Status != mutantInvalid {
		t.Fatalf("m-invalid-apply: got %+v, want invalid", v)
	}
	if v := verdictByName(t, rep, "m-unused-survived"); v.Status != mutantSurvived || v.Cached {
		t.Fatalf("m-unused-survived: got %+v, want uncached survived", v)
	}
	if v := verdictByName(t, rep, "m-go-skipped"); v.Status != mutantSkipped {
		t.Fatalf("m-go-skipped: got %+v, want skipped", v)
	}
}

func TestMutantsRunnerRedBaselineIsUnjudged(t *testing.T) {
	t.Setenv("ERGO_CACHE", t.TempDir())
	t.Setenv("ERGO_LAWCACHE", "")
	dir := t.TempDir()
	smokeWrite(t, dir, "ergo.mod", "module redbase\n")
	smokeWrite(t, dir, "lib.ergo", `func Inc(x int) int {
	x + 1
}

test TestBroken {
	assert Inc(1) == 999
}
`)
	smokeWrite(t, dir, "testdata/mutants.json", `{
  "about": "red baseline fixture",
  "mutants": [
    {
      "name": "m-inc-red-base",
      "file": "lib.ergo",
      "decl": "Inc",
      "from": "x + 1",
      "to": "x + 2",
      "rule": "Inc adds one",
      "killers": []
    }
  ]
}
`)
	rep, code := runMutantsInProc(t, dir)
	if code != 1 {
		t.Fatalf("red baseline run: exit %d, want 1", code)
	}
	v := verdictByName(t, rep, "m-inc-red-base")
	if v.Status != mutantUnjudged {
		t.Fatalf("red baseline must yield unjudged (never false kill): got %+v", v)
	}
}

func TestMutantsRunnerNativeMode(t *testing.T) {
	t.Setenv("ERGO_CACHE", t.TempDir())
	t.Setenv("ERGO_LAWCACHE", "")
	dir := t.TempDir()
	writeMutantFixtureModule(t, dir)

	// Warm eval cache first.
	rep, code := runMutantsInProc(t, "--run", "^m-add-killed$", dir)
	if code != 0 || rep.Killed != 1 || rep.Cached != 0 {
		t.Fatalf("eval cold: exit %d rep %+v", code, rep)
	}
	// --native must NOT reuse eval-only verdict; must run native binary.
	rep, code = runMutantsInProc(t, "--native", "--run", "^m-add-killed$", dir)
	if code != 0 || rep.Killed != 1 || rep.Cached != 0 {
		t.Fatalf("native cold after eval warm: exit %d rep %+v; want 1 killed, 0 cached", code, rep)
	}
	if v := verdictByName(t, rep, "m-add-killed"); v.Runner != "native" {
		t.Fatalf("native run: runner = %q, want native", v.Runner)
	}
	// Second --native run hits the native cache.
	rep, code = runMutantsInProc(t, "--native", "--run", "^m-add-killed$", dir)
	if code != 0 || rep.Killed != 1 || rep.Cached != 1 {
		t.Fatalf("native warm: exit %d rep %+v; want 1 killed, 1 cached", code, rep)
	}
}

func TestMutantsRunnerCLIErrors(t *testing.T) {
	cases := [][]string{
		{"test", "--fresh", "."},
		{"test", "--mutants", "--affected", "."},
		{"test", "--mutants", "--trace", "."},
		{"test", "--mutants", "--run", "[invalid", "."},
		{"check", "--mutants", "."},
	}
	for _, argv := range cases {
		err := command(argv)
		if code := exitCode(err); code != 2 {
			t.Errorf("ergo %v: exit %d (%v), want 2", argv, code, err)
		}
	}
}
