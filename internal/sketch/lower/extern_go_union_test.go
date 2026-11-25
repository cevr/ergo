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

// TestExternGoStructUnionVariant verifies that an extern go struct from std
// (such as http.InvalidLimits and http.ListenFailed, whose Go types are
// ergohttp.HTTPInvalidLimits / ergohttp.HTTPListenFailed) can be embedded in
// union variants without emitting a duplicate local Go struct
// (`http__InvalidLimits`) that mismatches the runtime Go type in Error()/text
// helpers or union operations.
//
// Exercises:
//  1. a named failure row union wrapping extern go structs (`ServeError` in
//     `fails ServeError`, the exact BOARD-6 pattern, including cross-package
//     `LowerIRSplit` and `Error()` formatting),
//  2. a carried non-row value union wrapping `http.InvalidLimits` (>32B), and
//  3. a generic union wrapping `http.InvalidLimits`.
//
// Strictly lowered via IR (NoFallback: true) and executed natively via
// `ergo run .` with ERGO_FALLBACK=off.
// Mutant: extern-go-union-variant-emits-local.
func TestExternGoStructUnionVariant(t *testing.T) {
	const srvSrc = `import http

type ServeInvalidLimits { Err http.InvalidLimits }
type ServeListenFailed { Err http.ListenFailed }
type ServeLocalFail { Code int }

type ServeError = ServeInvalidLimits | ServeListenFailed | ServeLocalFail

type ValueInvalidLimits { Err http.InvalidLimits }
type ValueOk { Msg string }
type Outcome = ValueInvalidLimits | ValueOk

type GenInvalidLimits { Err http.InvalidLimits }
type GenOk[T] { Val T }
type Res[T] = GenInvalidLimits | GenOk[T]

func Start(which int) int fails ServeError {
	if which == 0 {
		return ServeInvalidLimits{Err: http.InvalidLimits{MaxBodyBytes: 33, MaxHeaderBytes: 44, IdleTimeout: 1s, ReadHeaderTimeout: 2s, ReadBodyTimeout: 3s}}
	}
	if which == 1 {
		return ServeListenFailed{Err: http.ListenFailed{Addr: ":0", Reason: "busy"}}
	}
	return ServeLocalFail{Code: 99}
}

func MakeOutcome(bad bool) Outcome {
	if bad {
		ValueInvalidLimits{Err: http.InvalidLimits{MaxBodyBytes: 11, MaxHeaderBytes: 22, IdleTimeout: 0s, ReadHeaderTimeout: 0s, ReadBodyTimeout: 0s}}
	} else {
		ValueOk{Msg: "local"}
	}
}

func DescribeOutcome(o Outcome) string {
	match o {
	case ValueInvalidLimits as v:
		"val-lim:\(v.Err.MaxBodyBytes):\(v.Err.MaxHeaderBytes)"
	case ValueOk as ok:
		"val-ok:\(ok.Msg)"
	}
}

func MakeRes(ok bool) Res[int] {
	if ok {
		GenOk[int]{Val: 7}
	} else {
		GenInvalidLimits{Err: http.InvalidLimits{MaxBodyBytes: 55, MaxHeaderBytes: 66, IdleTimeout: 0s, ReadHeaderTimeout: 0s, ReadBodyTimeout: 0s}}
	}
}

func DescribeRes(r Res[int]) string {
	match r {
	case GenInvalidLimits as v:
		"gen-lim:\(v.Err.MaxHeaderBytes)"
	case GenOk[int] as o:
		"gen-ok:\(o.Val)"
	}
}
`
	const rootSrc = `import example.com/externgounion/srv

func describeStart(which int) string {
	let _ = srv.Start(which) catch {
	case srv.ServeInvalidLimits as e:
		return "row-lim:\(e.Err.MaxBodyBytes):\(e.Err.MaxHeaderBytes)"
	case srv.ServeListenFailed as e:
		return "row-listen:\(e.Err.Reason)"
	case srv.ServeLocalFail as e:
		return "row-local:\(e.Code)"
	}
	"ok"
}

func main() needs Console {
	let a = srv.DescribeOutcome(srv.MakeOutcome(true))
	let b = srv.DescribeOutcome(srv.MakeOutcome(false))
	let c = describeStart(0)
	let d = describeStart(1)
	let e = describeStart(2)
	let f = srv.DescribeRes(srv.MakeRes(false))
	let g = srv.DescribeRes(srv.MakeRes(true))
	Console.Println("\(a)|\(b)|\(c)|\(d)|\(e)|\(f)|\(g)")
}
`
	// 1. In-process LowerIR and LowerIRSplit checks (strict IR, zero fallback).
	u := splitProgram{
		libs: []pkgSource{{"example.com/externgounion/srv", srvSrc}},
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
		t.Fatalf("program did not split cleanly: %+v", splitRep)
	}
	// Must NEVER emit duplicate local struct declarations for extern go types.
	for _, f := range files {
		if !strings.HasSuffix(f.Path, ".go") {
			continue
		}
		text := string(f.Data)
		if strings.Contains(text, "http__InvalidLimits struct") || strings.Contains(text, "http__ListenFailed struct") {
			t.Fatalf("lower emitted duplicate local struct for extern go type in %s:\n%s", f.Path, text)
		}
	}

	// 2. End-to-end native compile + run via ergo CLI under ERGO_FALLBACK=off.
	needsBuild(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "srv"), 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, data := range map[string]string{
		"ergo.mod":     "module example.com/externgounion\n",
		"srv/srv.ergo": srvSrc,
		"main.ergo":    rootSrc,
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
	want := "val-lim:11:22|val-ok:local|row-lim:33:44|row-listen:busy|row-local:99|gen-lim:66|gen-ok:7"
	if got := strings.TrimSpace(got); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}
