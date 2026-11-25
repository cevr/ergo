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

// TestErgoBindCFixtureEndToEnd proves the complete CFFI-2 vertical slice:
//  1. `ergo bind c fixture.h fixture.bind.json` generates both the CGO_ENABLED=0
//     purego Go package and the checked Ergo binding under ergo.bind/.
//  2. A real `.ergo` program imports the bound C package with zero hand-written
//     Go wrapper, calls Load, ContextCreate, NodeCreate, NodeSetBox, NodeGetBox,
//     NodeSetName, NodeReadName, ShapeArea, BoxZ, NodeCount, explicit
//     ContextDestroy, and automatic scope-exit Close() against real libfixture.so
//     with CGO_ENABLED=0.
//  3. `ergo check` refuses use-after-destroy of Context (S108) and calling C
//     from a pure function without `needs go.Foreign` (S102).
func TestErgoBindCFixtureEndToEnd(t *testing.T) {
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

	// Rewrite fixture.bind.json's go.import_path to live inside our temp module.
	var contract map[string]any
	if err := json.Unmarshal(contractRaw, &contract); err != nil {
		t.Fatal(err)
	}
	goCfg := contract["go"].(map[string]any)
	goCfg["import_path"] = "example.test/cffi2app/fixture"
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
	write("go.mod", []byte("module example.test/cffi2app\n\ngo 1.25.0\n\nrequire github.com/ebitengine/purego v0.11.3\n"))
	write("go.sum", puregoSum)
	headerPath := write("c/fixture.h", headerSrc)
	contractPath := write("c/fixture.bind.json", contractOut)

	// Compile real libfixture.so from fixture.c.
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

	// Run `ergo bind c c/fixture.h c/fixture.bind.json` inside the temp module.
	t.Chdir(root)
	if err := bindCommand([]string{"c", headerPath, contractPath}); err != nil {
		t.Fatalf("ergo bind c failed: %v", err)
	}
	// Verify both generated files landed in the expected module paths.
	for _, rel := range []string{
		"fixture/fixture.go",
		"ergo.bind/example.test/cffi2app/fixture/fixture.ergo",
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Fatalf("expected generated file %s: %v", rel, err)
		}
	}

	// Write a real `.ergo` program using the generated C binding directly (zero Go wrapper).
	mainErgo := `import go example.test/cffi2app/fixture
import go os

type CheckFail {
	Msg string,
}

func exerciseAutoClose() fails go.Error | CheckFail needs go.Foreign {
	let ctx = try fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 4, Strict: true})
	let node = try fixture.NodeCreate(ctx, 7)

	let box = fixture.Box{
		Kind: 3,
		Origin: [2]float64{1.5, -2.25},
		Corner: fixture.Point{X: 3, Y: 4},
		Color: [4]uint16{1, 2, 65535, 4},
		Layer: -2,
		Depth: -300,
		Z: 987654321,
		Visible: fixture.Flag(true),
		Age: fixture.Seconds(9.5),
		Name: [16]uint8{101, 114, 103, 111, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
	}
	try fixture.NodeSetBox(ctx, node, box)
	let got = try fixture.NodeGetBox(ctx, node)
	if got.Kind != 3 || got.Z != 987654321 || got.Layer != -2 || got.Depth != -300 {
		return CheckFail{Msg: "box mismatch"}
	}

	try fixture.NodeSetName(ctx, node, []byte{104, 101, 108, 108, 111})
	let buf = []byte{0, 0, 0, 0, 0, 0, 0, 0}
	let (readBuf, n) = try fixture.NodeReadName(ctx, node, buf)
	if n != 5 || readBuf[0] != 104 || readBuf[4] != 111 {
		return CheckFail{Msg: "name roundtrip mismatch"}
	}

	let areaRect = try fixture.ShapeArea(ctx, fixture.ShapeRect{
		Header: fixture.ShapeHeader{Kind: fixture.FX_SHAPE_RECT},
		Width: 6,
		Height: 7,
	})
	if areaRect != 42 {
		return CheckFail{Msg: "ShapeRect area mismatch"}
	}
	let areaDot = try fixture.ShapeArea(ctx, fixture.ShapeDot{
		Header: fixture.ShapeHeader{Kind: fixture.FX_SHAPE_DOT},
		Weight: -9,
	})
	if areaDot != -9 {
		return CheckFail{Msg: "ShapeDot area mismatch"}
	}

	let zNil = try fixture.BoxZ(ctx, None)
	if zNil != -1 {
		return CheckFail{Msg: "BoxZ(None) mismatch"}
	}
	let zSome = try fixture.BoxZ(ctx, Some{Value: go.New(fixture.Box{..box, Z: 555})})
	if zSome != 555 {
		return CheckFail{Msg: "BoxZ(Some) mismatch"}
	}

	let count = try fixture.NodeCount(ctx)
	if count != 1 {
		return CheckFail{Msg: "NodeCount mismatch"}
	}
}

func exerciseExplicitDestroy() fails go.Error | CheckFail needs go.Foreign {
	let ctx2 = try fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 2, Strict: false})
	let count2 = try fixture.NodeCount(ctx2)
	if count2 != 0 {
		return CheckFail{Msg: "ctx2 NodeCount mismatch"}
	}
	let z2 = try fixture.BoxZ(ctx2, None)
	if z2 != -1 {
		return CheckFail{Msg: "ctx2 BoxZ mismatch"}
	}
	try fixture.ContextDestroy(ctx2)
}

func exercise(libPath string) fails go.Error | CheckFail needs go.Foreign {
	try fixture.Load(libPath)
	let abi = try fixture.AbiVersion()
	if abi != fixture.FX_ABI_VERSION {
		return CheckFail{Msg: "abi mismatch"}
	}
	try exerciseAutoClose()
	try exerciseExplicitDestroy()
}

func main() needs Console | go.Foreign | Ambient {
	let lib = os.Getenv("CFFI2_LIB")
	exercise(lib) catch {
	case CheckFail as e:
		Console.Println("FAIL: " + e.Msg)
		return
	case go.Error:
		Console.Println("FAIL: go.Error")
		return
	}
	Console.Println("ok: cffi2 end-to-end passed")
}
`
	write("main.ergo", []byte(mainErgo))

	// Build and run with CGO_ENABLED=0 using the real ergo binary built from this repo.
	ergoBin := filepath.Join(t.TempDir(), "ergo")
	buildErgo := exec.Command(goTool, "build", "-o", ergoBin, ".")
	buildErgo.Dir = filepath.Join(repoRoot, "cmd", "ergo")
	buildErgo.Env = append(os.Environ(), "CGO_ENABLED=0", "GOMODCACHE="+modCache)
	if out, err := buildErgo.CombinedOutput(); err != nil {
		t.Fatalf("build ergo cli: %v\n%s", err, out)
	}

	appBin := filepath.Join(root, "cffi2app")
	buildApp := exec.Command(ergoBin, "build", "-o", appBin, ".")
	buildApp.Dir = root
	buildApp.Env = append(os.Environ(), "CGO_ENABLED=0", "GOMODCACHE="+modCache, "GOWORK=off")
	if out, err := buildApp.CombinedOutput(); err != nil {
		t.Fatalf("ergo build failed: %v\n%s", err, out)
	}

	runApp := exec.Command(appBin)
	runApp.Dir = root
	runApp.Env = append(os.Environ(), "CFFI2_LIB="+libPath)
	out, err := runApp.CombinedOutput()
	if err != nil {
		t.Fatalf("cffi2app failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "ok: cffi2 end-to-end passed") {
		t.Fatalf("unexpected cffi2app output:\n%s", out)
	}

	// Negative check 1: use-after-destroy refused as S108 by `ergo check`.
	badUAF := `import go example.test/cffi2app/fixture

func bad() uint32 fails go.Error needs go.Foreign {
	let ctx = try fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 2, Strict: false})
	try fixture.ContextDestroy(ctx)
	try fixture.NodeCount(ctx)
}
`
	write("bad_uaf/main.ergo", []byte(badUAF))
	chkUAF := exec.Command(ergoBin, "check", "./bad_uaf")
	chkUAF.Dir = root
	chkUAF.Env = append(os.Environ(), "CGO_ENABLED=0", "GOMODCACHE="+modCache, "GOWORK=off")
	uafOut, uafErr := chkUAF.CombinedOutput()
	if uafErr == nil || !strings.Contains(string(uafOut), "S108") {
		t.Fatalf("expected S108 for use-after-destroy, err=%v out:\n%s", uafErr, uafOut)
	}

	// Negative check 2: pure function calling C refused as S102 by `ergo check`.
	badPure := `import go example.test/cffi2app/fixture

func pure() uint32 fails go.Error {
	try fixture.AbiVersion()
}
`
	write("bad_pure/main.ergo", []byte(badPure))
	chkPure := exec.Command(ergoBin, "check", "./bad_pure")
	chkPure.Dir = root
	chkPure.Env = append(os.Environ(), "CGO_ENABLED=0", "GOMODCACHE="+modCache, "GOWORK=off")
	pureOut, pureErr := chkPure.CombinedOutput()
	if pureErr == nil || !strings.Contains(string(pureOut), "S102") {
		t.Fatalf("expected S102 for pure call to C, err=%v out:\n%s", pureErr, pureOut)
	}
}
