package lower

import (
	"strings"
	"testing"
)

// flatBenchSource is effect-machine's "simulate" scenario (em-parity): a
// counter machine folded by machines.Run over n Increment signals.
const flatBenchSource = `import machines

type Count = Active{N int}

type Sig = Increment{} | Barrier{}

func step(s Count, e Sig) Decision[Count, int] {
	match (s, e) {
	case (Active as a, Increment):
		Stay[Count]{Next: Active{N: a.N + 1}}
	case (Active, Barrier):
		Stay[Count]{Next: s}
	}
}

type Counter machine[Count, Sig] {
	initial: Active{N: 0},
	step:    step,
}

func count(xs []Sig) int {
	let o = machines.Run(Counter{}, xs) catch {
	case machines.Rejected:
		return -1
	}
	match o {
	case machines.Finished[Count, int] as f:
		match f.State {
		case Active as a:
			a.N
		}
	case machines.Running[Count, int] as r:
		match r.State {
		case Active as a:
			a.N
		}
	}
}

func main() needs Console {
	Console.Println("\(count([]Sig{Increment, Barrier}))")
}
`

// flatBenchGo is the Go benchmark file beside the emitted program: the
// run of mode, and in the flat build the hand Go controls. HandSig is
// what a Go programmer writes over the same []Sig: a struct state and a
// type switch, with the step inlined; HandByte the same loop over byte
// signals, which the Ergo program does not hold.
func flatBenchGo(mode string) func(string) string {
	return func(string) string {
		src := `package main

import "testing"

const simN = 100000

func sigs() []Sig {
	xs := make([]Sig, simN)
	for i := range xs {
		xs[i] = Increment{}
	}
	return xs
}

func BenchmarkMODERun(b *testing.B) {
	// The machine's initial state is a package-level let that main's
	// ergoInit sets; a test binary does not run main.
	ergoInit(benchEnv)
	xs := sigs()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if count(benchEnv, xs) != simN {
			b.Fatal("count")
		}
	}
}
`
		if mode == "Flat" {
			src += `
type handState struct{ n int }

func BenchmarkHandSig(b *testing.B) {
	xs := sigs()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		s := handState{}
		for _, e := range xs {
			switch e.(type) {
			case Increment:
				s = handState{n: s.n + 1}
			case Barrier:
			}
		}
		if s.n != simN {
			b.Fatal("count")
		}
	}
}

func BenchmarkHandByte(b *testing.B) {
	xs := make([]byte, simN)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		s := handState{}
		for _, e := range xs {
			switch e {
			case 0:
				s = handState{n: s.n + 1}
			case 1:
			}
		}
		if s.n != simN {
			b.Fatal("count")
		}
	}
}
`
		}
		return strings.ReplaceAll(src, "MODE", mode)
	}
}

// BenchmarkMachineFlatRun measures a run of 100,000 signals (ns/op is per
// run): the flat run (FlatRun), std's loop (InterfaceRun, the reference
// lowering), and the hand Go controls. Per transition is ns/op / 100,000.
// The window command interleaves them (MU8 bench-pending.md).
func BenchmarkMachineFlatRun(b *testing.B) {
	for _, mode := range []string{"Flat", "Interface"} {
		saved := machineLowering
		machineLowering = map[string]string{"Flat": "", "Interface": "interface"}[mode]
		benches := []string{mode + "Run"}
		if mode == "Flat" {
			benches = append(benches, "HandSig", "HandByte")
		}
		emittedBench(b, "flatbench"+strings.ToLower(mode), flatBenchSource, flatBenchGo(mode), benches...)
		machineLowering = saved
	}
}
