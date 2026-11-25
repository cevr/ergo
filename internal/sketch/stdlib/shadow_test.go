package stdlib_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/ergobuild"
	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
)

// TestStdShadowLawsRun runs the privacy-preserving shadow/differential laws of
// std testing at check time in process and records every row via lawledger.Run.
func TestStdShadowLawsRun(t *testing.T) {
	rows, err := lawledger.Read()
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := check.Std.Import("testing")
	if err != nil {
		t.Fatalf("import testing: %v", err)
	}
	b := eval.DefaultBudget()
	b.Steps = 50_000_000
	b.Bytes = 1 << 30
	rep, err := eval.Run(pkg, b)
	if err != nil {
		t.Fatalf("evaluate testing: %v", err)
	}
	results := map[string]eval.Result{}
	for _, r := range rep.Results {
		results["testing "+strings.TrimPrefix(r.Name, "law ")] = r
	}
	laws := map[string]lawledger.Law{}
	for _, row := range rows {
		if row.Runner != t.Name() {
			continue
		}
		r, ok := results[row.Key()]
		statement, _ := lawledger.StdStatement(row.Key())
		laws[row.Key()] = lawledger.Law{Statement: statement, Check: func(t *testing.T) {
			switch {
			case !ok:
				t.Errorf("std %s has no law %s at check time", row.Pkg, row.Law)
			case r.Status != eval.Passed || (r.Evidence != "tested" && r.Evidence != "proved"):
				t.Errorf("law %s: %s (%s, evidence=%q) after %d cases, seed %d, case %d: %+v", row.Key(), r.Status, r.Reason, r.Evidence, r.Cases, r.Seed, r.Case, r.Arguments)
			}
		}}
	}
	lawledger.Run(t, laws)
}

const shadowNativeTestSrc = `import option
import strings
import testing

type migrationReq {
	model  string,
	prompt string,
	cache  bool,
}

func runBaseline(req migrationReq, scriptedReply string, log testing.CheckpointLog) string {
	let wireReq = "model=" + req.model + ";cache=" + (if req.cache { "on" } else { "off" }) + ";prompt=" + req.prompt
	testing.RecordText(log, "input", 0, wireReq)
	"reply:" + scriptedReply + ";model=" + req.model
}

func runCandidate(req migrationReq, scriptedReply string, bugInInput bool, bugInOutput bool, log testing.CheckpointLog) string {
	let wireReq = if bugInInput {
		"model=" + req.model + ";cache=off;prompt=" + req.prompt
	} else {
		"model=" + req.model + ";cache=" + (if req.cache { "on" } else { "off" }) + ";prompt=" + req.prompt
	}
	testing.RecordText(log, "input", 0, wireReq)
	if bugInOutput {
		"reply:" + scriptedReply + ";model=wrong"
	} else {
		"reply:" + scriptedReply + ";model=" + req.model
	}
}

test TwoStageShadowMigrationNative {
	let scripted = "canned-downstream-llm-answer"
	let req = migrationReq{model: "claude", prompt: "SECRET_USER_PROMPT_999", cache: true}
	let tags = []string{"prompt_caching", "tools"}

	let rInputDiff = testing.Differential(
		req,
		tags,
		func(r migrationReq, log testing.CheckpointLog) string { runBaseline(r, scripted, log) },
		func(r migrationReq, log testing.CheckpointLog) string { runCandidate(r, scripted, true, true, log) },
		func(out string) []byte { out.Bytes() },
	)
	assert !rInputDiff.Matched
	match rInputDiff.FirstDiff {
	case option.Some[testing.DiffFingerprint] as fp:
		assert fp.Value.Step == "input"
		assert fp.Value.Piece == 0
		assert fp.Value.FirstDiffOffset == 20
		let k = testing.FingerprintKey(fp.Value)
		assert !strings.Contains(k, "SECRET_USER_PROMPT_999")
	case option.None:
		assert false
	}

	let rOutputDiff = testing.RunShadow(
		req,
		tags,
		func(r migrationReq, log testing.CheckpointLog) string { runBaseline(r, scripted, log) },
		func(r migrationReq, log testing.CheckpointLog) string { runCandidate(r, scripted, false, true, log) },
		func(out string) []byte { out.Bytes() },
	)
	assert !rOutputDiff.Matched
	match rOutputDiff.FirstDiff {
	case option.Some[testing.DiffFingerprint] as fp:
		assert fp.Value.Step == "output"
		assert fp.Value.Piece == 0
	case option.None:
		assert false
	}

	let rClean = testing.Differential(
		req,
		tags,
		func(r migrationReq, log testing.CheckpointLog) string { runBaseline(r, scripted, log) },
		func(r migrationReq, log testing.CheckpointLog) string { runCandidate(r, scripted, false, false, log) },
		func(out string) []byte { out.Bytes() },
	)
	assert rClean.Matched
	assert rClean.BaselineOut == rClean.CandidateOut

	let fp1 = match rInputDiff.FirstDiff {
	case option.Some[testing.DiffFingerprint] as fp:
		fp.Value
	case option.None:
		testing.DiffFingerprint{Step: "", Piece: 0, FirstDiffOffset: -1, BaselineLen: 0, CandidateLen: 0, Tags: []string{}}
	}
	let fp2 = match rOutputDiff.FirstDiff {
	case option.Some[testing.DiffFingerprint] as fp:
		fp.Value
	case option.None:
		testing.DiffFingerprint{Step: "", Piece: 0, FirstDiffOffset: -1, BaselineLen: 0, CandidateLen: 0, Tags: []string{}}
	}
	let buckets = testing.GroupByFingerprint([]testing.DiffFingerprint{fp1, fp2, fp1})
	assert buckets.Len() == 2
	assert buckets[0].Count == 2 && buckets[0].Sample.Step == "input"
	assert buckets[1].Count == 1 && buckets[1].Sample.Step == "output"

	let deltas = testing.CohortCompare([]testing.DiffFingerprint{fp1, fp2}, []testing.DiffFingerprint{fp2}, []string{"prompt_caching", "tools"})
	assert deltas.Len() == 2
	let mut sawEliminated = false
	for d in deltas {
		if d.FingerprintKey == testing.FingerprintKey(fp1) {
			assert d.BeforeCount == 1 && d.AfterCount == 0 && d.Eliminated
			sawEliminated = true
		}
	}
	assert sawEliminated
}
`

// TestStdShadowNativeAndTwoStageMigration executes a two-stage checkpointed
// differential migration test with ERGO_FALLBACK=off using `ergo test --native`,
// proving shadow primitives lower cleanly to native code without fallback.
func TestStdShadowNativeAndTwoStageMigration(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds ergo binary")
	}
	root := moduleRootDir(t)
	tmp := t.TempDir()
	ergoBin := filepath.Join(tmp, "bin", "ergo")
	if err := os.MkdirAll(filepath.Dir(ergoBin), 0o755); err != nil {
		t.Fatal(err)
	}
	build := ergobuild.Command(ergoBin, "./cmd/ergo", "-buildvcs=false")
	build.Dir = root
	if out, err := runBound(build); err != nil {
		t.Fatalf("build ergo: %v\n%s", err, out)
	}

	modDir := filepath.Join(tmp, "shadowmod")
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "ergo.mod"), []byte("module shadowmod\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "shadow_native_test.ergo"), []byte(shadowNativeTestSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(ergoBin, "test", "--native", "--count", "1", ".")
	cmd.Dir = modDir
	cmd.Env = append(os.Environ(), "ERGO_FALLBACK=off")
	out, err := runBound(cmd)
	if err != nil {
		t.Fatalf("ergo test --native failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "ok TwoStageShadowMigrationNative") {
		t.Fatalf("expected ok TwoStageShadowMigrationNative in output:\n%s", out)
	}
}
