package lower

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMachineTimerNotPositive (fs2; runtime.md, Machines and actors 9.1)
// is a property over generated timer durations. Each program runs
// machines.RunTimed on a machine whose one state re-enters itself by an
// Emit each time its timer fires, the loop a timer of In 0s would make
// endless. A positive In arms a timer that fires end/In times; an In that
// is not positive is a defect that names the machine, the state and the
// duration for a declared machine, and the run's time for a Machine built
// from parts. No run loops: runMachineBounded stops one that does not end and the
// test fails. The seed draws the durations; ERGO_MACHINETIMER_SEED replays
// another one, and a failure prints the program.
//
// It builds and runs the programs: the defect is a Go panic of the lowered
// After wrapper and of std machines' Ergo body, and the loop it replaces is
// visible only in a run; the repository has no reference interpreter.
func TestMachineTimerNotPositive(t *testing.T) {
	seed := int64(9)
	if s := os.Getenv("ERGO_MACHINETIMER_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_MACHINETIMER_SEED: %v", err)
		}
		seed = n
	}
	r := rand.New(rand.NewSource(seed))
	type program struct {
		name, src string
		in        int64
		parts     bool
	}
	var progs []program
	var builds []*lowered
	// Each kind of In, for a declared machine and one built from parts: a
	// positive In, 0 and a negative one, each drawn by the seed.
	for i, kind := range []string{"positive", "zero", "negative"} {
		for _, parts := range []bool{false, true} {
			var in int64
			switch kind {
			case "positive":
				in = int64(r.Intn(20) + 1)
			case "negative":
				in = -int64(r.Intn(3_000_000_000) + 1)
			}
			name := fmt.Sprintf("machinetimer_%d_%t", i, parts)
			src := machineTimerProgram(in, parts)
			progs = append(progs, program{name, src, in, parts})
			builds = append(builds, mustLower(t, name, src))
		}
	}
	exes := buildAll(t, builds, true)
	if exes == nil {
		return
	}
	for _, p := range progs {
		stdout, stderr, code, ended := runMachineBounded(t, exes[p.name])
		if !ended {
			t.Fatalf("seed %d, In %d: still running at the test's deadline, stopped: a timer that is not positive loops\nsource:\n%s", seed, p.in, p.src)
		}
		if p.in > 0 {
			want := fmt.Sprintf("running %d\n", machineTimerEnd/p.in)
			if code != 0 || stdout != want {
				t.Errorf("seed %d, In %d: exit %d, stdout %q, want %q\nstderr:\n%s\nsource:\n%s", seed, p.in, code, stdout, want, firstLines(stderr, 8), p.src)
			}
			continue
		}
		in := time.Duration(p.in).String()
		want := "panic: machine Loop: the timer of state Idle{n:0} has In " + in + ", which is not positive"
		if p.parts {
			want = "panic: a Machine built from parts: the timer armed at 0s has In " + in + ", which is not positive"
		}
		if code != 2 || !strings.Contains(stderr, want) {
			t.Errorf("seed %d, In %d: exit %d, stderr does not hold %q:\n%s\nsource:\n%s", seed, p.in, code, want, firstLines(stderr, 8), p.src)
		}
	}
}

// machineTimerEnd is the end of each timed run, in nanoseconds.
const machineTimerEnd = 100

// machineTimerProgram is a machine whose state Idle re-enters itself, with
// one more output, each time its timer of In nanoseconds fires; RunTimed
// runs it with no signal until machineTimerEnd, on a bound
// testing.VirtualClock (RunTimed reads the context's one clock), as the
// declared machine Loop or as a Machine built from parts.
func machineTimerProgram(in int64, parts bool) string {
	m := "Loop{}"
	if parts {
		m = "Machine[St, Sig, int]{Initial: Idle{n: 0}, Step: step, After: timer}"
	}
	return fmt.Sprintf(`import machines
import testing
import time

type Idle { n int }
type Busy {}
type St = Idle | Busy
type Tick {}
type Halt {}
type Sig = Tick | Halt

func step(s St, e Sig) Decision[St, int] {
	match s {
	case Idle as a:
		Emit[St, int]{Next: Idle{n: a.n + 1}, Output: a.n}
	case Busy:
		Ignore
	}
}

func timer(s St) Option[Timeout[Sig]] {
	match s {
	case Idle:
		Some[Timeout[Sig]]{Value: Timeout[Sig]{In: duration(int64(%d)), Signal: Tick}}
	case Busy:
		None
	}
}

type Loop machine[St, Sig] {
	initial: Idle{n: 0},
	step:    step,
	after:   timer,
}

func main() needs Console {
	let c = testing.NewVirtualClock(0)
	with context { Scheduler: c, time.Clock: c } {
		let o = machines.RunTimed(%s, []machines.Timed[Sig]{}, duration(int64(%d))) catch {
		case machines.Rejected:
			return
		}
		match o {
		case machines.Finished[St, int]:
			Console.Println("finished")
		case machines.Running[St, int] as r:
			Console.Println("running \(len(r.Emitted))")
		}
	}
}
`, in, m, machineTimerEnd)
}
