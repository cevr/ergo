package lower

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
)

// TestMachinesStdTypesNativeRun verifies NO-PRELUDE Unit 9a end-to-end:
// a real machine + InputMachine using all 11 qualified `machines.*` types
// (Decision, Enter, Stay, Emit, Ignore, Reject, Done, Tell, Timeout, Machine,
// InputMachine) checks with 0 diagnostics, evaluates cleanly at check time,
// lowers strictly via IR (NoFallback=true), and runs natively under
// ERGO_FALLBACK=off with matching stdout.
func TestMachinesStdTypesNativeRun(t *testing.T) {
	src := `import io
import machines

type State { V int }

type Ev = Go{N int} | Bump{N int} | Loud{N int} | Whisper{N int} | Skip{} | Bad{} | Stop{}

func start(n int) State { State{V: n} }

func step(s State, e Ev) machines.Decision[State, int] {
	match e {
	case Go{N: n}:
		machines.Enter[State]{Next: State{V: s.V + n}}
	case Bump{N: n}:
		machines.Stay{Next: State{V: s.V + n}}
	case Loud{N: n}:
		machines.Emit[State, int]{Next: State{V: s.V + n}, Output: n}
	case Whisper{N: n}:
		machines.Tell{Next: State{V: s.V + n}, Output: n}
	case Skip:
		machines.Ignore
	case Bad:
		machines.Reject
	case Stop:
		machines.Done{Output: s.V}
	}
}

func afterEv(s State) Option[machines.Timeout[Ev]] {
	if s.V > 100 { Some{Value: machines.Timeout[Ev]{In: 1s, Signal: Stop}} } else { None }
}

type Counter machine[State, Ev] { initial: State{V: 0}, step: step, after: afterEv }

type Seeded machine[State, Ev] { initial: start, step: step, after: afterEv }

func describe(d machines.Decision[State, int]) int {
	match d {
	case machines.Enter{Next: n}:
		n.V
	case machines.Stay as st:
		st.Next.V
	case machines.Emit{Next: n, Output: o}:
		n.V + o
	case machines.Tell{Next: n, Output: o}:
		n.V + o
	case machines.Ignore:
		-1
	case machines.Reject:
		-2
	case machines.Done{Output: o}:
		o
	}
}

func runIt(m machines.Machine[State, Ev, int], sigs []Ev) int {
	let out = machines.Run(m, sigs) catch {
	case machines.Rejected:
		return -99
	}
	match out {
	case machines.Finished{State: s, Output: o, Emitted: xs}:
		s.V + o + xs.Len()
	case machines.Running{State: s, Emitted: xs}:
		s.V + xs.Len()
	}
}

func runSeeded(im machines.InputMachine[int, State, Ev, int], seed int, sigs []Ev) int {
	runIt(machines.Start(im, seed), sigs)
}

func main() needs io.Console {
	let sigs = []Ev{Go{N: 3}, Bump{N: 2}, Loud{N: 4}, Whisper{N: 1}, Skip, Stop}
	let r1 = runIt(Counter{}, sigs)
	let r2 = runSeeded(Seeded{}, 5, sigs)
	let r3 = runIt(Counter{}, []Ev{Bad})
	let d = describe(step(State{V: 1}, Go{N: 2})) + describe(step(State{V: 1}, Bump{N: 3})) + describe(step(State{V: 1}, Loud{N: 4})) + describe(step(State{V: 1}, Whisper{N: 5})) + describe(step(State{V: 1}, Skip)) + describe(step(State{V: 1}, Bad)) + describe(step(State{V: 1}, Stop))
	io.Console.Println("\(r1) \(r2) \(r3) \(d)")
}
`
	pkg, diags := check.CheckSource(src)
	if len(diags) > 0 {
		t.Fatalf("check failed: %v", diags)
	}
	evalOut, err := eval.RunMain(pkg, eval.DefaultBudget(), eval.Process{})
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	if evalOut.Skipped != "" {
		t.Fatalf("eval skipped: %q", evalOut.Skipped)
	}
	want := "22 32 -99 25\n"
	if evalOut.Stdout != want {
		t.Fatalf("eval stdout = %q, want %q", evalOut.Stdout, want)
	}

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "main.ergo")
	if err := os.WriteFile(srcPath, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	u := Unit{
		Package:    pkg,
		Files:      pkg.Files(),
		Sources:    []Source{{Path: srcPath, Text: src}},
		NoFallback: true,
	}
	if _, rep, err := LowerIR(u); err != nil || len(rep.Fallback) > 0 {
		t.Fatalf("LowerIR(NoFallback=true): err=%v fallback=%v", err, rep.Fallback)
	}

	needsBuild(t)
	cmd := exec.Command(ergoCLI(t), "run", srcPath)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "ERGO_FALLBACK=off", "ERGO_CACHE="+t.TempDir())
	outBytes, err := cmd.CombinedOutput()
	got := string(withoutBuildNote(outBytes))
	if err != nil {
		t.Fatalf("ergo run failed (%v):\n%s", err, got)
	}
	if got != want {
		t.Fatalf("native stdout = %q, want %q", got, want)
	}
}
