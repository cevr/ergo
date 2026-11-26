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

// TestStdMetricLawsRun runs every law of std metric at check time in process
// and holds every metric row of runners.txt to a pass.
func TestStdMetricLawsRun(t *testing.T) {
	rows, err := lawledger.Read()
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := check.Std.Import("metric")
	if err != nil {
		t.Fatalf("import metric: %v", err)
	}
	b := eval.DefaultBudget()
	b.Steps = 200_000_000
	b.Bytes = 1 << 30
	rep, err := eval.Run(pkg, b)
	if err != nil {
		t.Fatalf("evaluate metric: %v", err)
	}
	results := map[string][]eval.Result{}
	for _, r := range rep.Results {
		name := strings.TrimPrefix(r.Name, "law ")
		if head, _, ok := strings.Cut(name, " for "); ok {
			name = head
		}
		if head, _, ok := strings.Cut(name, " under "); ok {
			name = head
		}
		key := "metric " + name
		results[key] = append(results[key], r)
	}
	outerT := t
	laws := map[string]lawledger.Law{}
	var ensureNativeRunner func(t *testing.T) (string, string)
	var ergoBin string
	var nativeModDir string
	ensureNativeRunner = func(t *testing.T) (string, string) {
		t.Helper()
		if ergoBin != "" {
			return ergoBin, nativeModDir
		}
		root := moduleRootDir(t)
		tmp := outerT.TempDir()
		ergoBin = filepath.Join(tmp, "bin", "ergo")
		if err := os.MkdirAll(filepath.Dir(ergoBin), 0o755); err != nil {
			t.Fatal(err)
		}
		build := ergobuild.Command(ergoBin, "./cmd/ergo", "-buildvcs=false")
		build.Dir = root
		if out, err := runBound(build); err != nil {
			t.Fatalf("build ergo for native metric law: %v\n%s", err, out)
		}
		nativeModDir = filepath.Join(tmp, "mod")
		pkgDir := filepath.Join(nativeModDir, "metric")
		if err := os.MkdirAll(pkgDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(nativeModDir, "ergo.mod"), []byte("module stdmetriccheck\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		srcDir := filepath.Join(root, "internal", "sketch", "std", "metric")
		entries, err := os.ReadDir(srcDir)
		if err != nil {
			t.Fatal(err)
		}
		for _, ent := range entries {
			if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".ergo") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(srcDir, ent.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(pkgDir, ent.Name()), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return ergoBin, nativeModDir
	}
	for _, row := range rows {
		if row.Runner != t.Name() {
			continue
		}
		row := row
		rs := results[row.Key()]
		statement, _ := lawledger.StdStatement(row.Key())
		laws[row.Key()] = lawledger.Law{Statement: statement, Check: func(t *testing.T) {
			if len(rs) == 0 {
				t.Errorf("std %s has no law %s at check time", row.Pkg, row.Law)
				return
			}
			passed := 0
			nativeSkipped := 0
			for _, r := range rs {
				switch r.Status {
				case eval.Passed:
					if r.Evidence != "tested" && r.Evidence != "proved" {
						t.Errorf("law %s (%s): passed with evidence=%q", row.Key(), r.Name, r.Evidence)
					} else {
						passed++
					}
				case eval.Skipped, eval.Native:
					nativeSkipped++
				default:
					t.Errorf("law %s (%s): %s (%s) after %d cases, seed %d, case %d: %+v", row.Key(), r.Name, r.Status, r.Reason, r.Cases, r.Seed, r.Case, r.Arguments)
				}
			}
			if passed == 0 && nativeSkipped > 0 {
				bin, modDir := ensureNativeRunner(t)
				cmd := exec.Command(bin, "test", "--native", "--count", "1", "--seed", "1", "--law", row.Law, "./metric")
				cmd.Dir = modDir
				cmd.Env = append(os.Environ(), "ERGO_FALLBACK=off")
				out, err := runBound(cmd)
				if err != nil {
					t.Fatalf("native law %s (ERGO_FALLBACK=off): %v\n%s", row.Key(), err, out)
				}
				if !strings.Contains(out, "ok law "+row.Law) {
					t.Fatalf("native law %s did not report ok:\n%s", row.Key(), out)
				}
				return
			}
			if passed == 0 {
				t.Errorf("law %s: no run passed (%+v)", row.Key(), rs)
			}
		}}
	}
	lawledger.Run(t, laws)
}

const metricNativeDriverSrc = `import config
import io
import metric
import strings
import task
import time
import trace

func main() needs io.Console {
	let reqCounter = metric.NewCounter("http.requests", "HTTP request counter") catch {
	case metric.InvalidMetric:
		io.Console.Println("ERR counter")
		return
	}
	let inFlight = metric.NewGauge("http.inflight", "In-flight requests") catch {
	case metric.InvalidMetric:
		io.Console.Println("ERR gauge")
		return
	}
	let latHist = metric.NewHistogram("http.latency", "HTTP latency seconds", []float64{0.01, 0.1, 1.0}) catch {
	case metric.InvalidMetric, metric.InvalidBoundaries:
		io.Console.Println("ERR hist")
		return
	}
	let errFreq = metric.NewFrequency("http.errors", "HTTP error classes") catch {
	case metric.InvalidMetric:
		io.Console.Println("ERR freq")
		return
	}
	let rpcSummary = metric.NewSummary("rpc.duration", "RPC latency summary", metric.DefaultSummaryQuantiles(), 4, 30s) catch {
	case metric.InvalidMetric, metric.InvalidSummary:
		io.Console.Println("ERR summary")
		return
	}
	let strictBudget = metric.StrictBudget(5) catch {
	case metric.InvalidBudget:
		io.Console.Println("ERR budget")
		return
	}
	let rec = metric.NewRecorder(strictBudget)
	let bridge = metric.NewTraceBridgeSink(rec)
	let token = config.NewRedacted("super-secret-jwt-999")
	let secLbl = metric.RedactedFrom("auth", token)
	let gen = trace.NewIDGenerator(uint64(42))
	let svc = trace.ServiceMeta{Service: "api", Version: "1.0.0", DeploymentID: "d1", Region: "us"}
	let t0 = time.Unix(1700000000, 0)

	let (_, events) = trace.Capture(func() bool needs trace.Sink | time.Clock {
		trace.WithMetaSpan("serve", svc, gen, true, func() bool needs trace.Sink | time.Clock {
			with context { metric.Sink: bridge } {
				let t1 = task.Go() {
					reqCounter.AddInt(int64(3), []metric.Label{metric.LabelOf("method", "GET"), secLbl})
					1
				}
				let t2 = task.Go() {
					reqCounter.AddInt(int64(4), []metric.Label{secLbl, metric.LabelOf(" method ", "GET")})
					2
				}
				let _ = t1.Wait()
				let _ = t2.Wait()
				inFlight.Set(10.0, []metric.Label{})
				inFlight.Add(2.5, []metric.Label{})
				inFlight.Sub(1.0, []metric.Label{})
				latHist.ObserveDuration(5ms, []metric.Label{metric.LabelOf("route", "/v1")})
				latHist.ObserveDuration(50ms, []metric.Label{metric.LabelOf("route", "/v1")})
				latHist.ObserveDuration(2s, []metric.Label{metric.LabelOf("route", "/v1")})
				errFreq.Record("timeout", []metric.Label{})
				errFreq.RecordCount("bad_gateway", int64(2), []metric.Label{})
				rpcSummary.SampleAt(1.0, t0, []metric.Label{})
				rpcSummary.SampleAt(2.0, t0.Add(5s), []metric.Label{})
				rpcSummary.SampleAt(3.0, t0.Add(10s), []metric.Label{})
				rpcSummary.SampleAt(4.0, t0.Add(15s), []metric.Label{})
				rpcSummary.SampleAt(5.0, t0.Add(20s), []metric.Label{})
				let mut exceeded = false
				reqCounter.TryAddInt(int64(1), []metric.Label{metric.LabelOf("route", "/overflow")}) catch {
				case metric.BudgetExceeded:
					exceeded = true
				case metric.InvalidDelta:
					return false
				}
				if !exceeded {
					return false
				}
			}
			let snap = rec.CollectAt(t0.Add(20s))
			metric.FlushToActiveSpan(snap)
			metric.EmitSnapshotToTrace(snap, "metrics.flush", svc, gen)
			true
		})
	})

	let snap = rec.CollectAt(t0.Add(20s))
	let jsonText = metric.EncodeJSON(snap)
	let decoded = metric.DecodeJSON(jsonText) catch {
	case metric.InvalidSnapshot:
		io.Console.Println("ERR decode")
		return
	}
	let om = metric.FormatOpenMetrics(snap)
	let omBudget = metric.FormatOpenMetricsWithBudget(snap)
	let hasNoopSkipped = with context { metric.Sink: metric.NewNoopSink() } {
		reqCounter.AddInt(int64(-99), []metric.Label{})
		true
	}
	let ok = decoded == snap &&
		snap.Budget.ActiveSeries == 5 &&
		snap.Budget.MaxSeries == 5 &&
		snap.Budget.OverflowCount == int64(1) &&
		events.Len() == 2 &&
		hasNoopSkipped &&
		!strings.Contains(om, "super-secret-jwt-999") &&
		strings.Contains(om, metric.RedactedText()) &&
		strings.Contains(omBudget, "ergo_metric_budget_active_series 5") &&
		strings.HasSuffix(om, "# EOF\n")
	if !ok {
		io.Console.Println("ERR assertions active=\(snap.Budget.ActiveSeries) overflow=\(snap.Budget.OverflowCount) events=\(events.Len()) eq=\(decoded == snap)")
		return
	}
	io.Console.Println("OK")
	io.Console.Println(om)
}
`

// TestStdMetricNativeEndToEnd builds and runs an end-to-end native Ergo binary
// with ERGO_FALLBACK=off exercising all metric primitives, cardinality budgets,
// redacted labels, OpenMetrics formatting, JSON snapshot round-trip, and trace
// emission.
func TestStdMetricNativeEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
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
	modDir := filepath.Join(tmp, "mod")
	srcPath := filepath.Join(modDir, "cmd", "driver", "main.ergo")
	if err := os.MkdirAll(filepath.Dir(srcPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "ergo.mod"), []byte("module m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcPath, []byte(metricNativeDriverSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(tmp, "bin", "driver")
	compile := exec.Command(ergoBin, "build", "./cmd/driver", "-o", exe)
	compile.Dir = modDir
	compile.Env = append(os.Environ(), "ERGO_FALLBACK=off")
	if out, err := runBound(compile); err != nil {
		t.Fatalf("ergo build driver (ERGO_FALLBACK=off): %v\n%s", err, out)
	}
	run := exec.Command(exe)
	out, err := runBound(run)
	if err != nil {
		t.Fatalf("run driver: %v\n%s", err, out)
	}
	if !strings.HasPrefix(out, "OK\n") {
		t.Fatalf("unexpected driver output:\n%s", out)
	}
}
