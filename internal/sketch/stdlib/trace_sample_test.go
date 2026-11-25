package stdlib_test

import (
	"encoding/json"
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

// TestStdTraceSampleLawsRun runs the composable tail-sampling laws of std
// trace at check time in process and records every row via lawledger.Run.
func TestStdTraceSampleLawsRun(t *testing.T) {
	rows, err := lawledger.Read()
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := check.Std.Import("trace")
	if err != nil {
		t.Fatalf("import trace: %v", err)
	}
	b := eval.DefaultBudget()
	b.Steps = 50_000_000
	b.Bytes = 1 << 30
	rep, err := eval.Run(pkg, b)
	if err != nil {
		t.Fatalf("evaluate trace: %v", err)
	}
	results := map[string]eval.Result{}
	for _, r := range rep.Results {
		results["trace "+strings.TrimPrefix(r.Name, "law ")] = r
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

const traceSampleAndLogDriverSrc = `import io
import log
import option
import os
import time
import trace

type fakeProc {
	env Map[string, string],
} implements os.Process

func Args(_ fakeProc) []string { []string{"app"} }

func Getenv(p fakeProc, key string) option.Option[string] { p.env.Get(key) }

func Environ(p fakeProc) Map[string, string] { p.env }

func Getwd(_ fakeProc) string fails os.NoWorkdir { "/" }

type opFail { reason string }

func main() needs io.Console {
	let clk = time.NewManual(1700000000000000000)
	let gen = trace.NewIDGenerator(uint64(99))
	let svc = trace.ServiceMeta{Service: "orders", Version: "3.0.0", DeploymentID: "d2", Region: "us"}

	let envMap = Map[string, string]{
		"ERGO_OBSERVE":       "jsonl",
		"ERGO_SAMPLE_RATE":   "0.0",
		"ERGO_SAMPLE_SLOW":   "50ms",
		"ERGO_SAMPLE_ERRORS": "1",
	}
	let traceOut = io.NewRecorder()
	let sampledSink = with context { os.Process: fakeProc{env: envMap} } {
		trace.FromEnv(traceOut)
	}

	let logOut = io.NewRecorder()
	let logSink = log.NewJSONLines(logOut, clk, log.LevelInfo)

	with context { trace.Sink: sampledSink, log.Sink: logSink, time.Clock: clk } {
		log.Info("outside-span")

		let _ = trace.WithMetaSpan("fast.ok", svc, gen, true, func() int needs trace.Sink | log.Sink | time.Clock {
			clk.Advance(5ms)
			0
		})

		let _ = trace.WithMetaSpan("slow.ok", svc, gen, true, func() int needs trace.Sink | log.Sink | time.Clock {
			with log.Annotate("customer", "c-42") {
				trace.SetStr("tier", "enterprise")
				trace.Tag("vip")
				log.Info("inside-slow-span")
				clk.Advance(75ms)
			}
			0
		})

		let _ = trace.WithMetaSpan("fast.err", svc, gen, true, func() int fails opFail needs trace.Sink | log.Sink | time.Clock {
			clk.Advance(2ms)
			trace.RecordError("payment", "declined", "card declined", false)
			return opFail{reason: "declined"}
		}) catch {
		case opFail:
			0
		}
	}

	let rec = trace.NewRecorder()
	let fieldRule = trace.AnyOf([]trace.SampleRule{
		trace.KeepFieldEquals("tier", "vip"),
		trace.KeepTag("canary"),
	})
	let customSink = trace.TailSampledSink(rec, fieldRule)
	with context { trace.Sink: customSink, time.Clock: clk } {
		let _ = trace.WithMetaSpan("vip.req", svc, gen, true, func() int needs trace.Sink | time.Clock {
			trace.SetStr("tier", "vip")
			0
		})
		let _ = trace.WithMetaSpan("regular.req", svc, gen, true, func() int needs trace.Sink | time.Clock {
			trace.SetStr("tier", "free")
			0
		})
		let _ = trace.WithMetaSpan("canary.req", svc, gen, true, func() int needs trace.Sink | time.Clock {
			trace.Tag("canary")
			0
		})
	}
	let st = trace.Stats(customSink)
	io.Console.Println("CUSTOM seen=\(st.Seen) kept=\(st.Kept) dropped=\(st.Dropped)")
	io.Console.Println("TRACE_BEGIN")
	io.Console.Println(traceOut.Text())
	io.Console.Println("TRACE_END")
	io.Console.Println("LOG_BEGIN")
	io.Console.Println(logOut.Text())
	io.Console.Println("LOG_END")
}
`

// TestStdTraceSamplingAndLogCorrelation builds and runs an end-to-end native
// Ergo program exercising trace.FromEnv tail sampling, TailSampledSink field/tag
// rules, and automatic trace_id/span_id correlation in log.JSONLines.
func TestStdTraceSamplingAndLogCorrelation(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	root := moduleRootDir(t)
	dir := t.TempDir()
	modDir := filepath.Join(dir, "mod")
	srcPath := filepath.Join(modDir, "cmd", "driver", "main.ergo")
	if err := os.MkdirAll(filepath.Dir(srcPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "ergo.mod"), []byte("module m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcPath, []byte(traceSampleAndLogDriverSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	ergoBin := filepath.Join(dir, "bin", "ergo")
	if err := os.MkdirAll(filepath.Dir(ergoBin), 0o755); err != nil {
		t.Fatal(err)
	}
	build := ergobuild.Command(ergoBin, "./cmd/ergo", "-buildvcs=false")
	build.Dir = root
	if out, err := runBound(build); err != nil {
		t.Fatalf("build ergo: %v\n%s", err, out)
	}
	exe := filepath.Join(dir, "bin", "driver")
	compile := exec.Command(ergoBin, "build", "./cmd/driver", "-o", exe)
	compile.Dir = modDir
	compile.Env = append(os.Environ(), "ERGO_FALLBACK=on")
	if out, err := runBound(compile); err != nil {
		t.Fatalf("ergo build driver: %v\n%s", err, out)
	}
	run := exec.Command(exe)
	outBytes, err := runBound(run)
	if err != nil {
		t.Fatalf("run driver: %v\n%s", err, outBytes)
	}
	out := string(outBytes)

	if !strings.Contains(out, "CUSTOM seen=3 kept=2 dropped=1") {
		t.Fatalf("unexpected custom tail sampling stats:\n%s", out)
	}

	extract := func(startMarker, endMarker string) []map[string]any {
		t.Helper()
		s := strings.Index(out, startMarker+"\n")
		e := strings.Index(out, endMarker)
		if s < 0 || e <= s {
			t.Fatalf("missing markers %s/%s:\n%s", startMarker, endMarker, out)
		}
		block := strings.TrimSpace(out[s+len(startMarker)+1 : e])
		var rows []map[string]any
		for i, line := range strings.Split(block, "\n") {
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				t.Fatalf("%s line %d invalid JSON: %v\n%s", startMarker, i, err, line)
			}
			rows = append(rows, m)
		}
		return rows
	}

	traces := extract("TRACE_BEGIN", "TRACE_END")
	if len(traces) != 2 {
		t.Fatalf("expected 2 sampled traces (slow.ok + fast.err, fast.ok dropped), got %d:\n%s", len(traces), out)
	}
	if traces[0]["name"] != "slow.ok" || traces[1]["name"] != "fast.err" {
		t.Fatalf("unexpected sampled traces: [0]=%v [1]=%v", traces[0]["name"], traces[1]["name"])
	}

	logs := extract("LOG_BEGIN", "LOG_END")
	if len(logs) != 2 {
		t.Fatalf("expected 2 JSONLines log entries, got %d:\n%s", len(logs), out)
	}
	outsideLog := logs[0]
	insideLog := logs[1]
	if _, hasTrace := outsideLog["trace_id"]; hasTrace {
		t.Fatalf("outside-span log must not carry trace_id: %v", outsideLog)
	}
	if _, hasSpan := outsideLog["span_id"]; hasSpan {
		t.Fatalf("outside-span log must not carry span_id: %v", outsideLog)
	}
	if insideLog["trace_id"] != traces[0]["trace_id"] {
		t.Fatalf("inside-span log trace_id %v != slow.ok wide event trace_id %v", insideLog["trace_id"], traces[0]["trace_id"])
	}
	if insideLog["span_id"] != traces[0]["span_id"] {
		t.Fatalf("inside-span log span_id %v != slow.ok wide event span_id %v", insideLog["span_id"], traces[0]["span_id"])
	}
	if insideLog["customer"] != "c-42" {
		t.Fatalf("inside-span log missing log.Annotate customer=c-42: %v", insideLog)
	}
}
