package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	kp "ergo.local/prototype/internal/experimental/kernelprobe"
)

type latencyStats struct {
	N        int     `json:"n"`
	MinMs    float64 `json:"min_ms"`
	MedianMs float64 `json:"median_ms"`
	P95Ms    float64 `json:"p95_ms"`
	MaxMs    float64 `json:"max_ms"`
	MeanMs   float64 `json:"mean_ms"`
}

type engineBench struct {
	Engine            string       `json:"engine"`
	ColdFirstMs       float64      `json:"cold_first_ms"`
	ColdStart         latencyStats `json:"cold_start"`
	TrivialCell       latencyStats `json:"trivial_cell"`
	ToolCell10        latencyStats `json:"tool_cell_10"`
	Snapshot          latencyStats `json:"snapshot"`
	Restore           latencyStats `json:"restore"`
	SnapshotBytes     int          `json:"snapshot_bytes"`
	CutLatencyMs      float64      `json:"cut_latency_ms"`
	AsyncWakeSignalUs float64      `json:"async_wake_signal_us"`
	WakeRestoreEvalMs float64      `json:"wake_restore_eval_ms"`
	BytecodeSnapBytes int          `json:"bytecode_snap_bytes,omitempty"`
	TasksPassed       int          `json:"tasks_passed"`
	TasksTotal        int          `json:"tasks_total"`
}

type sizeEntry struct {
	Label       string  `json:"label"`
	UnstrippedB int64   `json:"unstripped_bytes"`
	StrippedB   int64   `json:"stripped_bytes"`
	DeltaMiB    float64 `json:"delta_mib"`
	SharedLibB  int64   `json:"shared_lib_bytes,omitempty"`
	StaticLibB  int64   `json:"static_lib_bytes,omitempty"`
	Note        string  `json:"note,omitempty"`
}

type benchReport struct {
	TimestampUTC string                 `json:"timestamp_utc"`
	Iterations   int                    `json:"iterations"`
	Engines      []engineBench          `json:"engines"`
	BinarySizes  []sizeEntry            `json:"binary_sizes"`
	Complexity   map[string]interface{} `json:"complexity"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	sub := os.Args[1]
	switch sub {
	case "bench":
		runBenchCmd(os.Args[2:])
	case "eval":
		runEvalCmd(os.Args[2:])
	case "prompts":
		runPromptsCmd(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  kernelprobe bench [--iters 50] [--sizes] [--out /path/raw.json]
  kernelprobe eval --lang <psychoscript|luau|ergoscript> [--task N | --all] [--cell-file path ...] [--json]
  kernelprobe prompts [--json]`)
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func engineFactory(lang string) (func(*kp.EvalEnv) (kp.KernelEngine, error), error) {
	switch lang {
	case "psychoscript":
		return func(env *kp.EvalEnv) (kp.KernelEngine, error) {
			return kp.NewPsychoscriptEngine(env.Tools, kp.NewSlotPool(kp.MaxRoleLiveSlots))
		}, nil
	case "luau":
		return func(env *kp.EvalEnv) (kp.KernelEngine, error) {
			return kp.NewLuauEngine(env.Tools, kp.NewSlotPool(kp.MaxRoleLiveSlots))
		}, nil
	case "ergoscript":
		return func(env *kp.EvalEnv) (kp.KernelEngine, error) {
			return kp.NewErgoScriptEngine(env.Tools, kp.NewSlotPool(kp.MaxRoleLiveSlots)), nil
		}, nil
	default:
		return nil, fmt.Errorf("unknown engine %q (want psychoscript, luau, or ergoscript)", lang)
	}
}

func runEvalCmd(args []string) {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	lang := fs.String("lang", "psychoscript", "engine: psychoscript, luau, or ergoscript")
	taskID := fs.Int("task", 0, "task number 1..10")
	all := fs.Bool("all", false, "run all 10 tasks")
	asJSON := fs.Bool("json", false, "emit JSON output")
	var cellFiles stringList
	fs.Var(&cellFiles, "cell-file", "cell source file (repeat for multi-cell tasks 3, 7, 8)")
	_ = fs.Parse(args)

	factory, err := engineFactory(*lang)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	tasks := kp.EvalTasks()
	var selected []kp.EvalTask
	if *all || *taskID == 0 {
		selected = tasks
	} else {
		for _, t := range tasks {
			if t.ID == *taskID {
				selected = append(selected, t)
			}
		}
		if len(selected) == 0 {
			fmt.Fprintf(os.Stderr, "unknown task id %d (must be 1..10)\n", *taskID)
			os.Exit(2)
		}
	}

	var customCells []string
	for _, p := range cellFiles {
		b, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read %s: %v\n", p, err)
			os.Exit(2)
		}
		customCells = append(customCells, string(b))
	}
	if len(customCells) > 0 && len(selected) != 1 {
		fmt.Fprintln(os.Stderr, "--cell-file requires a single --task")
		os.Exit(2)
	}

	ctx := context.Background()
	var reports []kp.EvalTaskReport
	allPass := true
	for _, t := range selected {
		cells := t.CellsByLang[*lang]
		if len(customCells) > 0 {
			cells = customCells
		}
		rep := kp.VerifyTask(ctx, t, *lang, factory, cells)
		if !rep.Passed {
			allPass = false
		}
		reports = append(reports, rep)
	}

	if *asJSON {
		b, _ := json.MarshalIndent(reports, "", "  ")
		fmt.Println(string(b))
	} else {
		for _, r := range reports {
			status := "PASS"
			if !r.Passed {
				status = "FAIL"
			}
			fmt.Printf("[%s] T%02d %-18s %-12s %s\n", status, r.TaskID, r.Slug, r.Engine, r.Detail)
		}
	}
	if !allPass {
		os.Exit(1)
	}
}

func runPromptsCmd(args []string) {
	fs := flag.NewFlagSet("prompts", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	_ = fs.Parse(args)

	tasks := kp.EvalTasks()
	if *asJSON {
		type promptItem struct {
			ID       int    `json:"id"`
			Slug     string `json:"slug"`
			Title    string `json:"title"`
			Evidence string `json:"evidence"`
			Prompt   string `json:"prompt"`
			Verify   string `json:"verifier_cmd"`
		}
		var out []promptItem
		for _, t := range tasks {
			cellFlags := "--cell-file <cell.src>"
			if len(t.CellsByLang["psychoscript"]) > 1 {
				cellFlags = "--cell-file <cell1.src> --cell-file <cell2.src>"
			}
			out = append(out, promptItem{
				ID:       t.ID,
				Slug:     t.Slug,
				Title:    t.Title,
				Evidence: t.Evidence,
				Prompt:   t.Prompt,
				Verify:   fmt.Sprintf("KERNELPROBE_LIB_DIR=/tmp/kernelprobe/lib go run ./internal/experimental/kernelprobe/cmd/kernelprobe eval --lang <psychoscript|luau|ergoscript> --task %d %s --json", t.ID, cellFlags),
			})
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return
	}
	for _, t := range tasks {
		cellFlags := "--cell-file <cell.src>"
		if len(t.CellsByLang["psychoscript"]) > 1 {
			cellFlags = "--cell-file <cell1.src> --cell-file <cell2.src>"
		}
		fmt.Printf("### Task %d: %s (%s)\nEvidence: %s\nPrompt:\n%s\nVerify:\n  KERNELPROBE_LIB_DIR=/tmp/kernelprobe/lib go run ./internal/experimental/kernelprobe/cmd/kernelprobe eval --lang <psychoscript|luau|ergoscript> --task %d %s --json\n\n",
			t.ID, t.Title, t.Slug, t.Evidence, t.Prompt, t.ID, cellFlags)
	}
}

func runBenchCmd(args []string) {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	iters := fs.Int("iters", 50, "warmup+measured iterations for per-cell metrics")
	sizes := fs.Bool("sizes", true, "measure real compiled Go binary size deltas in /tmp/kernelprobe/sizetest")
	outPath := fs.String("out", "", "optional JSON output path")
	_ = fs.Parse(args)

	langs := []string{"psychoscript", "luau", "ergoscript"}
	var results []engineBench
	for _, lang := range langs {
		b, err := benchEngine(lang, *iters)
		if err != nil {
			fmt.Fprintf(os.Stderr, "benchmark %s failed: %v\n", lang, err)
			os.Exit(1)
		}
		results = append(results, b)
	}

	var sizeEntries []sizeEntry
	if *sizes {
		var err error
		sizeEntries, err = measureBinarySizes()
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: binary size measurement failed: %v\n", err)
		}
	}

	rep := benchReport{
		TimestampUTC: time.Now().UTC().Format(time.RFC3339),
		Iterations:   *iters,
		Engines:      results,
		BinarySizes:  sizeEntries,
		Complexity: map[string]interface{}{
			"psychoscript": map[string]interface{}{
				"cgo_required":   true,
				"pure_go":        false,
				"cgo_disabled":   "stub returns ErrNativeUnavailable",
				"cross_compile":  "requires Rust toolchain + per-target C cross-linker",
				"native_deps":    "rustc 1.99.1 + 137 Cargo.lock packages (oxc 0.159, icu 2.4, serde, psychoscript VM)",
				"binding_bridge": "kp_abi.h -> C ABI wrapper (psychoscript_c) -> cgo dlopen",
			},
			"luau": map[string]interface{}{
				"cgo_required":   true,
				"pure_go":        false,
				"cgo_disabled":   "stub returns ErrNativeUnavailable",
				"cross_compile":  "requires C++17 cross-compiler (clang++/zig c++); zero Rust/cargo deps",
				"native_deps":    "Luau upstream C++17 Compiler + VM + Common (~40 .cpp files, zero external crates)",
				"binding_bridge": "kp_abi.h -> C++ ABI wrapper (luau_c) -> cgo dlopen",
			},
			"ergoscript": map[string]interface{}{
				"cgo_required":   false,
				"pure_go":        true,
				"cgo_disabled":   "full support natively",
				"cross_compile":  "standard Go GOOS/GOARCH (CGO_ENABLED=0)",
				"native_deps":    "none (imports only internal/sketch/syntax + stdlib)",
				"binding_bridge": "direct in-process AST interpreter (no FFI)",
			},
		},
	}

	if *outPath != "" {
		if err := os.MkdirAll(filepath.Dir(*outPath), 0o755); err == nil {
			raw, _ := json.MarshalIndent(rep, "", "  ")
			if werr := os.WriteFile(*outPath, raw, 0o644); werr != nil {
				fmt.Fprintf(os.Stderr, "write %s: %v\n", *outPath, werr)
				os.Exit(1)
			}
		}
	}

	raw, _ := json.MarshalIndent(rep, "", "  ")
	fmt.Println(string(raw))
}

func benchEngine(lang string, iters int) (engineBench, error) {
	ctx := context.Background()
	factory, err := engineFactory(lang)
	if err != nil {
		return engineBench{}, err
	}

	var trivialCode, tool10Code, bindCode, checkBindCode, cutCode, wakeCode string
	switch lang {
	case "psychoscript":
		trivialCode = `const a = 40; const b = 2; return { sum: a + b, ok: true };`
		tool10Code = `
let total = 0;
for (let i = 0; i < 10; i++) {
  const s = await tools.read({ path: "src/config.ergo" });
  total += s.content.length;
}
return { bytes: total };`
		bindCode = `
const workspace = "/workspaces/ergo";
const shard = 7;
const lanes = ["u01","u02","u03","u04","u05","u06","u07","u08"];
const meta = { branch: "harness-spec", count: 8, nested: { ok: true, tags: ["a","b","c"] } };
const summary = "snapshot benchmark payload";
return meta;`
		checkBindCode = `return { workspace, shard, n: lanes.length, branch: meta.branch, summary };`
		cutCode = `const r = await tools.shell({ command: "slow-gate" }); return r;`
		wakeCode = `const h = await tools.handles.get({ id: handleID }); return { state: h.state, passed: h.value && h.value.passed };`
	case "luau":
		trivialCode = `local a = 40; local b = 2; return { sum = a + b, ok = true }`
		tool10Code = `
local total = 0
for i = 1, 10 do
  local s = tools.read({ path = "src/config.ergo" })
  total += #s.content
end
return { bytes = total }`
		bindCode = `
workspace = "/workspaces/ergo"
shard = 7
lanes = {"u01","u02","u03","u04","u05","u06","u07","u08"}
meta = { branch = "harness-spec", count = 8, nested = { ok = true, tags = {"a","b","c"} } }
summary = "snapshot benchmark payload"
return meta`
		checkBindCode = `return { workspace = workspace, shard = shard, n = #lanes, branch = meta.branch, summary = summary }`
		cutCode = `local r = tools.shell({ command = "slow-gate" }); return r`
		wakeCode = `local h = tools.handles.get({ id = handleID }); return { state = h.state, passed = h.value and h.value.passed }`
	case "ergoscript":
		trivialCode = `let a = 40; let b = 2; return Args{sum: a + b, ok: true}`
		tool10Code = `
let mut total = 0
for i in []any{0, 1, 2, 3, 4, 5, 6, 7, 8, 9} {
  let s = try tools.read(Args{path: "src/config.ergo"})
  total = total + len(s.content)
}
return Args{bytes: total}`
		bindCode = `
session.workspace = "/workspaces/ergo"
session.shard = 7
session.lanes = []any{"u01","u02","u03","u04","u05","u06","u07","u08"}
session.meta = Args{branch: "harness-spec", count: 8, nested: Args{ok: true, tags: []any{"a","b","c"}}}
session.summary = "snapshot benchmark payload"
return session.meta`
		checkBindCode = `return Args{workspace: session.workspace, shard: session.shard, n: len(session.lanes), branch: session.meta.branch, summary: session.summary}`
		cutCode = `let r = try tools.shell(Args{command: "slow-gate"}); return r`
		wakeCode = `let h = try tools.handles.get(Args{id: handleID}); return Args{state: h.state, passed: h.value.passed}`
	}

	// Metric 1: Cold start latency (engine open + first trivial cell).
	coldSamples := make([]float64, 0, 10)
	var coldFirstMs float64
	for i := 0; i < 10; i++ {
		env := kp.NewEvalEnv()
		t0 := time.Now()
		eng, err := factory(env)
		if err != nil {
			return engineBench{}, err
		}
		res, err := eng.Eval(ctx, kp.EvalRequest{
			CellID:    fmt.Sprintf("cold-%d", i),
			SessionID: "bench-cold",
			OwnerID:   "bench/0",
			Role:      kp.RoleRoot,
			Code:      trivialCode,
			Limits:    kp.DefaultLimits(),
		})
		dt := ms(time.Since(t0))
		_ = eng.Close()
		if err != nil || res.Status != kp.EvalCompleted {
			return engineBench{}, fmt.Errorf("cold cell failed (%s): err=%v res=%+v", lang, err, res)
		}
		if i == 0 {
			coldFirstMs = dt
		}
		coldSamples = append(coldSamples, dt)
	}

	// Metric 2: Per-cell warm execution (trivial and 10-tool loop).
	env := kp.NewEvalEnv()
	eng, err := factory(env)
	if err != nil {
		return engineBench{}, err
	}
	defer eng.Close()

	// Warmup.
	for i := 0; i < 5; i++ {
		_, _ = eng.Eval(ctx, kp.EvalRequest{CellID: "warm", SessionID: "bench-w", OwnerID: "bench/0", Role: kp.RoleRoot, Code: trivialCode, Limits: kp.DefaultLimits()})
		_, _ = eng.Eval(ctx, kp.EvalRequest{CellID: "warm-t", SessionID: "bench-w", OwnerID: "bench/0", Role: kp.RoleRoot, Code: tool10Code, Limits: kp.DefaultLimits()})
	}

	trivialSamples := make([]float64, 0, iters)
	tool10Samples := make([]float64, 0, iters)
	for i := 0; i < iters; i++ {
		t0 := time.Now()
		r1, err := eng.Eval(ctx, kp.EvalRequest{CellID: fmt.Sprintf("triv-%d", i), SessionID: "bench-w", OwnerID: "bench/0", Role: kp.RoleRoot, Code: trivialCode, Limits: kp.DefaultLimits()})
		dt1 := ms(time.Since(t0))
		if err != nil || r1.Status != kp.EvalCompleted {
			return engineBench{}, fmt.Errorf("trivial failed (%s): err=%v res=%+v", lang, err, r1)
		}
		trivialSamples = append(trivialSamples, dt1)

		t1 := time.Now()
		r2, err := eng.Eval(ctx, kp.EvalRequest{CellID: fmt.Sprintf("tool-%d", i), SessionID: "bench-w", OwnerID: "bench/0", Role: kp.RoleRoot, Code: tool10Code, Limits: kp.DefaultLimits()})
		dt2 := ms(time.Since(t1))
		if err != nil || r2.Status != kp.EvalCompleted {
			return engineBench{}, fmt.Errorf("tool10 failed (%s): err=%v res=%+v", lang, err, r2)
		}
		tool10Samples = append(tool10Samples, dt2)
	}

	// Metric 3: Snapshot + Restore cost across cells.
	resBind, err := eng.Eval(ctx, kp.EvalRequest{CellID: "bind", SessionID: "bench-snap", OwnerID: "bench/0", Role: kp.RoleRoot, Code: bindCode, Limits: kp.DefaultLimits()})
	if err != nil || resBind.Status != kp.EvalCompleted {
		return engineBench{}, fmt.Errorf("bind cell failed (%s): err=%v res=%+v", lang, err, resBind)
	}

	snapSamples := make([]float64, 0, 20)
	restoreSamples := make([]float64, 0, 20)
	var snapBytes int
	for i := 0; i < 20; i++ {
		tS := time.Now()
		blob, err := eng.Snapshot(ctx)
		dtS := ms(time.Since(tS))
		if err != nil {
			return engineBench{}, fmt.Errorf("snapshot (%s): %v", lang, err)
		}
		rawSnap, _ := json.Marshal(blob)
		snapBytes = len(rawSnap)
		snapSamples = append(snapSamples, dtS)

		eng2, err := factory(kp.NewEvalEnv())
		if err != nil {
			return engineBench{}, err
		}
		tR := time.Now()
		err = eng2.Restore(ctx, blob)
		dtR := ms(time.Since(tR))
		if err != nil {
			_ = eng2.Close()
			return engineBench{}, fmt.Errorf("restore (%s): %v", lang, err)
		}
		restoreSamples = append(restoreSamples, dtR)
		if i == 0 {
			chk, cerr := eng2.Eval(ctx, kp.EvalRequest{CellID: "chk", SessionID: "bench-snap", OwnerID: "bench/0", Role: kp.RoleRoot, Code: checkBindCode, Limits: kp.DefaultLimits()})
			if cerr != nil || chk.Status != kp.EvalCompleted {
				_ = eng2.Close()
				return engineBench{}, fmt.Errorf("restored check cell failed (%s): err=%v res=%+v", lang, cerr, chk)
			}
		}
		_ = eng2.Close()
	}

	// Metric 4: Async settle waking the session after cut.
	asyncEnv := kp.NewEvalEnv()
	asyncEng, err := factory(asyncEnv)
	if err != nil {
		return engineBench{}, err
	}
	var wakeAt time.Time
	wakeCh := make(chan time.Time, 2)
	asyncEng.OnAsyncSettle(func(ev kp.AsyncSettleEvent) {
		wakeCh <- time.Now()
	})
	tCutStart := time.Now()
	cutRes, err := asyncEng.Eval(ctx, kp.EvalRequest{
		CellID:    "cut-cell",
		SessionID: "bench-async",
		OwnerID:   "bench/0",
		Role:      kp.RoleRoot,
		Code:      cutCode,
		Limits:    kp.DefaultLimits(),
	})
	cutLatencyMs := ms(time.Since(tCutStart))
	if err != nil || cutRes.Status != kp.EvalCut || cutRes.Cut == nil {
		_ = asyncEng.Close()
		return engineBench{}, fmt.Errorf("cut cell failed (%s): err=%v res=%+v", lang, err, cutRes)
	}
	asyncSnap, err := asyncEng.Snapshot(ctx)
	if err != nil {
		_ = asyncEng.Close()
		return engineBench{}, err
	}
	asyncSnap.Cut = cutRes.Cut
	_ = asyncEng.Close()

	asyncEnv.SlowPhase2.Store(true)
	tSettle := time.Now()
	ok, err := asyncEnv.Handles.Settle(asyncEnv.SlowHandleID, kp.DispSucceeded, map[string]any{"passed": true})
	if err != nil || !ok {
		return engineBench{}, fmt.Errorf("settle failed (%s): ok=%v err=%v", lang, ok, err)
	}
	wakeAt = <-wakeCh
	asyncWakeSignalUs := float64(wakeAt.Sub(tSettle).Nanoseconds()) / 1e3

	tWakeEval := time.Now()
	asyncEng2, err := factory(asyncEnv)
	if err != nil {
		return engineBench{}, err
	}
	if err := asyncEng2.Restore(ctx, asyncSnap); err != nil {
		_ = asyncEng2.Close()
		return engineBench{}, err
	}
	wakeRes, err := asyncEng2.Eval(ctx, kp.EvalRequest{
		CellID:    "wake-cell",
		SessionID: "bench-async",
		OwnerID:   "bench/0",
		Role:      kp.RoleRoot,
		Code:      wakeCode,
		Bindings:  map[string]any{"handleID": asyncEnv.SlowHandleID},
		Limits:    kp.DefaultLimits(),
	})
	wakeRestoreEvalMs := ms(time.Since(tWakeEval))
	_ = asyncEng2.Close()
	if err != nil || wakeRes.Status != kp.EvalCompleted {
		return engineBench{}, fmt.Errorf("wake cell failed (%s): err=%v res=%+v", lang, err, wakeRes)
	}
	wm, _ := wakeRes.Value.(map[string]any)
	if wm["state"] != "settled" || wm["passed"] != true {
		return engineBench{}, fmt.Errorf("wake cell unexpected value (%s): %+v", lang, wakeRes.Value)
	}

	// Metric 7 sanity: verify all 10 tasks pass.
	tasks := kp.EvalTasks()
	passed := 0
	for _, t := range tasks {
		rep := kp.VerifyTask(ctx, t, lang, factory, t.CellsByLang[lang])
		if rep.Passed {
			passed++
		}
	}

	return engineBench{
		Engine:            lang,
		ColdFirstMs:       coldFirstMs,
		ColdStart:         summarize(coldSamples),
		TrivialCell:       summarize(trivialSamples),
		ToolCell10:        summarize(tool10Samples),
		Snapshot:          summarize(snapSamples),
		Restore:           summarize(restoreSamples),
		SnapshotBytes:     snapBytes,
		CutLatencyMs:      cutLatencyMs,
		AsyncWakeSignalUs: asyncWakeSignalUs,
		WakeRestoreEvalMs: wakeRestoreEvalMs,
		BytecodeSnapBytes: len(asyncSnap.EngineBytes),
		TasksPassed:       passed,
		TasksTotal:        len(tasks),
	}, nil
}

func ms(d time.Duration) float64 {
	return float64(d.Nanoseconds()) / 1e6
}

func summarize(xs []float64) latencyStats {
	cp := slices.Clone(xs)
	sort.Float64s(cp)
	if len(cp) == 0 {
		return latencyStats{}
	}
	var sum float64
	for _, v := range cp {
		sum += v
	}
	med := cp[len(cp)/2]
	if len(cp)%2 == 0 {
		med = (cp[len(cp)/2-1] + cp[len(cp)/2]) / 2
	}
	p95Idx := int(float64(len(cp)-1) * 0.95)
	return latencyStats{
		N:        len(cp),
		MinMs:    cp[0],
		MedianMs: med,
		P95Ms:    cp[p95Idx],
		MaxMs:    cp[len(cp)-1],
		MeanMs:   sum / float64(len(cp)),
	}
}

func measureBinarySizes() ([]sizeEntry, error) {
	repoRoot, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "go.mod")); err != nil {
		return nil, fmt.Errorf("run from repo root (%v)", err)
	}
	libDir := os.Getenv("KERNELPROBE_LIB_DIR")
	if libDir == "" {
		libDir = "/tmp/kernelprobe/lib"
	}
	workDir := "/tmp/kernelprobe/sizetest"
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(workDir, "go.mod"), []byte("module sizetest\n\ngo 1.25\n"), 0o644); err != nil {
		return nil, err
	}

	cTouch := `
#include "` + filepath.Join(repoRoot, "internal/experimental/kernelprobe/native/include/kp_abi.h") + `"
#include <stdint.h>
static uintptr_t kp_link_anchor(void) {
    return (uintptr_t)&kp_new ^ (uintptr_t)&kp_destroy ^ (uintptr_t)&kp_eval ^
           (uintptr_t)&kp_restore ^ (uintptr_t)&kp_snapshot ^ (uintptr_t)&kp_cut ^
           (uintptr_t)&kp_kill ^ (uintptr_t)&kp_free;
}
`

	type variant struct {
		label      string
		cgo        string
		dir        string
		target     string
		mainSrc    string
		sharedPath string
		staticPath string
		note       string
	}
	variants := []variant{
		{
			label:  "baseline_purego",
			cgo:    "0",
			dir:    workDir,
			target: "./base0",
			mainSrc: `package main
import ("encoding/json"; "fmt"; "os"; "sync")
func main() { var mu sync.Mutex; mu.Lock(); mu.Unlock(); b, _ := json.Marshal(len(os.Args)); fmt.Println(string(b)) }
`,
			note: "CGO_ENABLED=0 minimal Go binary (encoding/json + sync + fmt)",
		},
		{
			label:  "baseline_cgo",
			cgo:    "1",
			dir:    workDir,
			target: "./basecgo",
			mainSrc: `package main
/*
#include <stdlib.h>
*/
import "C"
import ("encoding/json"; "fmt"; "os"; "sync")
func main() { var mu sync.Mutex; mu.Lock(); mu.Unlock(); b, _ := json.Marshal(len(os.Args)); fmt.Println(string(b), C.atoi(nil)) }
`,
			note: "CGO_ENABLED=1 minimal Go+cgo binary (baseline before linking VM)",
		},
		{
			label:  "ergoscript_cmd_purego",
			cgo:    "0",
			dir:    repoRoot,
			target: "./internal/experimental/kernelprobe/cmd/kernelprobe",
			note:   "CGO_ENABLED=0 full kernelprobe CLI + ergoscript AST interpreter (includes internal/sketch/syntax)",
		},
		{
			label:  "ergoscript_cmd_cgo_dlopen",
			cgo:    "1",
			dir:    repoRoot,
			target: "./internal/experimental/kernelprobe/cmd/kernelprobe",
			note:   "CGO_ENABLED=1 full kernelprobe CLI + dlopen bridge (no VM baked in)",
		},
		{
			label:      "luau_static_cgo",
			cgo:        "1",
			dir:        workDir,
			target:     "./luau_static",
			sharedPath: filepath.Join(libDir, "libluau_c.so"),
			staticPath: filepath.Join(libDir, "libluau_c.a"),
			mainSrc: `package main
/*
#cgo LDFLAGS: ` + filepath.Join(libDir, "libluau_c.a") + ` -lstdc++ -lm
` + cTouch + `
*/
import "C"
import ("encoding/json"; "fmt"; "os"; "sync")
func main() { var mu sync.Mutex; mu.Lock(); mu.Unlock(); b, _ := json.Marshal(len(os.Args)); fmt.Println(string(b), uint64(C.kp_link_anchor())) }
`,
			note: "CGO_ENABLED=1 baseline + static libluau_c.a (-lstdc++ -lm)",
		},
		{
			label:  "ergo_cli_purego",
			cgo:    "0",
			dir:    repoRoot,
			target: "./cmd/ergo",
			note:   "CGO_ENABLED=0 existing Ergo CLI (already embeds internal/sketch/syntax)",
		},
		{
			label:      "psychoscript_static_cgo",
			cgo:        "1",
			dir:        workDir,
			target:     "./psycho_static",
			sharedPath: filepath.Join(libDir, "libpsychoscript_c.so"),
			staticPath: filepath.Join(libDir, "libpsychoscript_c.a"),
			mainSrc: `package main
/*
#cgo LDFLAGS: ` + filepath.Join(workDir, "libpsycho_nolto.a") + ` -lm -ldl -lpthread
` + cTouch + `
*/
import "C"
import ("encoding/json"; "fmt"; "os"; "sync")
func main() { var mu sync.Mutex; mu.Lock(); mu.Unlock(); b, _ := json.Marshal(len(os.Args)); fmt.Println(string(b), uint64(C.kp_link_anchor())) }
`,
			note: "CGO_ENABLED=1 baseline + static libpsychoscript_c.a (objcopy .llvmbc stripped for Go cgo)",
		},
	}

	if srcA := filepath.Join(libDir, "libpsychoscript_c.a"); fileExists(srcA) {
		dstA := filepath.Join(workDir, "libpsycho_nolto.a")
		if raw, err := os.ReadFile(srcA); err == nil {
			_ = os.WriteFile(dstA, raw, 0o644)
			_ = exec.Command("objcopy", "--remove-section=.llvmbc", "--remove-section=.llvmcmd", dstA).Run()
		}
	}

	var entries []sizeEntry
	var baselineStripped int64
	for _, v := range variants {
		if v.staticPath != "" {
			if _, err := os.Stat(v.staticPath); err != nil {
				continue
			}
		}
		outDir := filepath.Join(workDir, v.label)
		_ = os.MkdirAll(outDir, 0o755)
		if v.mainSrc != "" {
			pkgDir := filepath.Join(workDir, strings.TrimPrefix(v.target, "./"))
			_ = os.MkdirAll(pkgDir, 0o755)
			if err := os.WriteFile(filepath.Join(pkgDir, "main.go"), []byte(v.mainSrc), 0o644); err != nil {
				return entries, err
			}
		}

		binFull := filepath.Join(outDir, "bin_full")
		binStrip := filepath.Join(outDir, "bin_strip")

		for _, item := range []struct {
			out   string
			flags string
		}{
			{binFull, ""},
			{binStrip, "-ldflags=-s -w"},
		} {
			cmdArgs := []string{"build", "-p", "4", "-o", item.out}
			if item.flags != "" {
				cmdArgs = append(cmdArgs, item.flags)
			}
			cmdArgs = append(cmdArgs, v.target)
			cmd := exec.Command("go", cmdArgs...)
			cmd.Dir = v.dir
			cmd.Env = append(os.Environ(), "CGO_ENABLED="+v.cgo, "GOMAXPROCS=4", "GOMEMLIMIT=6GiB")
			out, err := cmd.CombinedOutput()
			if err != nil {
				return entries, fmt.Errorf("size build %s (%s): %v\n%s", v.label, item.out, err, string(out))
			}
		}

		stFull, err := os.Stat(binFull)
		if err != nil {
			return entries, err
		}
		stStrip, err := os.Stat(binStrip)
		if err != nil {
			return entries, err
		}
		if v.label == "baseline_purego" {
			baselineStripped = stStrip.Size()
		}
		var sharedSz, staticSz int64
		if v.sharedPath != "" {
			if fi, err := os.Stat(v.sharedPath); err == nil {
				sharedSz = fi.Size()
			}
		}
		if v.staticPath != "" {
			if fi, err := os.Stat(v.staticPath); err == nil {
				staticSz = fi.Size()
			}
		}
		entries = append(entries, sizeEntry{
			Label:       v.label,
			UnstrippedB: stFull.Size(),
			StrippedB:   stStrip.Size(),
			DeltaMiB:    float64(stStrip.Size()-baselineStripped) / (1024 * 1024),
			SharedLibB:  sharedSz,
			StaticLibB:  staticSz,
			Note:        v.note,
		})
	}
	return entries, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
