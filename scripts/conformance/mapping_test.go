package main

// Mapping admission controls; behavior evidence runs through existing Go tests.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func committedMapping(t *testing.T) map[string]any {
	t.Helper()
	value, err := readMapping(filepath.Join(repoRoot(t), mappingRelative))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func deepCopy(value any) any {
	switch v := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(v))
		for key, item := range v {
			result[key] = deepCopy(item)
		}
		return result
	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			result[i] = deepCopy(item)
		}
		return result
	}
	return value
}

func copyMapping(value map[string]any) map[string]any {
	return deepCopy(value).(map[string]any)
}

func mappingCase(value map[string]any, index int) map[string]any {
	return value["cases"].([]any)[index].(map[string]any)
}

func rejectMapping(t *testing.T, value map[string]any) {
	t.Helper()
	rejectMappingAt(t, value, repoRoot(t))
}

func rejectMappingAt(t *testing.T, value map[string]any, root string) {
	t.Helper()
	if tests, _, err := validateMapping(value, root); err == nil {
		t.Fatalf("mapping accepted with evidence %v", tests)
	}
}

func evidence(file, test string) any {
	return map[string]any{"file": file, "test": test, "targets": []any{"go"}}
}

// pendingMapping is a copy of the committed mapping in which every case
// that claims evidence is pending with none, so a fixture repository that
// lacks the real evidence tests can hold it.
func pendingMapping(t *testing.T) map[string]any {
	t.Helper()
	value := copyMapping(committedMapping(t))
	for _, item := range value["cases"].([]any) {
		if c := item.(map[string]any); len(c["evidence"].([]any)) > 0 {
			c["status"], c["evidence"] = "pending", []any{}
		}
	}
	return value
}

// caseID is the id of the mapping's case at index.
func caseID(value map[string]any, index int) string {
	return mappingCase(value, index)["id"].(string)
}

// runs is a subtest call that names case id.
func runs(id string) string {
	return "\tt.Run(" + strconv.Quote(id) + ", func(t *testing.T) {})\n"
}

// evidenceFixture is the pending mapping with its first two cases backed
// by evidence tests in two packages of a temporary Git repository, every
// file tracked. One test is named twice, by both cases, and runs a subtest
// for each.
func evidenceFixture(t *testing.T) (map[string]any, string) {
	t.Helper()
	root := t.TempDir()
	value := pendingMapping(t)
	first, second := caseID(value, 0), caseID(value, 1)
	writeFiles(t, root, map[string]string{
		"alpha/one_test.go":   "package alpha\n\nimport \"testing\"\n\nfunc TestOne(t *testing.T) {\n" + runs(first) + runs(second) + "}\n\nfunc TestTwo(t *testing.T) {\n" + runs(first) + "}\n",
		"alpha/one.go":        "package alpha\n\nimport \"testing\"\n\nfunc TestOne(t *testing.T) {\n" + runs(first) + "}\n",
		"beta/deep/x_test.go": "package deep\n\nimport \"testing\"\n\nfunc TestThree(t *testing.T) {\n" + runs(second) + "}\n",
	})
	fixtureGit(t, root, "init", "-q")
	fixtureGit(t, root, "add", ".")
	mappingCase(value, 0)["status"] = "covered"
	mappingCase(value, 0)["evidence"] = []any{evidence("alpha/one_test.go", "TestTwo"), evidence("alpha/one_test.go", "TestOne")}
	mappingCase(value, 1)["status"] = "difference"
	mappingCase(value, 1)["evidence"] = []any{evidence("beta/deep/x_test.go", "TestThree"), evidence("alpha/one_test.go", "TestOne")}
	return value, root
}

// Evidence must be a test file Git tracks: an untracked test with a real
// declaration is refused until it is added, and so is a root outside Git.
func TestUntrackedEvidenceFails(t *testing.T) {
	t.Parallel()
	value, root := evidenceFixture(t)
	writeFiles(t, root, map[string]string{"alpha/extra_test.go": "package alpha\n\nimport \"testing\"\n\nfunc TestExtra(t *testing.T) {\n" + runs(caseID(value, 0)) + "}\n"})
	mappingCase(value, 0)["evidence"] = append(mappingCase(value, 0)["evidence"].([]any), evidence("alpha/extra_test.go", "TestExtra"))
	if _, _, err := validateMapping(value, root); err == nil || !strings.Contains(err.Error(), "evidence file is not tracked") {
		t.Fatalf("untracked evidence: err = %v, want the not-tracked refusal", err)
	}
	fixtureGit(t, root, "add", "alpha/extra_test.go")
	if _, _, err := validateMapping(value, root); err != nil {
		t.Fatalf("tracked evidence refused: %v", err)
	}
	outside := t.TempDir()
	backed := pendingMapping(t)
	writeFiles(t, outside, map[string]string{"alpha/one_test.go": "package alpha\n\nimport \"testing\"\n\nfunc TestOne(t *testing.T) {\n" + runs(caseID(backed, 0)) + "}\n"})
	mappingCase(backed, 0)["status"] = "covered"
	mappingCase(backed, 0)["evidence"] = []any{evidence("alpha/one_test.go", "TestOne")}
	rejectMappingAt(t, backed, outside)
}

// committedClaims are the cases the committed mapping proves by a v1 test,
// each by its test's subtest of the case's name (fu-conf-pending: the rows
// std-align S1c and S1d added; batch 10: S3a's filepath case, and the
// round-2 rows of S2a-S2c and S3c). The earlier compiler's evidence tests
// were deleted with it (#205), so its cases stay pending.
var committedClaims = []string{
	"./internal/sketch/lower TestExecRuns effect.child-process.kills-pipeline",
	"./internal/sketch/lower TestFilepathRuns effect.path.join-posix",
	"./internal/sketch/lower TestFlagMatchesGo effect.cli.options-interleaved-with-operands",
	"./internal/sketch/lower TestQueuePollRuns effect.queue.poll-option",
	"./internal/sketch/lower TestRandEffectCases effect.random.choice-empty",
	"./internal/sketch/lower TestRandEffectCases effect.random.choice-seed",
	"./internal/sketch/lower TestRandEffectCases effect.random.next-boolean-seed",
	"./internal/sketch/lower TestRandEffectCases effect.random.next-in-unit",
	"./internal/sketch/lower TestRandEffectCases effect.random.shuffle-copy",
	"./internal/sketch/lower TestRandEffectCases effect.random.shuffle-seed",
	"./internal/sketch/lower TestStdSlicesEffectCases effect.array.filter",
	"./internal/sketch/lower TestStdSlicesEffectCases effect.array.map",
	"./internal/sketch/lower TestStdSlicesEffectCases effect.array.reduce",
	"./internal/sketch/lower TestStdSlicesEffectCases effect.array.sort",
	"./internal/sketch/lower TestStdTextRuns2 effect.number.parse",
	"./internal/sketch/lower TestStdTextRuns2 effect.string.lines-with-separators",
	"./internal/sketch/lower TestStdTextRuns2 effect.string.replace-all",
	"./internal/sketch/lower TestTimeEffectCases effect.test-clock.admitted-fork",
	"./internal/sketch/lower TestTimeEffectCases effect.test-clock.delays-until-adjusted",
	"./internal/sketch/lower TestTimeEffectCases effect.test-clock.multiple-sleeps",
}

// The committed mapping is valid at the repository, and its claims are
// committedClaims: each a covered or difference case, each test's body
// naming the case's subtest.
func TestCommittedMappingClaims(t *testing.T) {
	t.Parallel()
	value := committedMapping(t)
	_, claims, err := validateMapping(value, repoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, claim := range claims {
		got = append(got, claim.test.pkg+" "+claim.test.name+" "+claim.caseID)
	}
	if !slices.Equal(got, committedClaims) {
		t.Fatalf("claims = %q, want %q", got, committedClaims)
	}
}

// An evidence test proves its case only in a subtest named after the case:
// a test whose body runs no subtest of that name is refused, also when
// the name is another case's, when the call is in another function of the
// file, or when the id is only in a comment or a log line.
func TestEvidenceTestMustNameItsCase(t *testing.T) {
	t.Parallel()
	value, root := evidenceFixture(t)
	first, second := caseID(value, 0), caseID(value, 1)
	for name, body := range map[string]string{
		"none":    "func TestCase(t *testing.T) {\n}\n",
		"other":   "func TestCase(t *testing.T) {\n" + runs(second) + "}\n",
		"outside": "func TestElse(t *testing.T) {\n" + runs(first) + "}\n\nfunc TestCase(t *testing.T) {\n}\n",
		"text":    "func TestCase(t *testing.T) {\n\t// t.Run(" + strconv.Quote(first) + "\n\tt.Log(" + strconv.Quote(first) + ")\n}\n",
	} {
		// Serial: the subtests share the fixture's Git index.
		t.Run(name, func(t *testing.T) {
			writeFiles(t, root, map[string]string{"alpha/case_" + name + "_test.go": "package alpha\n\nimport \"testing\"\n\n" + body})
			fixtureGit(t, root, "add", "alpha/case_"+name+"_test.go")
			bad := copyMapping(value)
			mappingCase(bad, 0)["evidence"] = []any{evidence("alpha/case_"+name+"_test.go", "TestCase")}
			if _, _, err := validateMapping(bad, root); err == nil || !strings.Contains(err.Error(), "runs no subtest") {
				t.Fatalf("err = %v, want the no-subtest refusal", err)
			}
		})
	}
	_, claims, err := validateMapping(value, root)
	if err != nil {
		t.Fatal(err)
	}
	want := []evidenceClaim{
		{evidenceTest{"./alpha", "TestOne"}, first}, {evidenceTest{"./alpha", "TestOne"}, second},
		{evidenceTest{"./alpha", "TestTwo"}, first}, {evidenceTest{"./beta/deep", "TestThree"}, second},
	}
	slices.SortFunc(want, func(a, b evidenceClaim) int {
		if a.test != b.test {
			return strings.Compare(a.test.pkg+" "+a.test.name, b.test.pkg+" "+b.test.name)
		}
		return strings.Compare(a.caseID, b.caseID)
	})
	if !slices.Equal(claims, want) {
		t.Fatalf("claims = %v, want %v", claims, want)
	}
}

// checkClaimsRan passes a package's claims only when go test -json reports
// each claim's subtest as passed: a skipped, failed or absent subtest, or
// one of another test, fails; claims of other packages are not its.
func TestClaimsMustRunAndPass(t *testing.T) {
	t.Parallel()
	claim := evidenceClaim{evidenceTest{"./alpha", "TestOne"}, "effect.a"}
	elsewhere := evidenceClaim{evidenceTest{"./beta", "TestOne"}, "effect.b"}
	event := func(action, test string) string {
		return `{"Action":"` + action + `","Test":"` + test + `","Output":"` + action + ` ` + test + `\n"}` + "\n"
	}
	for _, c := range []struct {
		name, events string
		ok           bool
	}{
		{"pass", event("run", "TestOne/effect.a") + event("output", "TestOne/effect.a") + event("pass", "TestOne/effect.a") + event("pass", "TestOne"), true},
		{"skip", event("run", "TestOne/effect.a") + event("skip", "TestOne/effect.a") + event("pass", "TestOne"), false},
		{"fail", event("run", "TestOne/effect.a") + event("fail", "TestOne/effect.a") + event("fail", "TestOne"), false},
		{"absent", event("run", "TestOne") + event("pass", "TestOne"), false},
		{"another test", event("pass", "TestTwo/effect.a") + event("pass", "TestOne"), false},
		{"not json", "PASS\n", false},
	} {
		var out strings.Builder
		err := checkClaimsRan(strings.NewReader(c.events), "./alpha", []evidenceClaim{claim, elsewhere}, &out)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok %t", c.name, err, c.ok)
		}
		if c.name == "pass" && out.String() != "output TestOne/effect.a\n" {
			t.Errorf("output text = %q", out.String())
		}
	}
}

// check --run runs each evidence test with go test -json in a real module
// and fails a claim whose subtest the test names but never runs (it is in
// a branch not taken) or skips; with every claim run and passed, it
// passes.
func TestCheckRunNeedsTheCaseSubtestToPass(t *testing.T) {
	t.Parallel()
	value := pendingMapping(t)
	first := caseID(value, 0)
	for _, c := range []struct {
		name, body string
		ok         bool
	}{
		{"runs", "\tt.Run(" + strconv.Quote(first) + ", func(t *testing.T) {})\n", true},
		{"never", "\tif len(t.Name()) == 0 {\n\t\tt.Run(" + strconv.Quote(first) + ", func(t *testing.T) {})\n\t}\n", false},
		{"skips", "\tt.Run(" + strconv.Quote(first) + ", func(t *testing.T) { t.Skip(\"later\") })\n", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeFiles(t, root, map[string]string{
				"go.mod":                 "module fixture\n\ngo 1.22\n",
				"alpha/evidence_test.go": "package alpha\n\nimport \"testing\"\n\nfunc TestEvidence(t *testing.T) {\n" + c.body + "}\n",
			})
			fixtureGit(t, root, "init", "-q")
			fixtureGit(t, root, "add", ".")
			backed := copyMapping(value)
			mappingCase(backed, 0)["status"] = "covered"
			mappingCase(backed, 0)["evidence"] = []any{evidence("alpha/evidence_test.go", "TestEvidence")}
			encoded, err := json.Marshal(backed)
			if err != nil {
				t.Fatal(err)
			}
			mapping := filepath.Join(t.TempDir(), "effect-cases.json")
			if err := os.WriteFile(mapping, encoded, 0o644); err != nil {
				t.Fatal(err)
			}
			code, stdout, stderr := runCLI(t, root, hermeticGitEnv("GOFLAGS=", "GOWORK=off"), "check", "--mapping", mapping, "--run")
			if (code == 0) != c.ok {
				t.Fatalf("exit %d, want ok %t\nstdout %s\nstderr %s", code, c.ok, stdout, stderr)
			}
			if !c.ok {
				assertContains(t, stderr, "evidence subtests of ./alpha did not run and pass: TestEvidence/"+first)
			}
		})
	}
}

// Evidence may be any Go test of any package; the selection is sorted by
// package and name, each test once, and --run runs one go test per package.
func TestEvidenceInAnyPackageRunsPerPackage(t *testing.T) {
	t.Parallel()
	value, root := evidenceFixture(t)
	tests, _, err := validateMapping(value, root)
	if err != nil {
		t.Fatal(err)
	}
	want := []evidenceTest{{"./alpha", "TestOne"}, {"./alpha", "TestTwo"}, {"./beta/deep", "TestThree"}}
	if !slices.Equal(tests, want) {
		t.Fatalf("evidence tests = %v, want %v", tests, want)
	}
	commands := evidenceCommands(tests)
	wantCommands := [][]string{
		{"go", "test", "-json", "./alpha", "-run", "^(TestOne|TestTwo)$", "-count=1"},
		{"go", "test", "-json", "./beta/deep", "-run", "^(TestThree)$", "-count=1"},
	}
	if !slices.EqualFunc(commands, wantCommands, slices.Equal[[]string]) {
		t.Fatalf("commands = %v, want %v", commands, wantCommands)
	}
	if commands := evidenceCommands(nil); len(commands) != 0 {
		t.Fatalf("no evidence gave commands %v", commands)
	}
	if got := packageOf("top_test.go"); got != "." {
		t.Fatalf("a top-level test file is in package %q", got)
	}
}

func TestPinAndDuplicateIDsFail(t *testing.T) {
	t.Parallel()
	value := committedMapping(t)
	bad := copyMapping(value)
	bad["sourceCommit"] = "0000000000000000000000000000000000000000"
	rejectMapping(t, bad)
	bad = copyMapping(value)
	mappingCase(bad, 1)["id"] = mappingCase(bad, 0)["id"]
	rejectMapping(t, bad)
}

func TestMissingChangedOrDuplicateUpstreamCaseFails(t *testing.T) {
	t.Parallel()
	value := committedMapping(t)
	for _, change := range []struct {
		field string
		value any
	}{
		{"file", "missing.test.ts"}, {"file", "packages/effect/src/Effect.ts"}, {"file", "../outside"}, {"file", ""},
		{"line", json.Number("0")}, {"line", json.Number("1.5")}, {"line", "2733"}, {"label", ""}, {"label", " \t"}, {"label", nil},
	} {
		t.Run(change.field+"="+pyStr(change.value), func(t *testing.T) {
			t.Parallel()
			bad := copyMapping(value)
			mappingCase(bad, 0)["upstream"].(map[string]any)[change.field] = change.value
			rejectMapping(t, bad)
		})
	}
	bad := copyMapping(value)
	mappingCase(bad, 1)["upstream"] = mappingCase(bad, 0)["upstream"]
	rejectMapping(t, bad)
}

func TestInvalidNativeEvidenceTargetsFail(t *testing.T) {
	t.Parallel()
	value, root := evidenceFixture(t)
	for _, change := range []struct {
		field string
		value any
	}{
		{"file", "alpha/missing_test.go"}, {"file", "alpha/one.go"}, {"file", "../outside"}, {"test", "TestImaginaryConformance"}, {"test", "TestTwo2"},
		{"targets", []any{"go", "js"}}, {"targets", []any{"js"}}, {"targets", []any{}},
	} {
		t.Run(change.field+"="+pyStr(change.value), func(t *testing.T) {
			t.Parallel()
			bad := copyMapping(value)
			mappingCase(bad, 0)["evidence"].([]any)[0].(map[string]any)[change.field] = change.value
			rejectMappingAt(t, bad, root)
		})
	}
}

func TestReferenceOnlyCasesCannotBeCalledCoveredWithoutEvidence(t *testing.T) {
	t.Parallel()
	value := committedMapping(t)
	for _, status := range []string{"covered", "difference", "passing"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			bad := copyMapping(value)
			// Case 5 is a pending case with no evidence (cases 2-4 are
			// covered since batch 10's S2b rows).
			if ev, _ := mappingCase(bad, 5)["evidence"].([]any); len(ev) != 0 {
				t.Fatalf("case 5 %v has evidence; pick a case with none", mappingCase(bad, 5)["id"])
			}
			mappingCase(bad, 5)["status"] = status
			rejectMapping(t, bad)
		})
	}
	// A case with evidence cannot be called pending or unsupported.
	backed, root := evidenceFixture(t)
	for _, status := range []string{"pending", "unsupported"} {
		t.Run("evidence-"+status, func(t *testing.T) {
			t.Parallel()
			bad := copyMapping(backed)
			mappingCase(bad, 0)["status"] = status
			rejectMappingAt(t, bad, root)
		})
	}
}

func TestBehaviorLimitsAndClosedSchemaAreRequired(t *testing.T) {
	t.Parallel()
	value := committedMapping(t)
	for _, version := range []any{true, json.Number("1.5"), nil} {
		t.Run("schemaVersion="+pyRepr(version), func(t *testing.T) {
			bad := copyMapping(value)
			if version == nil {
				delete(bad, "schemaVersion")
			} else {
				bad["schemaVersion"] = version
			}
			rejectMapping(t, bad)
		})
	}
	for _, field := range []string{"behavior", "limits"} {
		bad := copyMapping(value)
		mappingCase(bad, 0)[field] = ""
		rejectMapping(t, bad)
	}
	bad := copyMapping(value)
	mappingCase(bad, 0)["passedUpstreamSuite"] = true
	rejectMapping(t, bad)
}

func TestMissingOrUnexecutableGoIsAStructuredCLIRefusal(t *testing.T) {
	t.Parallel()
	write := func(value map[string]any) string {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		mapping := filepath.Join(t.TempDir(), "effect-cases.json")
		if err := os.WriteFile(mapping, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		return mapping
	}
	// A mapping that names no evidence runs nothing with --run and needs
	// no Go (and no git).
	code, stdout, stderr := runCLI(t, repoRoot(t), hermeticGitEnv("PATH="+t.TempDir()), "check", "--mapping", write(pendingMapping(t)), "--run")
	if code != 0 || !strings.Contains(stdout, "; 0 Go evidence tests") {
		t.Fatalf("check --run without evidence: exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}
	// The committed mapping names evidence tests, so it needs Go to run
	// them.
	mapping := filepath.Join(repoRoot(t), mappingRelative)
	// Evidence admission runs git, so PATH keeps git and loses only go.
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	for _, unexecutable := range []bool{false, true} {
		directory := t.TempDir()
		if err := os.Symlink(git, filepath.Join(directory, "git")); err != nil {
			t.Fatal(err)
		}
		if unexecutable {
			if err := os.WriteFile(filepath.Join(directory, "go"), []byte("unexecutable fixture"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		code, _, stderr := runCLI(t, repoRoot(t), hermeticGitEnv("PATH="+directory), "check", "--mapping", mapping, "--run")
		if code == 0 {
			t.Fatalf("check --run succeeded without Go (unexecutable file: %v)", unexecutable)
		}
		assertContains(t, stderr, "effect_conformance: cannot execute Go evidence runner:")
		if strings.Contains(stderr, "panic") || strings.Contains(stderr, "goroutine") {
			t.Fatalf("unstructured failure: %s", stderr)
		}
	}
}

func TestMalformedTrailingDataIsRefusedAtTheMappingBoundary(t *testing.T) {
	t.Parallel()
	original, err := os.ReadFile(filepath.Join(repoRoot(t), mappingRelative))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeJSON(string(original) + " \n\t"); err != nil {
		t.Fatalf("trailing whitespace refused: %v", err)
	}
	for _, suffix := range []string{" garbage", ",", "]", "}", " {}", " 1", `"`, "\x00"} {
		path := filepath.Join(t.TempDir(), "effect-cases.json")
		if err := os.WriteFile(path, append(append([]byte{}, original...), suffix...), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := readMapping(path); err == nil || err.Error() != "mapping is not valid UTF-8 JSON" {
			t.Fatalf("suffix %q: err = %v, want the invalid-JSON refusal", suffix, err)
		}
	}
}
