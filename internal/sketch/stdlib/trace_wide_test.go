package stdlib_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/ergobuild"
)

const traceWideDriverSrc = `import io
import log
import option
import os
import task
import time
import trace

type fakeProc {
	env Map[string, string],
} implements os.Process

func Args(_ fakeProc) []string { []string{"app"} }

func Getenv(p fakeProc, key string) option.Option[string] { p.env.Get(key) }

func Environ(p fakeProc) Map[string, string] { p.env }

func Getwd(_ fakeProc) string fails os.NoWorkdir { "/" }

type checkoutErr { code string }

func enrichOrder(userID string) {
	trace.SetGroup("user", []trace.Field{
		trace.Str("id", userID),
		trace.Str("tier", "vip"),
		trace.Int("account_age_days", 420),
	})
	trace.SetRedacted("auth.token")
	trace.Count("db.queries", 2)
	trace.AddTiming("db", 4ms)
	trace.Tag("vip")
	trace.Tag("flag:new_checkout")
}

func main() needs io.Console {
	let clk = time.NewManual(1700000000000000000)
	let gen = trace.NewIDGenerator(uint64(42))
	let svc = trace.ServiceMeta{Service: "checkout", Version: "2.3.1", DeploymentID: "dep-9", Region: "us-east-1"}
	let rec = trace.NewRecorder()
	let jsonOut = io.NewRecorder()
	let consoleOut = io.NewRecorder()
	let tee = trace.NewTeeSink(rec, trace.NewTeeSink(trace.NewJSONLinesSink(jsonOut), trace.NewConsoleSink(consoleOut)))

	let mut propagatedHeader = ""

	with context { trace.Sink: tee, time.Clock: clk } {
		with log.Annotate("tenant", "acme") {
			let _ = trace.WithMetaSpan("POST /checkout", svc, gen, true, func() int needs trace.Sink | time.Clock {
				enrichOrder("u-99")
				let t1 = task.Go() {
					trace.Count("db.queries", 1)
					trace.AddTiming("db", 3ms)
					trace.SetBool("cache.warm", true)
					1
				}
				let t2 = task.Go() {
					trace.Count("db.queries", 3)
					trace.AddTiming("payment", 9ms)
					trace.Tag("vip")
					2
				}
				let _ = t1.Wait()
				let _ = t2.Wait()
				clk.Advance(15ms)
				match trace.Traceparent() {
				case option.Some[string] as tp:
					propagatedHeader = tp.Value
				case option.None:
				}
				let _ = trace.WithSpan("db.charge", func() bool needs trace.Sink | time.Clock {
					trace.SetStr("provider", "stripe")
					clk.Advance(7ms)
					true
				})
				1
			})
		}

		let incoming = trace.ParseTraceparent(propagatedHeader) catch {
		case trace.InvalidTraceparent:
			trace.TraceContext{TraceID: "", SpanID: "", Sampled: false}
		}
		let _ = trace.WithIncomingSpan("payment.webhook", svc, incoming, gen, func() int fails checkoutErr needs trace.Sink | time.Clock {
			trace.SetStr("webhook.id", "wh-7")
			clk.Advance(4ms)
			trace.RecordError("payment", "card_declined", "insufficient funds", false)
			return checkoutErr{code: "card_declined"}
		}) catch {
		case checkoutErr:
			0
		}

		let _ = trace.WithMetaSpan("implicit.fail", svc, gen, true, func() int fails checkoutErr needs trace.Sink | time.Clock {
			clk.Advance(2ms)
			return checkoutErr{code: "boom"}
		}) catch {
		case checkoutErr:
			0
		}
	}

	let mut noopActive = true
	let mut noopSpanName = ""
	with context { trace.Sink: trace.NewNoopSink(), time.Clock: clk } {
		let _ = trace.WithSpan("disabled.op", func() int needs trace.Sink | time.Clock {
			noopActive = trace.Active()
			match trace.Current() {
			case option.Some[string] as s:
				noopSpanName = s.Value
			case option.None:
			}
			0
		})
	}

	let envMap = Map[string, string]{
		"ERGO_OBSERVE":         "jsonl",
		"ERGO_SERVICE_NAME":    "billing",
		"ERGO_SERVICE_VERSION": "9.1.0",
		"ERGO_DEPLOYMENT_ID":   "dep-77",
		"ERGO_REGION":          "eu-west-1",
	}
	let envOut = io.NewRecorder()
	let mut envMode = ""
	let mut envSvc = ""
	let mut envEnabled = false
	with context { os.Process: fakeProc{env: envMap} } {
		let cfg = trace.ConfigFromEnv()
		envMode = cfg.Mode
		envSvc = cfg.Service.Service + "@" + cfg.Service.Version + "/" + cfg.Service.Region
		let s = trace.SinkFromEnv(envOut)
		envEnabled = s.Enabled()
	}

	let mut offEnabled = true
	with context { os.Process: fakeProc{env: Map[string, string]{}} } {
		offEnabled = trace.SinkFromEnv(envOut).Enabled()
	}

	let mut zeroRejected = false
	let _ = trace.ParseTraceparent("00-00000000000000000000000000000000-0123456789abcdef-01") catch {
	case trace.InvalidTraceparent:
		zeroRejected = true
		trace.TraceContext{TraceID: "", SpanID: "", Sampled: false}
	}

	let filteredCount = rec.FilterByName("db.charge").Len()
	let errCount = rec.Errors().Len()
	let totalCount = rec.Events().Len()
	rec.Clear()
	let afterClear = rec.Events().Len()

	io.Console.Println("SUMMARY total=\(totalCount) filtered=\(filteredCount) errors=\(errCount) afterClear=\(afterClear) noopActive=\(noopActive) noopSpan=\(noopSpanName) envMode=\(envMode) envSvc=\(envSvc) envEnabled=\(envEnabled) offEnabled=\(offEnabled) zeroRejected=\(zeroRejected)")
	io.Console.Println("TRACEPARENT " + propagatedHeader)
	io.Console.Println("CONSOLE_BEGIN")
	io.Console.Println(consoleOut.Text())
	io.Console.Println("CONSOLE_END")
	io.Console.Println("JSON_BEGIN")
	io.Console.Println(jsonOut.Text())
	io.Console.Println("JSON_END")
}
`

func TestStdTraceWideEventsEndToEnd(t *testing.T) {
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
	if err := os.WriteFile(srcPath, []byte(traceWideDriverSrc), 0o644); err != nil {
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

	wantSummary := "SUMMARY total=4 filtered=1 errors=2 afterClear=0 noopActive=false noopSpan=disabled.op envMode=jsonl envSvc=billing@9.1.0/eu-west-1 envEnabled=true offEnabled=false zeroRejected=true"
	if !strings.Contains(out, wantSummary) {
		t.Fatalf("unexpected summary:\ngot:\n%s\nwant substring:\n%s", out, wantSummary)
	}

	jsonStart := strings.Index(out, "JSON_BEGIN\n")
	jsonEnd := strings.Index(out, "JSON_END")
	if jsonStart < 0 || jsonEnd < 0 || jsonEnd <= jsonStart {
		t.Fatalf("missing JSON_BEGIN/JSON_END markers:\n%s", out)
	}
	jsonBlock := strings.TrimSpace(out[jsonStart+len("JSON_BEGIN\n") : jsonEnd])
	lines := strings.Split(jsonBlock, "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 canonical JSON wide events, got %d:\n%s", len(lines), jsonBlock)
	}

	var events []map[string]any
	for i, line := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("line %d is not valid JSON: %v\n%s", i, err, line)
		}
		events = append(events, m)
	}

	child := events[0]
	rootEv := events[1]
	webhook := events[2]
	implicitFail := events[3]

	if child["name"] != "db.charge" || rootEv["name"] != "POST /checkout" {
		t.Fatalf("unexpected event order: [0]=%v [1]=%v", child["name"], rootEv["name"])
	}
	if child["trace_id"] != rootEv["trace_id"] {
		t.Fatalf("child trace_id %v != root trace_id %v", child["trace_id"], rootEv["trace_id"])
	}
	if child["parent_span_id"] != rootEv["span_id"] {
		t.Fatalf("child parent_span_id %v != root span_id %v", child["parent_span_id"], rootEv["span_id"])
	}
	if _, hasParent := rootEv["parent_span_id"]; hasParent {
		t.Fatalf("root span should not have parent_span_id: %v", rootEv["parent_span_id"])
	}
	if child["duration_ms"] != float64(7) {
		t.Fatalf("child duration_ms = %v, want 7", child["duration_ms"])
	}
	if rootEv["duration_ms"] != float64(22) {
		t.Fatalf("root duration_ms = %v, want 22", rootEv["duration_ms"])
	}
	if webhook["trace_id"] != rootEv["trace_id"] || webhook["parent_span_id"] != rootEv["span_id"] {
		t.Fatalf("incoming W3C traceparent did not link webhook to root: webhook=%v root=%v", webhook, rootEv)
	}

	fields, _ := rootEv["fields"].(map[string]any)
	if fields["tenant"] != "acme" {
		t.Fatalf("log.Annotate tenant did not flow into wide event fields: %v", fields)
	}
	if fields["auth.token"] != "[REDACTED]" {
		t.Fatalf("redacted field auth.token = %v, want [REDACTED]", fields["auth.token"])
	}
	if fields["cache.warm"] != true {
		t.Fatalf("concurrent task field cache.warm = %v, want true", fields["cache.warm"])
	}
	userGroup, _ := fields["user"].(map[string]any)
	if userGroup["id"] != "u-99" || userGroup["tier"] != "vip" || userGroup["account_age_days"] != float64(420) {
		t.Fatalf("nested user group mismatch: %v", userGroup)
	}
	counters, _ := rootEv["counters"].(map[string]any)
	if counters["db.queries"] != float64(6) {
		t.Fatalf("accumulated db.queries counter = %v, want 6 (2+1+3 across concurrent tasks)", counters["db.queries"])
	}
	timings, _ := rootEv["timings_ms"].(map[string]any)
	if timings["db"] != float64(7) || timings["payment"] != float64(9) {
		t.Fatalf("accumulated timings_ms mismatch: %v", timings)
	}
	tags, _ := rootEv["tags"].([]any)
	if len(tags) != 2 {
		t.Fatalf("expected 2 deduplicated tags, got %v", tags)
	}

	if webhook["outcome"] != "error" {
		t.Fatalf("webhook outcome = %v, want error", webhook["outcome"])
	}
	errObj, _ := webhook["error"].(map[string]any)
	if errObj["kind"] != "payment" || errObj["code"] != "card_declined" || errObj["retriable"] != false {
		t.Fatalf("webhook error object mismatch: %v", errObj)
	}
	if implicitFail["outcome"] != "error" {
		t.Fatalf("implicit failure span did not emit OutcomeError: %v", implicitFail)
	}

	if !strings.Contains(out, "[OK] POST /checkout 22ms") || !strings.Contains(out, "[ERR:card_declined] payment.webhook 4ms") {
		t.Fatalf("console sink formatting missing expected summaries:\n%s", out)
	}
}
