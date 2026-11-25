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

// TestStdLLMScriptedLawsRun runs the deterministic ScriptedModel laws of
// std llm/memory at check time in process and records every row via lawledger.Run.
func TestStdLLMScriptedLawsRun(t *testing.T) {
	rows, err := lawledger.Read()
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := check.Std.Import("llm/memory")
	if err != nil {
		t.Fatalf("import llm/memory: %v", err)
	}
	b := eval.DefaultBudget()
	b.Steps = 50_000_000
	b.Bytes = 1 << 30
	rep, err := eval.Run(pkg, b)
	if err != nil {
		t.Fatalf("evaluate llm/memory: %v", err)
	}
	results := map[string]eval.Result{}
	for _, r := range rep.Results {
		results["llm/memory "+strings.TrimPrefix(r.Name, "law ")] = r
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

const twoSidedDifferentialSrc = `import llm
import llm/memory
import option
import strings
import testing

type promptTask {
	system string,
	user   string,
}

func renderPrompt(t promptTask, escapeQuotes bool) string {
	let body = if escapeQuotes { strings.ReplaceAll(t.user, "\"", "\\\"") } else { t.user }
	"sys=" + t.system + ";user=" + body
}

func runAgentTurn(t promptTask, escapeQuotes bool, dropTool bool, model memory.ScriptedModel, log testing.CheckpointLog) string {
	let rendered = renderPrompt(t, escapeQuotes)
	testing.RecordText(log, "input", 0, rendered)
	let req = llm.Request{
		System:    t.system,
		Messages:  []llm.Message{llm.User{Text: rendered}},
		Tools:     []llm.ToolSpec{},
		Choice:    llm.Auto,
		Format:    llm.Prose,
		MaxTokens: 0,
	}
	let turn = model.Generate(req) catch {
	case llm.RateLimited:
		return "ERR:rate_limited"
	case _:
		return "ERR:other"
	}
	let mut out = "text=" + turn.Text
	if !dropTool && turn.Calls.Len() > 0 {
		out = out + ";tool=" + turn.Calls[0].Name + ":" + turn.Calls[0].Arguments
	}
	out
}

test TwoSidedDifferentialInnerLoop {
	let u = llm.Usage{Input: 12, Output: 8, CacheRead: 0, CacheWrite: 0}
	let script = memory.NewScriptedModel([]memory.ScriptStep{
		memory.ScriptReply{
			Text:      "checking weather",
			Reasoning: "",
			ToolCalls: []llm.Call{llm.Call{Id: "call-1", Name: "get_weather", Arguments: "{\"city\":\"NYC\"}"}},
			Stop:      llm.ToolUse,
			Usage:     u,
		},
		memory.ScriptFault{Error: llm.RateLimited{After: option.Some[int]{Value: 100}}},
	})
	let task = promptTask{system: "assistant", user: "say \"SECRET_CANARY_42\" cleanly"}
	let tags = []string{"prompt_caching", "tools"}

	let baseIn = memory.ForkScript(script)
	let candIn = memory.ForkScript(script)
	let inputRes = testing.Differential(
		task,
		tags,
		func(inTask promptTask, log testing.CheckpointLog) string { runAgentTurn(inTask, true, false, baseIn, log) },
		func(inTask promptTask, log testing.CheckpointLog) string { runAgentTurn(inTask, false, false, candIn, log) },
		func(s string) []byte { s.Bytes() },
	)
	assert !inputRes.Matched
	assert memory.CallCount(baseIn) == 1 && memory.CallCount(candIn) == 1
	let fpInput = match inputRes.FirstDiff {
	case option.Some[testing.DiffFingerprint] as fp:
		assert fp.Value.Step == "input"
		assert !strings.Contains(testing.FingerprintKey(fp.Value), "SECRET_CANARY_42")
		fp.Value
	case option.None:
		assert false
		testing.DiffFingerprint{Step: "", Piece: 0, FirstDiffOffset: -1, BaselineLen: 0, CandidateLen: 0, Tags: []string{}}
	}

	let baseOut = memory.ForkScript(script)
	let candOut = memory.ForkScript(script)
	let outputRes = testing.Differential(
		task,
		tags,
		func(inTask promptTask, log testing.CheckpointLog) string { runAgentTurn(inTask, true, false, baseOut, log) },
		func(inTask promptTask, log testing.CheckpointLog) string { runAgentTurn(inTask, true, true, candOut, log) },
		func(s string) []byte { s.Bytes() },
	)
	assert !outputRes.Matched
	let fpOutput = match outputRes.FirstDiff {
	case option.Some[testing.DiffFingerprint] as fp:
		assert fp.Value.Step == "output"
		fp.Value
	case option.None:
		assert false
		testing.DiffFingerprint{Step: "", Piece: 0, FirstDiffOffset: -1, BaselineLen: 0, CandidateLen: 0, Tags: []string{}}
	}

	let faultRes = testing.Differential(
		task,
		tags,
		func(inTask promptTask, log testing.CheckpointLog) string { runAgentTurn(inTask, true, false, baseOut, log) },
		func(inTask promptTask, log testing.CheckpointLog) string { runAgentTurn(inTask, true, false, candOut, log) },
		func(s string) []byte { s.Bytes() },
	)
	assert faultRes.Matched && faultRes.BaselineOut == "ERR:rate_limited" && faultRes.CandidateOut == "ERR:rate_limited"
	assert memory.Remaining(baseOut) == 0 && memory.Remaining(candOut) == 0

	let buckets = testing.GroupByFingerprint([]testing.DiffFingerprint{fpInput, fpOutput, fpInput})
	assert buckets.Len() == 2
	assert buckets[0].Count == 2 && buckets[0].Sample.Step == "input"
	assert buckets[1].Count == 1 && buckets[1].Sample.Step == "output"

	let deltas = testing.CohortCompare([]testing.DiffFingerprint{fpInput, fpOutput}, []testing.DiffFingerprint{fpOutput}, []string{"prompt_caching", "tools"})
	assert deltas.Len() == 2
	let mut inputEliminated = false
	for d in deltas {
		if d.FingerprintKey == testing.FingerprintKey(fpInput) {
			assert d.BeforeCount == 1 && d.AfterCount == 0 && d.Eliminated
			inputEliminated = true
		}
	}
	assert inputEliminated
}
`

// TestStdLLMTwoSidedDifferentialLoop compiles and runs a two-sided local
// differential inner loop (input escaping check + output tool-call/fault replay
// via ForkScript) natively with ERGO_FALLBACK=off.
func TestStdLLMTwoSidedDifferentialLoop(t *testing.T) {
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

	modDir := filepath.Join(tmp, "shadowllm")
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "ergo.mod"), []byte("module shadowllm\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "loop_test.ergo"), []byte(twoSidedDifferentialSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(ergoBin, "test", "--native", "--count", "1", ".")
	cmd.Dir = modDir
	cmd.Env = append(os.Environ(), "ERGO_FALLBACK=off")
	out, err := runBound(cmd)
	if err != nil {
		t.Fatalf("ergo test --native failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "ok TwoSidedDifferentialInnerLoop") {
		t.Fatalf("expected ok TwoSidedDifferentialInnerLoop in output:\n%s", out)
	}
}
