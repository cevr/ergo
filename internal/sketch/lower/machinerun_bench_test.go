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
// run of mode, and the hand Go controls. HandSig, in the build whose
// signals are interfaces, is what a Go programmer writes over that []Sig: a
// struct state and a type switch, with the step inlined; HandByte, in the
// flat build, the same loop over byte signals.
func flatBenchGo(mode string) func(string) string {
	return func(string) string {
		sig := "Increment{}"
		if mode == "Flat" {
			sig = "Sig{}.of0(Increment{})" // a flat union (flatunion.go)
		}
		src := `package main

import "testing"

const simN = 100000

func sigs() []Sig {
	xs := make([]Sig, simN)
	for i := range xs {
		xs[i] = ` + sig + `
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
		src += "\ntype handState struct{ n int }\n"
		if mode == "SigIface" {
			src += `
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
`
		}
		if mode == "Flat" {
			src += `
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
// run): the flat run over flat signals (FlatRun), the flat run over
// interface signals (SigIfaceRun, MU8's lowering), std's loop
// (InterfaceRun, the reference lowering), and the hand Go controls.
// Per transition is ns/op / 100,000. The window command interleaves them
// (MU8b bench-pending.md).
func BenchmarkMachineFlatRun(b *testing.B) {
	for _, mode := range []string{"Flat", "SigIface", "Interface"} {
		savedRun, savedUnion := machineLowering, unionLowering
		machineLowering = map[string]string{"Flat": "", "SigIface": "", "Interface": "interface"}[mode]
		unionLowering = map[string]string{"Flat": "", "SigIface": "interface", "Interface": "interface"}[mode]
		benches := []string{mode + "Run"}
		switch mode {
		case "Flat":
			benches = append(benches, "HandByte")
		case "SigIface":
			benches = append(benches, "HandSig")
		}
		emittedBench(b, "flatbench"+strings.ToLower(mode), flatBenchSource, flatBenchGo(mode), benches...)
		machineLowering, unionLowering = savedRun, savedUnion
	}
}
