package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeFakeErgoScript(t *testing.T, dir, jsonOutput string, exitCode int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake ergo test")
	}
	script := filepath.Join(dir, "fake-ergo")
	body := "#!/bin/sh\ncat <<'EOF'\n" + jsonOutput + "\nEOF\nexit " + string(rune('0'+exitCode)) + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestHandoffPartitionsErgoAndGoRows(t *testing.T) {
	root := t.TempDir()
	for rel, content := range map[string]string{
		"pkg/pure/lib.ergo":    "func F(x int) int { x + 1 }\n\nlaw L(x int) { F(x) == x + 1 }\n",
		"pkg/goonly/a.go":      "package goonly\nfunc G() int { return 1 }\n",
		"pkg/goonly/a_test.go": "package goonly\nimport \"testing\"\nfunc TestG(t *testing.T) {}\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	j := Job{
		Path: filepath.Join(root, "pkg", "pure", "testdata", "mutants.json"),
		Ledger: &Ledger{
			Mutants: []*Mutant{
				{Name: "ergo-native-killer", File: "pkg/pure/lib.ergo", From: "x + 1", To: "x + 2", Killers: []string{"pkg/pure:law:L"}},
				{Name: "ergo-go-killer", File: "pkg/pure/lib.ergo", From: "x + 1", To: "x + 3", Killers: []string{"pkg/goonly:TestG"}},
				{Name: "go-row", File: "pkg/goonly/a.go", From: "return 1", To: "return 2", Killers: []string{"pkg/goonly:TestG"}},
				{Name: "go-row-ergo-killer", File: "pkg/goonly/a.go", From: "return 1", To: "return 3", Killers: []string{"pkg/pure:law:L"}},
				{Name: "ergo-regen", File: "pkg/pure/lib.ergo", From: "x + 1", To: "x + 4", Killers: []string{"pkg/pure:law:L"}, Regenerate: &Regenerate{Dir: ".", Argv: []string{"true"}}},
				{Name: "ergo-empty-gap", File: "pkg/pure/lib.ergo", From: "x + 1", To: "x + 5", Gap: "known gap"},
				{Name: "ergo-empty-nogap", File: "pkg/pure/lib.ergo", From: "x + 1", To: "x + 6"},
			},
		},
	}

	del, rem := partitionDelegatedJobs(root, Config{Root: root}, []Job{j})
	delNames := map[string]bool{}
	for _, dj := range del {
		for _, m := range dj.Ledger.Mutants {
			delNames[m.Name] = true
		}
	}
	remNames := map[string]bool{}
	for _, rj := range rem {
		for _, m := range rj.Ledger.Mutants {
			remNames[m.Name] = true
		}
	}

	for _, wantDel := range []string{"ergo-native-killer", "ergo-empty-nogap"} {
		if !delNames[wantDel] {
			t.Errorf("expected %q delegated, got del=%v rem=%v", wantDel, delNames, remNames)
		}
		if remNames[wantDel] {
			t.Errorf("expected %q NOT in remaining, got rem=%v", wantDel, remNames)
		}
	}
	for _, wantRem := range []string{"ergo-go-killer", "go-row", "go-row-ergo-killer", "ergo-regen", "ergo-empty-gap"} {
		if !remNames[wantRem] {
			t.Errorf("expected %q kept on Go path, got del=%v rem=%v", wantRem, delNames, remNames)
		}
		if delNames[wantRem] {
			t.Errorf("expected %q NOT delegated, got del=%v", wantRem, delNames)
		}
	}

	// Config.All keeps everything on the Go path so full kill-matrix semantics stay intact.
	delAll, remAll := partitionDelegatedJobs(root, Config{Root: root, All: true}, []Job{j})
	if len(delAll) != 0 || len(remAll) != 1 {
		t.Fatalf("expected All=true to skip delegation, got del=%d rem=%d", len(delAll), len(remAll))
	}
}

func TestHandoffResolveErgoBinaryAndDisableFlag(t *testing.T) {
	root := t.TempDir()
	// With empty PATH and no <root>/bin/ergo, resolveErgoBinary returns "".
	t.Setenv("PATH", t.TempDir())
	if got := resolveErgoBinary(root, "", false); got != "" {
		t.Fatalf("expected empty when ergo absent, got %q", got)
	}

	// Creating executable <root>/bin/ergo makes it auto-discovered.
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(binDir, "ergo")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveErgoBinary(root, "", false); got != binPath {
		t.Fatalf("expected %q, got %q", binPath, got)
	}
	// -no-native-ergo disables delegation even when bin/ergo or -ergo flag is present.
	if got := resolveErgoBinary(root, binPath, true); got != "" {
		t.Fatalf("expected empty when disabled, got %q", got)
	}
}

func TestHandoffDelegatesAndPreservesAllVerdicts(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test.local/handoff\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pkgDir := filepath.Join(root, "pkg")
	if err := os.MkdirAll(filepath.Join(pkgDir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := "func Add(a int, b int) int {\n\ta + b\n}\n\nlaw AddLaw(a int, b int) {\n\tAdd(a, b) == a + b\n}\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "lib.ergo"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	ledger := `{
  "about": "handoff verdict preservation fixture",
  "mutants": [
    {
      "name": "m-killed",
      "origin": "test",
      "file": "pkg/lib.ergo",
      "decl": "Add",
      "from": "a + b",
      "to": "a - b",
      "rule": "adds",
      "killers": ["pkg:law:AddLaw"]
    },
    {
      "name": "m-gap",
      "origin": "test",
      "file": "pkg/lib.ergo",
      "decl": "Add",
      "from": "a + b",
      "to": "a * b",
      "rule": "gap row",
      "gap": "known gap",
      "killers": ["pkg:law:AddLaw"]
    },
    {
      "name": "m-survived",
      "origin": "test",
      "file": "pkg/lib.ergo",
      "decl": "Add",
      "from": "a + b",
      "to": "b + a",
      "rule": "must fail",
      "killers": ["pkg:law:AddLaw"]
    },
    {
      "name": "m-invalid",
      "origin": "test",
      "file": "pkg/lib.ergo",
      "decl": "Add",
      "from": "a + b",
      "to": "\"bad\"",
      "rule": "invalid row",
      "killers": ["pkg:law:AddLaw"]
    },
    {
      "name": "m-hung",
      "origin": "test",
      "file": "pkg/lib.ergo",
      "decl": "Add",
      "from": "a + b",
      "to": "0",
      "rule": "hung row",
      "killers": ["pkg:law:AddLaw"]
    }
  ]
}
`
	ledgerFile := filepath.Join(pkgDir, "testdata", "mutants.json")
	if err := os.WriteFile(ledgerFile, []byte(ledger), 0o644); err != nil {
		t.Fatal(err)
	}

	fakeReport := `{
  "ledgers": ["` + ledgerFile + `"],
  "killed": 1,
  "survived": 1,
  "invalid": 1,
  "unjudged": 1,
  "gap": 1,
  "results": [
    {"name": "m-killed", "file": "pkg/lib.ergo", "decl": "Add", "status": "killed", "killer": "pkg:law AddLaw for Impl", "runner": "eval", "durationMs": 50},
    {"name": "m-gap", "file": "pkg/lib.ergo", "decl": "Add", "status": "gap", "detail": "known gap", "reachedBy": ["pkg:law:AddLaw"], "durationMs": 20},
    {"name": "m-survived", "file": "pkg/lib.ergo", "decl": "Add", "status": "survived", "reachedBy": ["pkg:law:AddLaw"], "durationMs": 30},
    {"name": "m-invalid", "file": "pkg/lib.ergo", "decl": "Add", "status": "invalid", "detail": "the compiler refuses the mutated source (FK12a)", "durationMs": 10},
    {"name": "m-hung", "file": "pkg/lib.ergo", "decl": "Add", "status": "unjudged", "detail": "killer timed out (timeouts never count as kills)", "durationMs": 1000}
  ]
}`
	fakeErgo := writeFakeErgoScript(t, t.TempDir(), fakeReport, 1)
	jsonOut := filepath.Join(t.TempDir(), "results.json")

	var stdout, stderr bytes.Buffer
	code := run([]string{"-root", root, "-fresh", "-resume=false", "-ergo", fakeErgo, "-json", jsonOut, ledgerFile}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1 (survivor+invalid+hung), got %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}

	raw, err := os.ReadFile(jsonOut)
	if err != nil {
		t.Fatalf("read jsonOut: %v", err)
	}
	gotByName := map[string]Result{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	for dec.More() {
		var r Result
		if err := dec.Decode(&r); err != nil {
			t.Fatalf("decode result: %v", err)
		}
		gotByName[r.Name] = r
	}
	if len(gotByName) != 5 {
		t.Fatalf("expected 5 results, got %d: %+v", len(gotByName), gotByName)
	}
	if r := gotByName["m-killed"]; r.Status != Killed || r.Killer != "pkg:law:AddLaw" {
		t.Errorf("m-killed: got %+v, want killed by canonical pkg:law:AddLaw", r)
	}
	if r := gotByName["m-gap"]; r.Status != Survived {
		t.Errorf("m-gap: got %+v, want status survived (reported under known gaps)", r)
	}
	if r := gotByName["m-survived"]; r.Status != Survived {
		t.Errorf("m-survived: got %+v, want status survived", r)
	}
	if r := gotByName["m-invalid"]; r.Status != Invalid {
		t.Errorf("m-invalid: got %+v, want status invalid", r)
	}
	if r := gotByName["m-hung"]; r.Status != Hung {
		t.Errorf("m-hung: got %+v, want status hung", r)
	}
	if !strings.Contains(stdout.String(), "NEW SURVIVORS") || !strings.Contains(stdout.String(), "m-survived") {
		t.Errorf("expected NEW SURVIVORS naming m-survived:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "known gaps") || !strings.Contains(stdout.String(), "m-gap") {
		t.Errorf("expected known gaps naming m-gap:\n%s", stdout.String())
	}

	// Passing run (only m-killed + m-gap) must exit 0.
	passReport := `{
  "ledgers": ["` + ledgerFile + `"],
  "killed": 1,
  "gap": 1,
  "results": [
    {"name": "m-killed", "file": "pkg/lib.ergo", "decl": "Add", "status": "killed", "killer": "pkg:law:AddLaw", "runner": "eval", "durationMs": 10},
    {"name": "m-gap", "file": "pkg/lib.ergo", "decl": "Add", "status": "gap", "detail": "known gap", "reachedBy": ["pkg:law:AddLaw"], "durationMs": 10}
  ]
}`
	passErgo := writeFakeErgoScript(t, t.TempDir(), passReport, 0)
	var passOut, passErr bytes.Buffer
	passCode := run([]string{"-root", root, "-fresh", "-resume=false", "-ergo", passErgo, "-run", "^(m-killed|m-gap)$", ledgerFile}, &passOut, &passErr)
	if passCode != 0 {
		t.Fatalf("expected exit 0 for killed+gap only, got %d\nstdout:\n%s\nstderr:\n%s", passCode, passOut.String(), passErr.String())
	}
}

func TestHandoffMixedGoAndErgoAndFallbackWhenErgoAbsent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test.local/mixed\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A Go package with a real Go test, plus an Ergo package with a law.
	goDir := filepath.Join(root, "gpkg")
	ergoDir := filepath.Join(root, "epkg")
	for _, d := range []string{goDir, filepath.Join(ergoDir, "testdata")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(goDir, "calc.go"), []byte("package gpkg\n\nfunc Inc(x int) int {\n\treturn x + 1\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goDir, "calc_test.go"), []byte("package gpkg\n\nimport \"testing\"\n\nfunc TestInc(t *testing.T) {\n\tif Inc(1) != 2 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ergoDir, "lib.ergo"), []byte("func Double(x int) int {\n\tx + x\n}\n\nlaw DoubleLaw(x int) {\n\tDouble(x) == 2*x\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ledgerFile := filepath.Join(ergoDir, "testdata", "mutants.json")
	ledger := `{
  "about": "mixed go + ergo ledger",
  "mutants": [
    {
      "name": "m-go-inc",
      "origin": "test",
      "file": "gpkg/calc.go",
      "from": "return x + 1",
      "to": "return x + 2",
      "rule": "Inc adds 1",
      "killers": ["gpkg:TestInc"]
    },
    {
      "name": "m-ergo-double",
      "origin": "test",
      "file": "epkg/lib.ergo",
      "decl": "Double",
      "from": "x + x",
      "to": "x + 1",
      "rule": "Double doubles x",
      "killers": ["epkg:law:DoubleLaw"]
    }
  ]
}
`
	if err := os.WriteFile(ledgerFile, []byte(ledger), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"add", "."},
		{"commit", "-q", "-m", "init"},
	} {
		c := exec.Command("git", argv...)
		c.Dir = root
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", argv, err, out)
		}
	}

	// 1. Mixed run with fake ergo binary: m-ergo-double delegates to fake ergo;
	// m-go-inc runs on Go worker path; both are killed and reported in ledger order.
	fakeReport := `{
  "ledgers": ["` + ledgerFile + `"],
  "killed": 1,
  "results": [
    {"name": "m-ergo-double", "file": "epkg/lib.ergo", "decl": "Double", "status": "killed", "killer": "epkg:law:DoubleLaw", "runner": "eval", "durationMs": 5}
  ]
}`
	fakeErgo := writeFakeErgoScript(t, t.TempDir(), fakeReport, 0)
	jsonOut := filepath.Join(t.TempDir(), "mixed.json")
	var out, errBuf bytes.Buffer
	code := run([]string{"-root", root, "-fresh", "-resume=false", "-workers", "1", "-tests", "1", "-ergo", fakeErgo, "-json", jsonOut, ledgerFile}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("mixed run exit %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errBuf.String())
	}
	raw, err := os.ReadFile(jsonOut)
	if err != nil {
		t.Fatal(err)
	}
	var results []Result
	dec := json.NewDecoder(bytes.NewReader(raw))
	for dec.More() {
		var r Result
		if err := dec.Decode(&r); err != nil {
			t.Fatal(err)
		}
		results = append(results, r)
	}
	if len(results) != 2 || results[0].Name != "m-go-inc" || results[0].Status != Killed || results[1].Name != "m-ergo-double" || results[1].Status != Killed {
		t.Fatalf("unexpected mixed ledger results: %+v", results)
	}

	// 2. When ergo is absent (clean PATH with go + git only, no bin/ergo), Go-only selection still runs cleanly via Go worker path.
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	cleanBin := t.TempDir()
	for _, target := range []string{goBinaryPath(t), gitBin} {
		if err := os.Symlink(target, filepath.Join(cleanBin, filepath.Base(target))); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", cleanBin)
	var fbOut, fbErr bytes.Buffer
	fbCode := run([]string{"-root", root, "-fresh", "-resume=false", "-workers", "1", "-tests", "1", "-run", "^m-go-inc$", ledgerFile}, &fbOut, &fbErr)
	if fbCode != 0 {
		t.Fatalf("fallback run without ergo exit %d\nstdout:\n%s\nstderr:\n%s", fbCode, fbOut.String(), fbErr.String())
	}
	if strings.Contains(fbErr.String(), "handoff: delegating") {
		t.Fatalf("expected no delegation when ergo absent, stderr:\n%s", fbErr.String())
	}
}

func goBinaryPath(t *testing.T) string {
	t.Helper()
	p, err := os.Executable()
	_ = p
	_ = err
	if goroot := runtime.GOROOT(); goroot != "" {
		return filepath.Join(goroot, "bin", "go")
	}
	return "/usr/bin/go"
}

func TestNormalizeDelegatedKillerUnderContext(t *testing.T) {
	known := []string{
		"modules/orchestrate/memory:law:OrchMemoryPaths.PathsAgree",
		"modules/orchestrate/memory:law:orchestrate.Api.ApiQueueRoundTrips",
	}
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{
			raw:  "modules/orchestrate/memory:law OrchMemoryPaths.PathsAgree under onOrchMemoryPaths",
			want: "modules/orchestrate/memory:law:OrchMemoryPaths.PathsAgree",
		},
		{
			raw:  "modules/orchestrate/memory:law orchestrate.Api.ApiQueueRoundTrips under overMemoryApi",
			want: "modules/orchestrate/memory:law:orchestrate.Api.ApiQueueRoundTrips",
		},
		{
			raw:  "modules/orchestrate/sqlite:law PersistenceChecks.ConcurrentMultiWriterWAL for livePersistence",
			want: "modules/orchestrate/sqlite:law:PersistenceChecks.ConcurrentMultiWriterWAL",
		},
	} {
		if got := normalizeDelegatedKiller(tc.raw, known); got != tc.want {
			t.Errorf("normalizeDelegatedKiller(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}
