package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestErgoBindCCallbacksLive proves the complete CFFI-3 vertical slice against
// real libfixture.so with CGO_ENABLED=0:
//  1. `fixture.ContextWrite` (ScopeCall with C-borrowed `data []byte`): reads
//     and clones chunks safely, returning exact byte counts.
//  2. Typed Ergo failure raised inside `fixture.ContextWrite` tunnels cleanly
//     through C (returning -1 to C, unwinding zero C frames so
//     `fx_counters.returned == fx_counters.started`) and re-surfaces as the
//     typed Ergo failure at the caller (kills cffi3-call-cb-drops-tunnel).
//  3. `fixture.ContextVisit` (ScopeCall with CData `Visit` struct).
//  4. `fixture.NodeSetMeasure` + `fixture.NodeMeasure` (ScopeRegistered with
//     `[2]float32` out-array), checking `LiveCallbacks()` == 1 while registered
//     and 0 after destroy.
//  5. `ergo check` refuses escaping the borrowed `data []byte` slice (S108).
func TestErgoBindCCallbacksLive(t *testing.T) {
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no C compiler (cc) on PATH")
	}
	goTool := filepath.Join(runtime.GOROOT(), "bin", "go")
	modCache := ""
	for _, c := range []string{os.Getenv("GOMODCACHE")} {
		if c == "" {
			continue
		}
		if st, err := os.Stat(filepath.Join(c, "github.com", "ebitengine", "purego@v0.11.3")); err == nil && st.IsDir() {
			modCache = c
			break
		}
	}
	if modCache == "" {
		if out, err := exec.Command(goTool, "env", "GOMODCACHE").Output(); err == nil {
			c := strings.TrimSpace(string(out))
			if st, err := os.Stat(filepath.Join(c, "github.com", "ebitengine", "purego@v0.11.3")); err == nil && st.IsDir() {
				modCache = c
			}
		}
	}
	if modCache == "" {
		t.Skip("purego@v0.11.3 not cached in GOMODCACHE")
	}

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	fixtureDir := filepath.Join(repoRoot, "internal", "bindc", "testdata", "fixture")
	puregoSum, err := os.ReadFile(filepath.Join(repoRoot, "internal", "bindc", "testdata", "purego.sum"))
	if err != nil {
		t.Fatal(err)
	}
	headerSrc, err := os.ReadFile(filepath.Join(fixtureDir, "fixture.h"))
	if err != nil {
		t.Fatal(err)
	}
	contractRaw, err := os.ReadFile(filepath.Join(fixtureDir, "fixture.bind.json"))
	if err != nil {
		t.Fatal(err)
	}

	var contract map[string]any
	if err := json.Unmarshal(contractRaw, &contract); err != nil {
		t.Fatal(err)
	}
	goCfg := contract["go"].(map[string]any)
	goCfg["import_path"] = "example.test/cffi3app/fixture"
	contractOut, err := json.MarshalIndent(contract, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	write := func(rel string, data []byte) string {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("ergo.mod", []byte("module app\n"))
	write("go.mod", []byte("module example.test/cffi3app\n\ngo 1.25.0\n\nrequire github.com/ebitengine/purego v0.11.3\n"))
	write("go.sum", puregoSum)
	headerPath := write("c/fixture.h", headerSrc)
	contractPath := write("c/fixture.bind.json", contractOut)

	ext := ".so"
	if runtime.GOOS == "darwin" {
		ext = ".dylib"
	}
	libPath := filepath.Join(root, "libfixture"+ext)
	cSrc := filepath.Join(fixtureDir, "fixture.c")
	ccCmd := exec.Command(cc, "-std=c11", "-shared", "-fPIC", "-o", libPath, cSrc, "-lm", "-lpthread")
	if out, err := ccCmd.CombinedOutput(); err != nil {
		t.Fatalf("compile libfixture: %v\n%s", err, out)
	}

	t.Chdir(root)
	if err := bindCommand([]string{"c", headerPath, contractPath}); err != nil {
		t.Fatalf("ergo bind c failed: %v", err)
	}

	mainErgo := `import go example.test/cffi3app/fixture
import go os

type CheckFail {
	Msg string,
}

type Boom {
	Code int,
}

func runCallbacks(libPath string) fails go.Error | CheckFail needs go.Foreign {
	try fixture.Load(libPath)
	let ctx = try fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 8, Strict: true})

	let payload = []byte{65, 66, 67, 68, 69, 70}
	let mut cloned = []byte{}
	let mut chunkCalls = 0
	let written = try fixture.ContextWrite(ctx, payload, 2, func(data []byte) int64 {
		chunkCalls = chunkCalls + 1
		let copyChunk = data.Clone()
		for _, b in copyChunk {
			cloned = cloned.Append(b)
		}
		int64(len(data))
	})
	if written != 6 || chunkCalls != 3 || len(cloned) != 6 || cloned[0] != 65 || cloned[5] != 70 {
		return CheckFail{Msg: "ContextWrite happy path mismatch"}
	}

	let mut failCalls = 0
	let mut caughtCode = 0
	let _ = fixture.ContextWrite(ctx, payload, 2, func(data []byte) int64 fails Boom {
		failCalls = failCalls + 1
		if failCalls == 2 {
			return Boom{Code: 42}
		}
		int64(len(data))
	}) catch {
	case Boom as b:
		caughtCode = b.Code
		uint32(0)
	case go.Error:
		return CheckFail{Msg: "ContextWrite returned go.Error instead of tunneled Boom"}
	}
	if caughtCode != 42 || failCalls != 2 {
		return CheckFail{Msg: "ContextWrite typed tunnel mismatch"}
	}
	let c1 = try fixture.ContextCounters(ctx)
	if c1.Started != c1.Returned || c1.Started != 5 || c1.WriteResults != 7 {
		return CheckFail{Msg: "ContextWrite C counters mismatch after tunnel"}
	}

	let n1 = try fixture.NodeCreate(ctx, 1)
	let n2 = try fixture.NodeCreate(ctx, 2)
	let mut visited = 0
	try fixture.ContextVisit(ctx, func(v fixture.Visit) {
		let _ = v.Ticket
		visited = visited + 1
	})
	if visited != 2 {
		return CheckFail{Msg: "ContextVisit count mismatch"}
	}

	let scale float32 = 2.0
	try fixture.NodeSetMeasure(ctx, n1, func(w float32, wm uint32, h float32, hm uint32) [2]float32 {
		[2]float32{w * scale, h + float32(5.0)}
	})
	if fixture.LiveCallbacks() != 1 {
		return CheckFail{Msg: "LiveCallbacks expected 1 after NodeSetMeasure"}
	}
	let pt = try fixture.NodeMeasure(ctx, n1, float32(10.0), float32(3.0))
	if pt.X != float32(20.0) || pt.Y != float32(8.0) {
		return CheckFail{Msg: "NodeMeasure result mismatch"}
	}

	try fixture.NodeDestroy(ctx, n1)
	try fixture.NodeDestroy(ctx, n2)
	if fixture.LiveCallbacks() != 0 {
		return CheckFail{Msg: "LiveCallbacks expected 0 after NodeDestroy"}
	}

	let c2 = try fixture.ContextCounters(ctx)
	if c2.Started != c2.Returned {
		return CheckFail{Msg: "final C counters mismatch"}
	}
	try fixture.ContextDestroy(ctx)
	if fixture.LiveCallbacks() != 0 {
		return CheckFail{Msg: "LiveCallbacks expected 0 after ContextDestroy"}
	}
}

func main() needs Console | go.Foreign | Ambient {
	let lib = os.Getenv("CFFI3_LIB")
	runCallbacks(lib) catch {
	case CheckFail as e:
		Console.Println("FAIL: " + e.Msg)
		return
	case go.Error:
		Console.Println("FAIL: go.Error")
		return
	}
	Console.Println("ok: cffi3 callbacks live passed")
}
`
	write("main.ergo", []byte(mainErgo))

	ergoBin := filepath.Join(t.TempDir(), "ergo")
	buildErgo := exec.Command(goTool, "build", "-o", ergoBin, ".")
	buildErgo.Dir = filepath.Join(repoRoot, "cmd", "ergo")
	buildErgo.Env = append(os.Environ(), "CGO_ENABLED=0", "GOMODCACHE="+modCache)
	if out, err := buildErgo.CombinedOutput(); err != nil {
		t.Fatalf("build ergo cli: %v\n%s", err, out)
	}

	appBin := filepath.Join(root, "cffi3app")
	buildApp := exec.Command(ergoBin, "build", "-o", appBin, ".")
	buildApp.Dir = root
	buildApp.Env = append(os.Environ(), "CGO_ENABLED=0", "GOMODCACHE="+modCache, "GOWORK=off")
	if out, err := buildApp.CombinedOutput(); err != nil {
		t.Fatalf("ergo build failed: %v\n%s", err, out)
	}

	runApp := exec.Command(appBin)
	runApp.Dir = root
	runApp.Env = append(os.Environ(), "CFFI3_LIB="+libPath, "GOMAXPROCS=1")
	out, err := runApp.CombinedOutput()
	if err != nil {
		t.Fatalf("cffi3app failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "ok: cffi3 callbacks live passed") {
		t.Fatalf("unexpected cffi3app output:\n%s", out)
	}

	// Verify `ergo check` rejects escaping the C-borrowed `data []byte` slice as S108.
	badEscape := `import go example.test/cffi3app/fixture

func leak() fails go.Error needs go.Foreign {
	let ctx = try fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 2, Strict: true})
	let mut outer = []byte{}
	let _ = try fixture.ContextWrite(ctx, []byte{1, 2}, 4, func(data []byte) int64 {
		outer = data
		int64(len(data))
	})
	let _ = len(outer)
}
`
	write("main.ergo", []byte(badEscape))
	chk := exec.Command(ergoBin, "check", ".")
	chk.Dir = root
	chk.Env = append(os.Environ(), "CGO_ENABLED=0", "GOMODCACHE="+modCache, "GOWORK=off")
	if out, err := chk.CombinedOutput(); err == nil || !strings.Contains(string(out), "S108") {
		t.Fatalf("ergo check should reject escaping C-borrowed slice with S108; err=%v\n%s", err, out)
	}
}
