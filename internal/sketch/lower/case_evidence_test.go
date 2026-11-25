package lower

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
)

type caseEvReceipt struct {
	Schema int            `json:"schema"`
	Laws   []caseEvEntry  `json:"laws"`
}

type caseEvEntry struct {
	Run            string       `json:"run"`
	Status         string       `json:"status"`
	Cases          int          `json:"cases"`
	Why            string       `json:"why"`
	Counterexample string       `json:"counterexample"`
	Evidence       *caseEvBlock `json:"evidence"`
}

type caseEvBlock struct {
	Tested   int    `json:"tested"`
	Matched  int    `json:"matched"`
	Planned  int    `json:"planned"`
	Scope    string `json:"scope"`
	Summary  string `json:"summary"`
	Complete bool   `json:"complete"`
}

func TestCaseEvidenceDowngradesProofAndReportsReceipt(t *testing.T) {
	needsBuild(t)
	src := `import explore
import sync
import testing

type cmd = inc{} | rst{}
type openErr { Why string }
type driveErr { Why string }

type sess {
	v sync.Ref[int],
	rel sync.Ref[bool],
} implements explore.PathSession[cmd, explore.Observed[int, int, Never], driveErr]

func Read(s sess) explore.Observed[int, int, Never] fails driveErr {
	explore.InitialRead[int]{State: s.v.Get()}
}

func Apply(s sess, c cmd) explore.Observed[int, int, Never] fails driveErr {
	let next = match c {
	case inc:
		let n = s.v.Get() + 1
		s.v.Set(n)
		n
	case rst:
		s.v.Set(0)
		0
	}
	explore.StepResult[int, int, Never]{Step: explore.StepObs[int, int, Never]{
		Kind: explore.DisEmit, State: Some[int]{Value: next}, Outputs: []int{next}, Error: None, Terminal: false,
	}}
}

func Close(s sess) { s.rel.Set(true) }

func model() explore.PathModel[int, cmd, int, int, Never] {
	explore.PathModelOfMachine[int, cmd, int]([]int{0}, []cmd{inc, rst}, func(s int, c cmd) Decision[int, int] {
		match c {
		case inc:
			Emit[int, int]{Next: s + 1, Output: s + 1}
		case rst:
			Emit[int, int]{Next: 0, Output: 0}
		}
	})
}

type Counter {
	Open(start int) explore.PathSession[cmd, explore.Observed[int, int, Never], driveErr] fails openErr,

	law BoundedPaths() {
		let res = explore.CheckPathsWithSink(
			model(),
			explore.ToDepth{Depth: 2},
			explore.PathBounds{MaxStates: 64, MaxEdges: 256, MaxPaths: 256, MaxSteps: 1024, MaxWork: 8192, MaxBytes: 65536},
			func(start int) explore.PathSession[cmd, explore.Observed[int, int, Never], driveErr] fails openErr {
				try Open(start)
			},
			testing.ActiveEvidenceSink(),
		)
		match res.Verdict {
		case explore.CampaignPassed:
			true
		case _:
			false
		}
	},
}

type Good {} implements Counter
func Open(_ Good, start int) explore.PathSession[cmd, explore.Observed[int, int, Never], driveErr] fails openErr {
	sess{v: sync.NewRef(start), rel: sync.NewRef(false)}
}

let onGood = context { Counter: Good }

law UserlandEvidence() {
	testing.RecordCaseEvidence(testing.CaseEvidence{
		Tested: 4, Matched: 4, Planned: 4, Scope: "depth <= 2", Summary: "4 userland cases; depth <= 2", Status: testing.EvidenceComplete,
	})
	true
}

law FiniteControl() { 1 + 1 == 2 }
`
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "case_ev_pass", src)}, true)
	if exes == nil {
		return
	}
	receiptPath := filepath.Join(t.TempDir(), "receipt.json")
	out, stderr, code := runExeArgs(t, exes["case_ev_pass"], "--receipt", receiptPath)
	if code != 0 {
		t.Fatalf("expected pass, got exit %d\nstdout:\n%s\nstderr:\n%s", code, out, stderr)
	}
	if strings.Contains(out, "law Counter.BoundedPaths under onGood (proved:") {
		t.Fatalf("bounded member law promoted to proved:\n%s", out)
	}
	if !strings.Contains(out, "ok law Counter.BoundedPaths under onGood (tested:") {
		t.Fatalf("expected tested verdict for member law, got:\n%s", out)
	}
	if !strings.Contains(out, "ok law UserlandEvidence (tested: 4 userland cases; depth <= 2)") {
		t.Fatalf("expected tested verdict for userland law, got:\n%s", out)
	}
	if !strings.Contains(out, "ok law FiniteControl (proved: 1 case)") {
		t.Fatalf("ordinary finite law lost proved verdict:\n%s", out)
	}
	data, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatalf("read receipt: %v", err)
	}
	var rf caseEvReceipt
	if err := json.Unmarshal(data, &rf); err != nil {
		t.Fatalf("unmarshal receipt: %v\n%s", err, string(data))
	}
	byRun := map[string]caseEvEntry{}
	for _, e := range rf.Laws {
		byRun[e.Run] = e
	}
	e1, ok := byRun["law Counter.BoundedPaths under onGood"]
	if !ok || e1.Status != "tested" || e1.Evidence == nil || e1.Evidence.Tested != 7 || !e1.Evidence.Complete {
		t.Fatalf("bad member law receipt entry: %+v\nraw:\n%s", e1, string(data))
	}
	e2, ok := byRun["law UserlandEvidence"]
	if !ok || e2.Status != "tested" || e2.Evidence == nil || e2.Evidence.Tested != 4 || e2.Evidence.Scope != "depth <= 2" {
		t.Fatalf("bad userland receipt entry: %+v\nraw:\n%s", e2, string(data))
	}
	e3, ok := byRun["law FiniteControl"]
	if !ok || e3.Status != "proved" || e3.Evidence != nil {
		t.Fatalf("bad finite control receipt entry: %+v\nraw:\n%s", e3, string(data))
	}

	evalSrc := `import testing

law UserlandOnly() {
	testing.RecordCaseEvidence(testing.CaseEvidence{
		Tested: 3, Matched: 3, Planned: 3, Scope: "n <= 3", Summary: "3 userland cases; n <= 3", Status: testing.EvidenceComplete,
	})
	true
}

law FiniteOnly() { 2 + 2 == 4 }
`
	pkg, diags := check.CheckSource(evalSrc)
	if len(diags) != 0 {
		t.Fatalf("check evalSrc: %v", diags)
	}
	rep, err := eval.Run(pkg, eval.DefaultBudget())
	if err != nil {
		t.Fatalf("eval.Run: %v", err)
	}
	for _, r := range rep.Results {
		line, ok := eval.VerdictLine(r, "")
		if !ok {
			t.Fatalf("VerdictLine undecided for %+v", r)
		}
		if r.Name == "law UserlandOnly" && line != "ok law UserlandOnly (tested: 3 userland cases; n <= 3)" {
			t.Fatalf("unexpected eval verdict line for UserlandOnly: %q (evidence=%q)", line, r.Evidence)
		}
		if r.Name == "law FiniteOnly" && line != "ok law FiniteOnly (proved: 1 case)" {
			t.Fatalf("unexpected eval verdict line for FiniteOnly: %q", line)
		}
	}
}

func TestCaseEvidenceIncompleteIsNonGreenEvenWhenBodyReturnsTrue(t *testing.T) {
	needsBuild(t)
	src := `import testing

law IncompleteCap() {
	testing.RecordCaseEvidence(testing.CaseEvidence{
		Tested: 0, Matched: 0, Planned: 0, Scope: "depth <= 5", Summary: "0 paths", Status: testing.EvidenceIncomplete{Reason: "path cap reached"},
	})
	true
}
`
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "case_ev_inc", src)}, true)
	if exes == nil {
		return
	}
	receiptPath := filepath.Join(t.TempDir(), "receipt.json")
	out, _, code := runExeArgs(t, exes["case_ev_inc"], "--receipt", receiptPath)
	if code == 0 {
		t.Fatalf("incomplete bounded campaign returned exit 0:\n%s", out)
	}
	if !strings.Contains(out, "incomplete bounded campaign: path cap reached") {
		t.Fatalf("missing incomplete reason in stdout:\n%s", out)
	}
	data, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatalf("read receipt: %v", err)
	}
	var rf caseEvReceipt
	if err := json.Unmarshal(data, &rf); err != nil {
		t.Fatalf("unmarshal receipt: %v", err)
	}
	if len(rf.Laws) != 1 || rf.Laws[0].Status != "incomplete" || rf.Laws[0].Why != "path cap reached" {
		t.Fatalf("expected incomplete status in receipt, got: %+v\nraw:\n%s", rf.Laws, string(data))
	}

	pkg, diags := check.CheckSource(src)
	if len(diags) != 0 {
		t.Fatalf("check incomplete src: %v", diags)
	}
	rep, err := eval.Run(pkg, eval.DefaultBudget())
	if err != nil {
		t.Fatalf("eval.Run: %v", err)
	}
	if len(rep.Results) != 1 || rep.Results[0].Status != eval.Refuted || !strings.Contains(rep.Results[0].Message, "path cap reached") {
		t.Fatalf("eval did not mark incomplete campaign non-green: %+v", rep.Results)
	}
}

func TestCaseEvidenceMismatchDetailsAfterCleanup(t *testing.T) {
	needsBuild(t)
	src := `import explore
import sync
import testing

type cmd = inc{}
type openErr { Why string }
type driveErr { Why string }

type badSess {
	v      sync.Ref[int],
	closed sync.Ref[int],
} implements explore.PathSession[cmd, explore.Observed[int, int, Never], driveErr]

func Read(s badSess) explore.Observed[int, int, Never] fails driveErr {
	explore.InitialRead[int]{State: s.v.Get()}
}

func Apply(s badSess, _ cmd) explore.Observed[int, int, Never] fails driveErr {
	let wrong = s.v.Get() + 99
	s.v.Set(wrong)
	explore.StepResult[int, int, Never]{Step: explore.StepObs[int, int, Never]{
		Kind: explore.DisEmit, State: Some[int]{Value: wrong}, Outputs: []int{wrong}, Error: None, Terminal: false,
	}}
}

func Close(s badSess) {
	s.closed.Set(s.closed.Get() + 1)
}

func model() explore.PathModel[int, cmd, int, int, Never] {
	explore.PathModelOfMachine[int, cmd, int]([]int{0}, []cmd{inc}, func(s int, _ cmd) Decision[int, int] {
		Emit[int, int]{Next: s + 1, Output: s + 1}
	})
}

law MismatchReportsAfterCleanup() {
	let opens = sync.NewRef(0)
	let closes = sync.NewRef(0)
	let res = explore.CheckPathsWithSink(
		model(),
		explore.ToDepth{Depth: 1},
		explore.PathBounds{MaxStates: 16, MaxEdges: 32, MaxPaths: 32, MaxSteps: 64, MaxWork: 1024, MaxBytes: 16384},
		func(start int) explore.PathSession[cmd, explore.Observed[int, int, Never], driveErr] fails openErr {
			opens.Set(opens.Get() + 1)
			badSess{v: sync.NewRef(start), closed: closes}
		},
		testing.ActiveEvidenceSink(),
	)
	assert opens.Get() > 0 && opens.Get() == closes.Get()
	match res.Verdict {
	case explore.CampaignPassed:
		true
	case explore.CampaignRefuted as r:
		match r.Failure {
		case explore.StepMismatch as m:
			assert m.Step == 0
			assert m.Field == explore.DiffState
			assert m.Actual == Some[explore.StepObs[int, int, Never]]{Value: m.Expected}
			false
		case _:
			false
		}
	case explore.CampaignIncomplete:
		false
	}
}
`
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "case_ev_fail", src)}, true)
	if exes == nil {
		return
	}
	receiptPath := filepath.Join(t.TempDir(), "receipt.json")
	out, _, code := runExeArgs(t, exes["case_ev_fail"], "--receipt", receiptPath)
	if code == 0 {
		t.Fatalf("mismatching campaign returned exit 0:\n%s", out)
	}
	if !strings.Contains(out, "FAIL law MismatchReportsAfterCleanup") {
		t.Fatalf("expected FAIL line for mismatching campaign:\n%s", out)
	}
	data, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatalf("read receipt: %v", err)
	}
	var rf caseEvReceipt
	if err := json.Unmarshal(data, &rf); err != nil {
		t.Fatalf("unmarshal receipt: %v\n%s", err, string(data))
	}
	if len(rf.Laws) != 1 || rf.Laws[0].Status != "refuted" || rf.Laws[0].Evidence == nil || rf.Laws[0].Evidence.Tested == 0 {
		t.Fatalf("expected refuted receipt with evidence after cleanup, got: %+v\nraw:\n%s", rf.Laws, string(data))
	}
}
