package selfhostcheck

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/budget"
	"ergo.local/prototype/internal/ergoversion"
	"ergo.local/prototype/internal/gocmd"
	"ergo.local/prototype/internal/opcount"
	"ergo.local/prototype/internal/pinset"
	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/producer"
	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/internal/toolchain"
)

func toolDump(t *testing.T, ctx context.Context) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "tooldump")
	if out, err := runCompiler(t, ctx, ergoCLI(t, ctx), "build", "-o", bin, "./cmd/tooldump"); err != nil {
		t.Fatalf("ergo build ./cmd/tooldump: %v\n%s", err, out)
	}
	return bin
}

func runToolDumpReq(t *testing.T, ctx context.Context, bin string, payload any) []byte {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	reqFile := filepath.Join(t.TempDir(), "req.json")
	if err := os.WriteFile(reqFile, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, bin, "-req", reqFile)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("tooldump -req: %v\n%s", err, out)
	}
	return out
}

// TestCompilerToolchainLawsRun executes fmt, lint, tests, and laws for the
// pure-Ergo toolchain/runtime packages with ERGO_FALLBACK=off.
func TestCompilerToolchainLawsRun(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	ergo := ergoCLI(t, ctx)
	pkgs := []string{"./proclimit", "./budget", "./producer", "./toolchain", "./gocmd", "./cmd/tooldump"}
	for _, pkg := range pkgs {
		out, err := runCompilerWithFallbackOff(t, ctx, ergo, "test", "--fresh", pkg)
		if err != nil {
			t.Fatalf("ERGO_FALLBACK=off ergo test --fresh %s: %v\n%s", pkg, err, out)
		}
		if !strings.Contains(out, "PASS") {
			t.Fatalf("ergo test %s produced no PASS:\n%s", pkg, out)
		}
	}
}

func runCompilerWithFallbackOff(t *testing.T, ctx context.Context, prog string, args ...string) (string, error) {
	t.Helper()
	limit, err := proclimit.Limit(os.Environ(), proclimit.DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, prog, args...)
	cmd.Dir = filepath.Join(repoRoot(), "compiler")
	cmd.Env = append(os.Environ(), "ERGO_FALLBACK=off")
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	err = proclimit.Run(cmd, limit)
	return out.String(), err
}

// TestCompilerToolchainAgrees verifies pure-Ergo compiler/{proclimit,budget,producer,toolchain,gocmd}
// against Go internal/{proclimit,budget,producer,opcount,toolchain,gocmd} across fixture cases,
// seeded scenarios, and the live repository environment.
func TestCompilerToolchainAgrees(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	bin := toolDump(t, ctx)

	t.Run("proclimit", func(t *testing.T) {
		testAgreeProclimit(t, ctx, bin)
	})
	t.Run("budget", func(t *testing.T) {
		testAgreeBudget(t, ctx, bin)
	})
	t.Run("opcount", func(t *testing.T) {
		testAgreeOpcount(t, ctx, bin)
	})
	t.Run("producer", func(t *testing.T) {
		testAgreeProducer(t, ctx, bin)
	})
	t.Run("toolchain", func(t *testing.T) {
		testAgreeToolchain(t, ctx, bin)
	})
	t.Run("gocmd", func(t *testing.T) {
		testAgreeGoCmd(t, ctx, bin)
	})
}

func testAgreeProclimit(t *testing.T, ctx context.Context, bin string) {
	sizeInputs := []string{"0", "off", "2 GiB", "2GiB", "512 MiB", "1024KiB", "4096", "0 bytes", "17179869184 GiB", "-5", ""}
	env := []string{"FOO=bar", "ERGO_PROGRAM_MEMORY=invalid", "ERGO_PROGRAM_MEMORY=512 MiB"}
	chunks := []string{"starting...\n", "SIGSEGV: segmentation violation\n", "PC=0x42 m=3 ", "addr=0x0\n\n", "goroutine 0 [idle]:\n", "runtime.(*spanQueue).tryDrain()\n"}
	memInfo := "MemTotal:       33554432 kB\nMemFree:        1048576 kB\nMemAvailable:   16777216 kB\n"

	raw := runToolDumpReq(t, ctx, bin, map[string]any{
		"op":            "proclimit",
		"sizes":         sizeInputs,
		"env":           env,
		"default_limit": proclimit.DefaultLimit,
		"chunks":        chunks,
		"cpu_max":       "600000 100000\n",
		"mem_max":       strconv.FormatUint(8<<30, 10),
		"self_cgroup":   "0::/user.slice/ergo.scope\n",
		"cpu_online":    "0-15\n",
		"meminfo":       memInfo,
	})
	var resp struct {
		Sizes []struct {
			Ok        bool   `json:"ok"`
			Val       uint64 `json:"val"`
			Formatted string `json:"formatted"`
			Err       string `json:"err"`
		} `json:"sizes"`
		Limit struct {
			Ok  bool   `json:"ok"`
			Val uint64 `json:"val"`
			Err string `json:"err"`
		} `json:"limit"`
		StreamCause   uint8  `json:"stream_cause"`
		FullCause     uint8  `json:"full_cause"`
		WatchedStderr string `json:"watched_stderr"`
		CheckExit     string `json:"check_exit"`
		Quota         struct {
			CgroupRel     string   `json:"cgroup_rel"`
			EffectiveCPUs uint32   `json:"effective_cpus"`
			EffectiveRAM  uint64   `json:"effective_ram"`
			Workers       uint32   `json:"workers"`
			MaxProcs      uint32   `json:"max_procs"`
			GoEnv         []string `json:"go_env"`
		} `json:"quota"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	for i, s := range sizeInputs {
		wantV, err := proclimit.ParseSize(s)
		got := resp.Sizes[i]
		if got.Ok != (err == nil) {
			t.Errorf("ParseSize(%q): ok=%v, err=%v (%s)", s, got.Ok, err, got.Err)
			continue
		}
		if err == nil {
			if got.Val != wantV || got.Formatted != proclimit.Format(wantV) {
				t.Errorf("ParseSize(%q): got (%d,%q), want (%d,%q)", s, got.Val, got.Formatted, wantV, proclimit.Format(wantV))
			}
		} else if got.Err != err.Error() {
			t.Errorf("ParseSize(%q) err=%q, want %q", s, got.Err, err.Error())
		}
	}
	wantLim, limErr := proclimit.Limit(env, proclimit.DefaultLimit)
	if !resp.Limit.Ok || limErr != nil || resp.Limit.Val != wantLim {
		t.Fatalf("Limit got (%v,%d,%q), want (%d,%v)", resp.Limit.Ok, resp.Limit.Val, resp.Limit.Err, wantLim, limErr)
	}
	wantBad, badErr := proclimit.Limit([]string{"ERGO_PROGRAM_MEMORY=nope"}, proclimit.DefaultLimit)
	_ = wantBad
	badRespRaw := runToolDumpReq(t, ctx, bin, map[string]any{
		"op":            "proclimit",
		"sizes":         []string{},
		"env":           []string{"ERGO_PROGRAM_MEMORY=nope"},
		"default_limit": proclimit.DefaultLimit,
		"chunks":        []string{"cannot alloc", "ate memory\n"},
		"meminfo":       memInfo,
	})
	var badResp struct {
		Limit struct {
			Ok  bool   `json:"ok"`
			Err string `json:"err"`
		} `json:"limit"`
		FullCause uint8 `json:"full_cause"`
	}
	if err := json.Unmarshal(badRespRaw, &badResp); err != nil {
		t.Fatal(err)
	}
	if badResp.Limit.Ok || badErr == nil || badResp.Limit.Err != badErr.Error() {
		t.Errorf("bad Limit err=%q, want %q", badResp.Limit.Err, badErr)
	}
	if proclimit.Cause(badResp.FullCause) != proclimit.RuntimeReport {
		t.Errorf("split mapping refused cause=%d, want RuntimeReport", badResp.FullCause)
	}
	wantCause := proclimit.CauseOf([]byte(strings.Join(chunks, "")))
	if proclimit.Cause(resp.StreamCause) != wantCause || proclimit.Cause(resp.FullCause) != wantCause {
		t.Errorf("QueueFault cause mismatch: stream=%d full=%d want=%d", resp.StreamCause, resp.FullCause, wantCause)
	}
	wantLE := (&proclimit.LimitError{Program: "prog", Limit: proclimit.DefaultLimit, Cause: wantCause, Err: execErrPlaceholder{}}).Error()
	if !strings.HasPrefix(resp.CheckExit, strings.TrimSuffix(wantLE, "exit status 2")) || !strings.HasSuffix(resp.CheckExit, "exit status 2") {
		t.Errorf("CheckExit format = %q, want matching LimitError prefix", resp.CheckExit)
	}
	if resp.Quota.CgroupRel != "user.slice/ergo.scope" || resp.Quota.EffectiveCPUs != 6 || resp.Quota.EffectiveRAM != 8<<30 || resp.Quota.Workers != 4 {
		t.Errorf("unexpected derived quota: %+v", resp.Quota)
	}

	probeOut, err := exec.CommandContext(ctx, bin, "-probe").CombinedOutput()
	if err != nil {
		t.Fatalf("tooldump -probe: %v\n%s", err, probeOut)
	}
	var probe struct {
		EffectiveCPUs uint32   `json:"effective_cpus"`
		EffectiveRAM  uint64   `json:"effective_ram"`
		Workers       uint32   `json:"workers"`
		GoEnv         []string `json:"go_env"`
	}
	if err := json.Unmarshal(probeOut, &probe); err != nil {
		t.Fatal(err)
	}
	if probe.EffectiveCPUs == 0 || probe.Workers == 0 || len(probe.GoEnv) == 0 {
		t.Errorf("live probe returned invalid quota: %s", probeOut)
	}
}

type execErrPlaceholder struct{}

func (execErrPlaceholder) Error() string { return "exit status 2" }

func testAgreeBudget(t *testing.T, ctx context.Context, bin string) {
	cfgText := "# host config\ncores 12\nmemory 24G\ndefault-memory 32G\nshare 4\nbackfill 2\ngate-load 0\nstate my.state\nlog my.log\n"
	cfgPath := filepath.Join(t.TempDir(), "budget.conf")
	if err := os.WriteFile(cfgPath, []byte(cfgText), 0o644); err != nil {
		t.Fatal(err)
	}
	goCfg, err := budget.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	seed := selfhostSeed(t)
	rng := rand.New(rand.NewPCG(seed, 0xb0d9e7))
	peaks := map[string]int64{"k1": 2 << 30, "k2": 6 << 30}
	var goState budget.State
	goRules := budget.Rules{
		Cores: 8, Memory: 16 << 30, Share: 4, Backfill: 2, Default: 4 << 30,
		Peak: func(k string) int64 { return peaks[k] },
	}
	var actions []map[string]any
	for i := range 20 {
		if i > 0 && rng.IntN(3) == 0 && len(goState.Holders)+len(goState.Waiters) > 0 {
			var id string
			if len(goState.Holders) > 0 {
				id = goState.Holders[rng.IntN(len(goState.Holders))].ID
			} else {
				id = goState.Waiters[rng.IntN(len(goState.Waiters))].ID
			}
			goState.Remove(goRules, id)
			actions = append(actions, map[string]any{"op": "remove", "id": id})
			continue
		}
		e := budget.Entry{
			ID:      "e" + strconv.Itoa(i),
			Pid:     100 + i,
			Width:   1 + rng.IntN(6),
			Crit:    rng.IntN(2) == 0,
			Lane:    "lane-" + strconv.Itoa(rng.IntN(3)),
			Key:     []string{"k1", "k2", "k3"}[rng.IntN(3)],
			Ceiling: []int64{0, 5 << 30}[rng.IntN(2)],
		}
		eb, _ := json.Marshal(e)
		goState.Join(goRules, e)
		actions = append(actions, map[string]any{"op": "join", "entry_json": string(eb)})
	}
	peaksJSON, _ := json.Marshal(peaks)
	wd, _ := os.Getwd()
	argv := []string{filepath.Join(wd, "cmd"), "--flag", wd}

	raw := runToolDumpReq(t, ctx, bin, map[string]any{
		"op":             "budget",
		"config_text":    cfgText,
		"config_path":    cfgPath,
		"cores":          8,
		"memory":         uint64(16 << 30),
		"share":          4,
		"backfill":       2,
		"default_memory": uint64(4 << 30),
		"state_json":     `{"holders":[],"waiters":[],"next_ticket":0}`,
		"peaks_json":     string(peaksJSON),
		"actions":        actions,
		"argv":           argv,
		"wd":             wd,
		"peak_text":      "4294967296\n1\n",
	})
	var resp struct {
		CfgOk         bool   `json:"cfg_ok"`
		CfgCores      uint32 `json:"cfg_cores"`
		CfgMemory     uint64 `json:"cfg_memory"`
		CfgDefaultMem uint64 `json:"cfg_default_memory"`
		StateJSON     string `json:"state_json"`
		Why           string `json:"why"`
		CommandKey    string `json:"command_key"`
		KeptPeak      uint64 `json:"kept_peak"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.CfgOk || int(resp.CfgCores) != goCfg.Cores || int64(resp.CfgMemory) != goCfg.Memory || int64(resp.CfgDefaultMem) != goCfg.DefaultMemory {
		t.Errorf("budget config mismatch: %+v vs %+v", resp, goCfg)
	}
	var ergoState budget.State
	if err := json.Unmarshal([]byte(resp.StateJSON), &ergoState); err != nil {
		t.Fatal(err)
	}
	wantStateBytes, _ := json.Marshal(goState)
	var canonGo budget.State
	_ = json.Unmarshal(wantStateBytes, &canonGo)
	if string(mustMarshal(t, ergoState)) != string(mustMarshal(t, canonGo)) {
		t.Errorf("budget state mismatch (seed %d):\nergo=%s\nwant=%s", seed, resp.StateJSON, wantStateBytes)
	}
	if resp.Why != goState.Why(goRules) {
		t.Errorf("budget Why=%q, want %q", resp.Why, goState.Why(goRules))
	}
	if resp.CommandKey != budget.CommandKey(argv) {
		t.Errorf("CommandKey=%q, want %q", resp.CommandKey, budget.CommandKey(argv))
	}
	wantKept := uint64(budget.KeptPeak(4<<30, 1, true, 4<<30))
	if resp.KeptPeak != wantKept {
		t.Errorf("KeptPeak=%d, want %d", resp.KeptPeak, wantKept)
	}
}

func testAgreeOpcount(t *testing.T, ctx context.Context, bin string) {
	syms := []string{
		"ergo.local/prototype/runtime/v1.Make",
		"runtime.gcWriteBarrier",
		"runtime.morestack",
		"runtime.mallocgc",
		"runtime.gcBgMarkWorker",
		"runtime.schedule",
		"runtime.memmove",
		"main.compute",
	}
	wl := "unit op\nsizes 100 1000\nargs run -n {n}\ninput self gen {n}\nprocs 2\n"
	raw := runToolDumpReq(t, ctx, bin, map[string]any{
		"op":              "opcount",
		"symbols":         syms,
		"workload_text":   wl,
		"workload_n":      100,
		"workload_input":  "f.txt",
		"env_period":      "",
		"rate_text":       "100000",
		"cpu_online":      "0,2-4",
		"counter_val":     12345,
		"counter_enabled": 100,
		"counter_running": 90,
	})
	var resp struct {
		Parts     []string `json:"parts"`
		Problems  []string `json:"problems"`
		FromStdin bool     `json:"from_stdin"`
		Expanded  []string `json:"expanded"`
		Period    uint64   `json:"period"`
		CPUCount  uint64   `json:"cpu_count"`
		Decode    struct {
			Refusal string `json:"refusal"`
		} `json:"decode"`
		HookOk bool `json:"hook_ok"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	for i, s := range syms {
		want := opcount.Classify(s).Counter()
		if resp.Parts[i] != want {
			t.Errorf("Classify(%q)=%q, want %q", s, resp.Parts[i], want)
		}
	}
	goW, goProbs := opcount.ParseWorkload(wl)
	if len(resp.Problems) != len(goProbs) || resp.FromStdin != goW.FromStdin() {
		t.Errorf("workload mismatch: probs=%v/%v stdin=%v/%v", resp.Problems, goProbs, resp.FromStdin, goW.FromStdin())
	}
	wantExp := opcount.Expand(goW.Args, 100, "f.txt")
	if strings.Join(resp.Expanded, "\x00") != strings.Join(wantExp, "\x00") {
		t.Errorf("Expand=%v, want %v", resp.Expanded, wantExp)
	}
	if resp.CPUCount != 4 || resp.Decode.Refusal != string(opcount.Multiplexed) || !resp.HookOk {
		t.Errorf("opcount mismatch: %+v", resp)
	}
}

func testAgreeProducer(t *testing.T, ctx context.Context, bin string) {
	info := &debug.BuildInfo{
		GoVersion: "go1.27.4",
		Main:      debug.Module{Path: "ergo.local/prototype", Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "b83aa00f2b4298b8b760bf57370ea94c41c95460"},
			{Key: "vcs.time", Value: "2026-11-27T10:00:00Z"},
			{Key: "vcs.modified", Value: "true"},
			{Key: "GOOS", Value: "linux"},
			{Key: "GOARCH", Value: "amd64"},
			{Key: "CGO_ENABLED", Value: "0"},
		},
	}
	wantVer := ergoversion.Of(info)
	img := []byte("synthetic executing image bytes")
	wantSum := sha256.Sum256(img)
	wantDigest := "sha256:" + hex.EncodeToString(wantSum[:])

	fatImg := []byte{0xca, 0xfe, 0xba, 0xbe, 0, 0, 0, 1}
	var settings []map[string]any
	for _, s := range info.Settings {
		settings = append(settings, map[string]any{"key": s.Key, "value": s.Value})
	}
	raw := runToolDumpReq(t, ctx, bin, map[string]any{
		"op":           "producer",
		"open_err":     "",
		"image_b64":    base64.StdEncoding.EncodeToString(img),
		"read_err":     "",
		"nonce":        "n1",
		"memo_text":    "",
		"image_key":    "1:2:31:10.000000000:10.000000000",
		"has_key":      true,
		"go_version":   info.GoVersion,
		"main_path":    info.Main.Path,
		"main_version": info.Main.Version,
		"settings":     settings,
		"macho_b64":    base64.StdEncoding.EncodeToString(fatImg),
		"cdhash_b64":   base64.StdEncoding.EncodeToString(make([]byte, 20)),
	})
	var resp struct {
		IdentityJSON string `json:"identity_json"`
		MemoText     string `json:"memo_text"`
		Version      string `json:"version"`
		CsOk         bool   `json:"cs_ok"`
		CsErr        string `json:"cs_err"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Version != wantVer {
		t.Errorf("VersionOf=%q, want %q", resp.Version, wantVer)
	}
	var id producer.Identity
	if err := json.Unmarshal([]byte(resp.IdentityJSON), &id); err != nil {
		t.Fatal(err)
	}
	if id.Digest != wantDigest || id.Qualifier != wantDigest || id.Strength != "executing-artifact" || id.Declaration.VCSRevision != "b83aa00f2b4298b8b760bf57370ea94c41c95460" {
		t.Errorf("unexpected producer Identity: %+v", id)
	}
	if err := id.Require("sha256:other"); err == nil {
		t.Errorf("expected stale qualification error")
	}
	if resp.CsOk || resp.CsErr != "universal" {
		t.Errorf("fat binary check = (%v,%q), want (false,universal)", resp.CsOk, resp.CsErr)
	}
}

func testAgreeToolchain(t *testing.T, ctx context.Context, bin string) {
	irDir := filepath.Join(repoRoot(), "toolchain", "ir", "_go")
	wantDerived, err := toolchain.DerivePatch(irDir)
	if err != nil {
		t.Fatalf("Go DerivePatch(%s): %v", irDir, err)
	}
	wantDerivedSum := sha256.Sum256(wantDerived)
	srcFiles := map[string]string{}
	srcRoot := filepath.Join(irDir, "src")
	err = filepath.WalkDir(srcRoot, func(p string, d os.DirEntry, werr error) error {
		if werr != nil || d.IsDir() {
			return werr
		}
		rel, _ := filepath.Rel(irDir, p)
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		srcFiles[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	hookPatch, err := os.ReadFile(filepath.Join(irDir, toolchain.GoFilesPatch))
	if err != nil {
		t.Fatal(err)
	}
	goModBytes, err := os.ReadFile(filepath.Join(repoRoot(), "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	pinLockBytes, err := os.ReadFile(filepath.Join(repoRoot(), "toolchain", "pin", "lock"))
	if err != nil {
		t.Fatal(err)
	}

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	keyBody := append([]byte{1}, pub...)
	kh := sha256.Sum256(append([]byte("testdb.org\n"), keyBody...))
	vkey := "testdb.org+" + hex.EncodeToString(kh[:4]) + "+" + base64.StdEncoding.EncodeToString(keyBody)
	noteText := "go.sum database tree\n10\n" + base64.StdEncoding.EncodeToString(make([]byte, 32)) + "\n"
	sigBytes := append(append([]byte(nil), kh[:4]...), ed25519.Sign(priv, []byte(noteText))...)
	signedNote := noteText + "\n— testdb.org " + base64.StdEncoding.EncodeToString(sigBytes) + "\n"

	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	for name, body := range map[string]string{"m@v1/a.txt": "hello\n", "m@v1/b.txt": "world\n"} {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()

	basePatch := "diff --git a/f.txt b/f.txt\n--- a/f.txt\n+++ b/f.txt\n@@ -1,2 +1,2 @@\n a\n-b\n+B\n"
	baseTree := filepath.Join(t.TempDir(), "tree")
	if err := os.MkdirAll(baseTree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseTree, "f.txt"), []byte("x\na\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	goApplied, err := toolchain.ApplyPatch(baseTree, []byte(basePatch))
	if err != nil {
		t.Fatal(err)
	}

	cache := "/tmp/ergo-cache"
	wantTC, wantInst := toolchain.Status(func(k string) string {
		if k == "ERGO_GO" {
			return "managed"
		}
		return ""
	}, cache)

	raw := runToolDumpReq(t, ctx, bin, map[string]any{
		"op":            "toolchain",
		"ergo_go":       "managed",
		"cache":         cache,
		"goos":          "linux",
		"goarch":        "amd64",
		"installed":     wantInst,
		"patch_text":    basePatch,
		"base_files":    map[string]string{"f.txt": "x\na\nb\n"},
		"src_files":     srcFiles,
		"hook_patch":    string(hookPatch),
		"source":        "/goroot",
		"release":       toolchain.Version,
		"go_cmd_digest": "deadbeef",
		"environ":       []string{"GOROOT=/u", "GOEXPERIMENT=foo", "GO_CUSTOM=abc"},
		"go_mod":        string(goModBytes),
		"vkey":          vkey,
		"note_msg":      signedNote,
		"zip_b64":       base64.StdEncoding.EncodeToString(zbuf.Bytes()),
		"zip_prefix":    "m@v1/",
		"pin_lock":      string(pinLockBytes),
	})
	var resp struct {
		StatusMode    string            `json:"status_mode"`
		StatusGo      string            `json:"status_go"`
		StatusRoot    string            `json:"status_root"`
		PatchOk       bool              `json:"patch_ok"`
		PatchFiles    map[string]string `json:"patch_files"`
		DeriveOk      bool              `json:"derive_ok"`
		DeriveSHA256  string            `json:"derive_sha256"`
		IRCKey        string            `json:"irc_key"`
		IRCUnknown    []string          `json:"irc_unknown"`
		WithToolchain string            `json:"with_toolchain"`
		Note          struct {
			Ok   bool   `json:"ok"`
			Text string `json:"text"`
			Err  string `json:"err"`
		} `json:"note"`
		Zip struct {
			H1       string `json:"h1"`
			Err      string `json:"err"`
			PinGo    string `json:"pin_go"`
			PinPatch string `json:"pin_patch"`
		} `json:"zip"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.StatusMode != wantTC.Mode || !strings.HasSuffix(resp.StatusRoot, "/release") {
		t.Errorf("Status mismatch: %+v vs %+v", resp, wantTC)
	}
	if !resp.PatchOk || resp.PatchFiles["f.txt"] != string(goApplied.Files["f.txt"]) {
		t.Errorf("ApplyPatch mismatch: got %q, want %q", resp.PatchFiles["f.txt"], goApplied.Files["f.txt"])
	}
	if !resp.DeriveOk || resp.DeriveSHA256 != hex.EncodeToString(wantDerivedSum[:]) {
		t.Errorf("real toolchain/ir DerivePatch SHA256=%q, want %q (err=%s)", resp.DeriveSHA256, hex.EncodeToString(wantDerivedSum[:]), resp.StatusMode)
	}
	if len(resp.IRCUnknown) != 1 || resp.IRCUnknown[0] != "GO_CUSTOM" || len(resp.IRCKey) != 64 {
		t.Errorf("IRCInputs mismatch: key=%q unk=%v", resp.IRCKey, resp.IRCUnknown)
	}
	if !strings.Contains(resp.WithToolchain, "toolchain "+toolchain.Version) {
		t.Errorf("real go.mod WithToolchain lost toolchain line")
	}
	if !resp.Note.Ok || resp.Note.Text != noteText {
		t.Errorf("Ed25519 VerifyNote failed: %+v", resp.Note)
	}
	goLock, err := pinset.ParseLock(pinLockBytes)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Zip.Err != "" || !strings.HasPrefix(resp.Zip.H1, "h1:") || resp.Zip.PinGo != goLock.Go || resp.Zip.PinPatch != goLock.Patch {
		t.Errorf("Zip/PinLock mismatch: %+v vs %+v", resp.Zip, goLock)
	}
}

func testAgreeGoCmd(t *testing.T, ctx context.Context, bin string) {
	tmp := t.TempDir()
	cache := filepath.Join(tmp, "cache")
	modRoot := filepath.Join(tmp, "mod")
	subDir := filepath.Join(modRoot, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	goMod := "module example.com/m\n\ngo 1.27\n"
	if err := os.WriteFile(filepath.Join(modRoot, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{"test", "-v", "-gcflags=./sub=-N -l", "./sub"}
	goflags := "-gcflags=all=-d=0"
	goLine, err := gocmd.Parse(args, goflags)
	if err != nil {
		t.Fatal(err)
	}
	m := gocmd.Module{Root: modRoot, Path: "example.com/m", GoMod: []byte(goMod)}
	rtFiles := []gocmd.File{{Path: "ergo/go.mod", Data: []byte("module ergo.local/prototype\n\ngo 1.27\n")}}
	eps := []gocmd.ErgoPackage{{Package: gocmd.Package{ImportPath: "example.com/m/sub", Dir: subDir}, Name: "sub"}}
	emitted := []gocmd.File{
		{Path: "sub/sub.go", Data: []byte("package sub\n")},
		{Path: "sub/ergoir.json", Data: []byte("{}")},
	}
	plan, err := gocmd.Assemble(cache, m, goLine, modRoot, rtFiles, eps, emitted)
	if err != nil {
		t.Fatal(err)
	}
	var modDirArg string
	for _, a := range plan.Args {
		if after, ok := strings.CutPrefix(a, "-modfile="); ok {
			modDirArg = filepath.Dir(after)
		}
	}

	raw := runToolDumpReq(t, ctx, bin, map[string]any{
		"op":          "gocmd",
		"args":        args,
		"goflags":     goflags,
		"cwd":         modRoot,
		"pattern":     "./sub",
		"import_path": "example.com/m/sub",
		"dir":         subDir,
		"cache":       cache,
		"mod_root":    modRoot,
		"mod_path":    m.Path,
		"mod_go":      goMod,
		"mod_dir":     modDirArg,
		"rt_path":     "ergo/go.mod",
		"rt_data":     "module ergo.local/prototype\n\ngo 1.27\n",
		"ep_dir":      subDir,
		"ep_import":   "example.com/m/sub",
		"ep_name":     "sub",
		"files": []map[string]string{
			{"path": "sub/sub.go", "data": "package sub\n"},
			{"path": "sub/ergoir.json", "data": "{}"},
		},
	})
	var resp struct {
		ParseOk  bool     `json:"parse_ok"`
		Packages []string `json:"packages"`
		Matches  bool     `json:"matches"`
		PFlagsOk bool     `json:"pflags_ok"`
		PFlags   []string `json:"pflags"`
		AsmOk    bool     `json:"asm_ok"`
		PlanArgs []string `json:"plan_args"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.ParseOk || !resp.Matches || !resp.PFlagsOk || !resp.AsmOk {
		t.Fatalf("gocmd failed: %+v", resp)
	}
	if len(resp.PFlags) != 2 || resp.PFlags[0] != "-N" || resp.PFlags[1] != "-l" {
		t.Errorf("PackageFlags=%v, want [-N -l]", resp.PFlags)
	}
	if strings.Join(resp.PlanArgs, "\x00") != strings.Join(plan.Args, "\x00") {
		t.Errorf("Assemble plan args differ:\nergo=%q\ngo  =%q", resp.PlanArgs, plan.Args)
	}
	_ = binary.LittleEndian
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
