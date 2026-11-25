package lower

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/toolchain"
)

// TestPkgSplitPreservesGenericTypeParams verifies that pkgsplit never qualifies
// generic type parameters (on TypeSpec.TypeParams, FuncDecl.Type.TypeParams,
// method receivers like Pair[OpenErr, DriveErr], and IR bodies) when the root
// package declares package-scope types with the exact same Go names
// (`OpenErr`, `DriveErr` — matching the P8 std/explore CheckResult collision).
//
// Mutant: pkgsplit-qualifies-generic-tparam.
func TestPkgSplitPreservesGenericTypeParams(t *testing.T) {
	const modelSrc = `type Choice[OpenErr, DriveErr] = Left{A OpenErr} | Right{B DriveErr}

type Pair[OpenErr, DriveErr] { First OpenErr, Second DriveErr }

func Swap[OpenErr, DriveErr](p Pair[OpenErr, DriveErr]) Pair[DriveErr, OpenErr] {
	Pair[DriveErr, OpenErr]{First: p.Second, Second: p.First}
}

func Pick[OpenErr, DriveErr](c Choice[OpenErr, DriveErr], fallback OpenErr) OpenErr {
	match c {
	case Left{A}:
		A
	case Right:
		fallback
	}
}
`
	const rootSrc = `import example.com/tparamtest/model

type OpenErr { Code int }
type DriveErr { Msg string }

func main() needs Console {
	let p = model.Swap[int, string](model.Pair[int, string]{First: 7, Second: "ok"})
	let c model.Choice[int, string] = model.Right[string]{B: "x"}
	let n = model.Pick[int, string](c, 42)
	let o = OpenErr{Code: 9}
	let d = DriveErr{Msg: "hi"}
	Console.Println("\(p.First):\(p.Second):\(n)|\(o.Code):\(d.Msg)")
}
`
	// 1. In-process LowerIRSplit check (strict IR, zero fallback).
	u := splitProgram{
		libs: []pkgSource{{"example.com/tparamtest/model", modelSrc}},
		root: rootSrc,
	}.check(t)
	u.NoFallback = true
	files, irRep, splitRep, err := LowerIRSplit(u, false)
	if err != nil {
		t.Fatalf("LowerIRSplit(NoFallback=true): %v", err)
	}
	if len(irRep.Fallback) > 0 {
		t.Fatalf("LowerIRSplit had fallbacks: %v", irRep.Fallback)
	}
	if splitRep.Reason != "" || splitRep.Packages < 2 {
		t.Fatalf("program did not split cleanly across packages: %+v", splitRep)
	}
	// Verify the exact collision setup: main.go declares package-scope
	// OpenErr/DriveErr while model.go declares generic method receiver
	// Pair[OpenErr, DriveErr] and generic function Pick[OpenErr any, DriveErr any],
	// neither qualified nor pulled into main.
	foundModelMethod := false
	foundMainOpenErr := false
	var summary strings.Builder
	for _, f := range files {
		if !strings.HasSuffix(f.Path, ".go") {
			continue
		}
		text := string(f.Data)
		for _, line := range strings.Split(text, "\n") {
			if strings.Contains(line, "OpenErr") || strings.Contains(line, "Swap(") {
				summary.WriteString(f.Path + ": " + line + "\n")
			}
			if (strings.Contains(line, "Pair[") || strings.Contains(line, "Pick[")) && (strings.Contains(line, ".OpenErr") || strings.Contains(line, ".DriveErr")) {
				if strings.HasPrefix(line, "type ") || strings.HasPrefix(line, "func ") {
					t.Fatalf("pkgsplit qualified a generic type parameter in %s:\n%s", f.Path, line)
				}
			}
		}
		if strings.HasSuffix(f.Path, "/model.go") && strings.Contains(text, "Pair[OpenErr, DriveErr]) Swap()") {
			foundModelMethod = true
		}
		if f.Path == "main.go" && strings.Contains(text, "type OpenErr struct") {
			foundMainOpenErr = true
		}
	}
	if !foundMainOpenErr {
		t.Fatalf("test setup error: expected main.go to declare package-scope type OpenErr struct:\n%s", summary.String())
	}
	if !foundModelMethod {
		t.Fatalf("expected Pair[OpenErr, DriveErr].Swap to remain in model.go without qualification (%+v):\n%s", splitRep, summary.String())
	}

	// 2. End-to-end native compile + run via ergo CLI under ERGO_FALLBACK=off.
	needsBuild(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "model"), 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, data := range map[string]string{
		"ergo.mod":         "module example.com/tparamtest\n",
		"model/model.ergo": modelSrc,
		"main.ergo":        rootSrc,
	} {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := exec.Command(ergoCLI(t), "run", ".")
	run.Dir = dir
	run.Env = append(envWithout("GOCOVERDIR"), "ERGO_FALLBACK=off")
	var out bytes.Buffer
	run.Stdout, run.Stderr = &out, &out
	if err := runProgram(t, run); err != nil {
		t.Fatalf("ergo run (ERGO_FALLBACK=off): %v\n%s", err, out.String())
	}
	got := out.String()
	if note, rest, ok := strings.Cut(got, "\n"); ok && strings.HasPrefix(note, toolchain.BuildNote) {
		got = rest
	}
	if got, want := strings.TrimSpace(got), "ok:7:42|9:hi"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func envWithout(key string) []string {
	prefix := key + "="
	var out []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return out
}
